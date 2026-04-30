package com.privycs.vpn

import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.graphics.Color
import android.os.Build
import android.util.Log
import com.privycs.vpn.data.CombinedCountryResolver
import com.privycs.vpn.data.ConnectionRepository
import com.privycs.vpn.data.HostnameCountryResolver
import com.privycs.vpn.data.MmdbCountryResolver
import com.privycs.vpn.data.PoolImporter
import com.privycs.vpn.data.PoolRepository
import com.privycs.vpn.data.PoolStateRepository
import com.privycs.vpn.data.SelfIpDetector
import com.privycs.vpn.data.SettingsRepository
import com.privycs.vpn.data.models.VpnProtocol
import de.blinkt.openvpn.VpnProfile
import de.blinkt.openvpn.core.ConfigParser
import de.blinkt.openvpn.core.PrivycsStatusListenerBridge
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import org.strongswan.android.logic.StrongSwanApplication
import java.io.StringReader
import java.util.UUID

// Extends StrongSwanApplication so super.onCreate() and the class's static
// initializer run when our app boots:
//   - static block:  System.loadLibrary("androidbridge")
//                    Security.addProvider(LocalCertificateKeyStoreProvider)
//   - onCreate():    mContext + mInstance setup, DatabaseHelper init,
//                    ManagedConfigurationService, TrustedCertificateManager,
//                    UserCertificateManager, broadcast receiver registration.
// Required so CharonVpnService can use StrongSwanApplication.getContext()
// and the native JNI lookups succeed on first tunnel start.
class PrivycsApp : StrongSwanApplication() {

    override fun attachBaseContext(base: Context?) {
        super.attachBaseContext(base)
        // Seed OpenVPN's GlobalPreferences BEFORE onCreate, before Application
        // and Service lifecycles interleave. In our `:openvpn` subprocess, the
        // run-24611939018 and -24612148380 builds crashed at
        // OpenVPNService.onStartCommand:549 with "Global preferences instance
        // is not set" even though setInstance was called later in
        // bootstrapOpenVpnIntegration(). Some Samsung One UI + Android 16
        // combinations appear to race Service onStartCommand past
        // Application.onCreate in foreground-service start paths; moving the
        // seed to attachBaseContext eliminates the window - attachBaseContext
        // is guaranteed to run before ANY other lifecycle callback on the
        // process.
        // Direct Log.i so we see it even if PrivycsLogger's file writer is not
        // ready yet (filesDir unavailable until onCreate).
        try {
            de.blinkt.openvpn.core.GlobalPreferences.setInstance(false, false, false)
            Log.i("PrivycsApp", "attachBaseContext: GlobalPreferences seeded (pid=${android.os.Process.myPid()})")
        } catch (t: Throwable) {
            Log.e("PrivycsApp", "attachBaseContext: GlobalPreferences seed FAILED", t)
        }
    }

    lateinit var connectionRepository: ConnectionRepository
        private set

    lateinit var settingsRepository: SettingsRepository
        private set

    /** Pool runtime state (active member, slot, unreachable flags, cursors). */
    lateinit var poolStateRepository: PoolStateRepository
        private set

    /** Pool definitions (pools.json). */
    lateinit var poolRepository: PoolRepository
        private set

    /** Pool import pipeline. */
    lateinit var poolImporter: PoolImporter
        private set

    /** MMDB-backed country lookup for both IP-endpoints and self-IP. */
    lateinit var mmdbResolver: MmdbCountryResolver
        private set

    /** User's public-IP -> country detector. Used by Geo-Nearest. */
    lateinit var selfIpDetector: SelfIpDetector
        private set

    // Kept as a GC root for the AIDL ServiceConnection inside StatusListener;
    // losing this reference lets the OpenVPNStatusService unbind and we stop
    // receiving state/log/byte-count events from the :openvpn subprocess.
    @Suppress("unused")
    private var openVpnStatusListener: de.blinkt.openvpn.core.StatusListener? = null

    // Process-lifetime coroutine scope. Public so UI callers can
    // launch persistence-critical work here that MUST survive the
    // caller Composable leaving the composition (e.g. a user toggling
    // a setting and immediately navigating away / killing the app).
    // rememberCoroutineScope() in Compose is bound to the composition
    // lifecycle, so a fast back-out can cancel an in-flight
    // DataStore.edit before disk I/O finishes, dropping the write -
    // observed concretely on the Connect-on-Demand toggle in v0.9.9.5.
    val appScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    override fun onCreate() {
        super.onCreate()
        instance = this
        connectionRepository = ConnectionRepository(this)
        settingsRepository = SettingsRepository(this)
        poolStateRepository = PoolStateRepository(this)
        poolRepository = PoolRepository(this, poolStateRepository)
        // MMDB-first resolver chain: IP-endpoints get exact country
        // via the bundled MMDB; the rare hostname-only-with-pattern
        // case falls back to filename parsing.
        mmdbResolver = MmdbCountryResolver(this)
        val combinedResolver = CombinedCountryResolver(mmdbResolver, HostnameCountryResolver())
        poolImporter = PoolImporter(this, combinedResolver)
        selfIpDetector = SelfIpDetector(applicationContext, mmdbResolver)

        // Warm the SelfIp cache on a background coroutine. Pool
        // activation reads this cache synchronously in the connect
        // critical path (PoolTunnelOps.userCountry); without a warm
        // cache the first activation falls through to "" and
        // Geo-Nearest degrades to random within the alphabetically-
        // first region (Africa for an Austrian user, hence the
        // Nigeria/Tokyo picks observed in v0.9.11.46).
        //
        // 9-second timeout matches the in-detector default. Network
        // probes happen sequentially (Cloudflare → ipify →
        // ifconfig.me) with 3-second per-probe timeouts; the worst
        // case is all three failing serially. The cache is also
        // refreshed on network change via the detector's network
        // callback, so this single warmup is enough for normal
        // app sessions.
        appScope.launch {
            // Pre-warm MMDB FIRST so the SelfIp probe's
            // countryCodeBlocking call finds the reader already open
            // instead of doing the synchronous extract+open inline
            // (~50-200ms + I/O on the IO dispatcher). Without this
            // pre-warm the first pool connect blocked the connect
            // path's first geo lookup.
            try {
                mmdbResolver.prewarm()
            } catch (e: Exception) {
                com.privycs.vpn.util.PrivycsLogger.w(
                    "PrivycsApp", "MMDB prewarm failed: ${e.message}"
                )
            }
            try {
                val country = selfIpDetector.countryFor()
                com.privycs.vpn.util.PrivycsLogger.i(
                    "PrivycsApp",
                    "SelfIp warm complete: country=${country.ifEmpty { "<unknown>" }}"
                )
            } catch (e: Exception) {
                com.privycs.vpn.util.PrivycsLogger.w(
                    "PrivycsApp", "SelfIp warm failed: ${e.message}"
                )
            }
        }

        // Load persisted Always-On detection flag so the UI on first
        // frame already knows whether disconnect should go through the
        // pause-or-settings bottom-sheet rather than straight teardown.
        com.privycs.vpn.util.AlwaysOnDetector.init(this)
        createNotificationChannels()
        bootstrapOpenVpnIntegration()
        // Guaranteed first log entry every app start so the Logs screen
        // reveals whether the tee writer is working. If THIS line is
        // missing from filesDir/privycs-vpn.log the logger itself is
        // broken; if it IS present but connect/disconnect events are
        // missing the wiring is off. Useful distinction when diagnosing
        // "no log entries yet" reports.
        com.privycs.vpn.util.PrivycsLogger.i(
            "PrivycsApp",
            "App boot - version ${com.privycs.vpn.BuildConfig.VERSION_NAME} (${com.privycs.vpn.BuildConfig.VERSION_CODE})"
        )
        // Pre-load OpenVPN profiles into the ProfileManager singleton on
        // a background thread. Without this, OpenVPNService in whichever
        // process it lands in (Samsung One UI still spawns :openvpn for
        // VpnService isolation despite our manifest being subprocess-
        // free) hits the race: profile saved at connect time has not yet
        // propagated through SharedPreferences MODE_MULTI_PROCESS sync
        // -> 10s of "Used x 101 tries to get current version (-1/1) of
        // the profile" -> NullPointerException in
        // ProfileManager.notifyProfileVersionChanged. Running this at
        // app startup gives SharedPreferences seconds/minutes to flush
        // before the user ever taps Connect - same timing pattern as
        // upstream ICS-OpenVPN's "Import then Connect" UI workflow.
        appScope.launch { preloadOpenVpnProfiles() }

        // Auto-start NetworkMonitor if on-demand is enabled in persisted
        // settings. Previously NetworkMonitor.start was only called from
        // SettingsScreen (when the user toggled the switch) and from
        // BootReceiver (boot broadcast). If the user enabled on-demand in
        // a prior session and then launched the app fresh (e.g. from the
        // launcher icon rather than after a boot), the monitor never
        // started - so the VpnStatus listener that triggers
        // auto-reconnect after a manual disconnect was never wired up.
        // Users reported repeatedly: "manual disconnect while on-demand
        // rules match does not reconnect" - root cause exactly this.
        appScope.launch {
            val codEnabled = settingsRepository.getSettingsBlocking().connectOnDemand.enabled
            if (codEnabled) {
                try {
                    com.privycs.vpn.service.NetworkMonitor.getInstance(applicationContext).start()
                    Log.i("PrivycsApp", "NetworkMonitor auto-started (on-demand enabled)")
                } catch (t: Throwable) {
                    Log.e("PrivycsApp", "NetworkMonitor auto-start failed", t)
                }
            }
        }

        // Pool keepalive watcher - independent of COD. When the user
        // has a pool selected, the watcher reconnects the pool after
        // any non-VPN network restoration event (Doze release, WiFi
        // re-association, mobile-data toggle, airplane-mode off).
        // Closes the "pool stopped overnight" hole where a Doze-
        // induced tunnel drop combined with the all-members-
        // unreachable connectivity gate left the pool stuck until
        // the user opened the app and tapped Connect manually.
        try {
            com.privycs.vpn.service.PoolKeepaliveWatcher.start(applicationContext)
        } catch (t: Throwable) {
            Log.e("PrivycsApp", "PoolKeepaliveWatcher start failed", t)
        }
    }

    /**
     * Walk every stored connection, parse each OpenVPN ProtocolConfig
     * into a VpnProfile, force the profile's UUID to our stable
     * VpnConnection.id, and persist synchronously to ProfileManager +
     * SharedPreferences vpnlist. OpenVpnTunnel.connect() later hands
     * the same VpnConnection.id in as `stableConnectionId`, so the
     * profile it parses shares the UUID of the pre-loaded one and
     * ProfileManager.get() hits the cache without retrying.
     */
    private fun preloadOpenVpnProfiles() {
        try {
            val connections = connectionRepository.connections
            for (conn in connections) {
                val ovpnCfg = conn.getProtocol(VpnProtocol.OPENVPN) ?: continue
                try {
                    val parser = ConfigParser()
                    parser.parseConfig(StringReader(ovpnCfg.configContent))
                    val profile: VpnProfile = parser.convertProfile()
                    profile.mName = conn.name
                    profile.mUsername = null
                    profile.mPassword = null
                    profile.mAuthenticationType = VpnProfile.TYPE_CERTIFICATES
                    // Stable UUID: use the VpnConnection.id verbatim (already
                    // a UUID string). Must match what OpenVpnTunnel.connect
                    // uses via forceProfileUuid at connect time.
                    PrivycsStatusListenerBridge.forceProfileUuid(profile, conn.id)
                    PrivycsStatusListenerBridge.persistProfileSync(applicationContext, profile)
                    Log.i("PrivycsApp", "Pre-loaded OpenVPN profile: name=${conn.name} uuid=${conn.id}")
                } catch (e: Throwable) {
                    Log.w("PrivycsApp", "Failed to pre-load OpenVPN profile '${conn.name}': ${e.message}")
                }
            }
        } catch (t: Throwable) {
            Log.w("PrivycsApp", "preloadOpenVpnProfiles outer failure: ${t.message}")
        }
    }

    /**
     * Replicate the parts of ics-openvpn's ICSOpenVPNApplication we need.
     * We can't extend that class (we already extend StrongSwanApplication
     * for IPSec's native JNI init), so we bolt the two pieces OpenVPNService
     * actually relies on directly:
     *
     *   1. The three notification channels OpenVPNService posts into
     *      (background / new-status / user-request). Without these any
     *      startForeground call from OpenVPNService on API 26+ throws
     *      IllegalStateException and the :openvpn process crashes on
     *      first connect.
     *
     *   2. A StatusListener bound in the MAIN process. OpenVPNService runs
     *      in :openvpn and pushes state/log/byte-count updates into
     *      OpenVPNStatusService via AIDL; StatusListener is the main-side
     *      receiver that redistributes them through VpnStatus singletons
     *      so our OpenVpnTunnel.stateListener fires. Without it the tunnel
     *      connects but the UI never leaves CONNECTING.
     *
     * Both are safe to run even when the user never touches OpenVPN -
     * creating channels is idempotent, and StatusListener binds lazily
     * the first time OpenVPNService comes up.
     */
    private fun bootstrapOpenVpnIntegration() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            createOpenVpnNotificationChannels()
        }

        // Seed the GlobalPreferences singleton. OpenVPNService.onStartCommand
        // -> showNotification() -> addVpnActionsToNotification() calls
        // GlobalPreferences.getForceConnected() UNCONDITIONALLY at line 429;
        // if the singleton has not been created the call throws
        // "Global preferences instance is not set" and the :openvpn
        // subprocess hard-crashes before the tunnel even starts.
        //
        // Upstream ICSOpenVPNApplication seeds this via
        // AppRestrictions.checkRestrictions() which only overrides the
        // defaults (false/false/false) when an MDM policy exists; we have
        // no MDM integration, so the bare defaults are fine. Running this
        // in Application.onCreate means it executes in BOTH the main
        // process AND the `:openvpn` subprocess (same Application class
        // runs in every process of the app), which is exactly what we
        // need because GlobalPreferences is a per-process static.
        try {
            de.blinkt.openvpn.core.GlobalPreferences.setInstance(false, false, false)
        } catch (t: Throwable) {
            com.privycs.vpn.util.PrivycsLogger.e(
                "PrivycsApp",
                "Failed to init OpenVPN GlobalPreferences: ${t.message}",
                t
            )
        }

        try {
            // StatusListener.init(Context) is package-private in
            // de.blinkt.openvpn.core; PrivycsStatusListenerBridge.install
            // lives in that package (inside :openvpn-lib's own sources)
            // and re-exposes it. Returned listener is stored so its
            // internal AIDL ServiceConnection is not GC'd.
            openVpnStatusListener =
                de.blinkt.openvpn.core.PrivycsStatusListenerBridge.install(applicationContext)
        } catch (t: Throwable) {
            // If this fails the OpenVPN tunnel will still start, but the UI
            // won't receive state updates. Log loudly so the Logs screen
            // surfaces the root cause instead of an invisible degradation.
            com.privycs.vpn.util.PrivycsLogger.e(
                "PrivycsApp",
                "Failed to init OpenVPN StatusListener: ${t.message}",
                t
            )
        }
    }

    private fun createOpenVpnNotificationChannels() {
        val manager = getSystemService(NotificationManager::class.java) ?: return
        // Channel IDs are the literal constants declared in
        // de.blinkt.openvpn.core.OpenVPNService. Keep them in sync with
        // the upstream class - they're also read out of vendor resources
        // for the channel name strings.
        fun mkChannel(id: String, nameResId: Int, importance: Int, lightColor: Int, lights: Boolean) {
            val channel = NotificationChannel(id, getString(nameResId), importance).apply {
                enableLights(lights)
                setLightColor(lightColor)
            }
            manager.createNotificationChannel(channel)
        }
        mkChannel(
            de.blinkt.openvpn.core.OpenVPNService.NOTIFICATION_CHANNEL_BG_ID,
            de.blinkt.openvpn.R.string.channel_name_background,
            NotificationManager.IMPORTANCE_MIN,
            Color.DKGRAY,
            lights = false
        )
        mkChannel(
            de.blinkt.openvpn.core.OpenVPNService.NOTIFICATION_CHANNEL_NEWSTATUS_ID,
            de.blinkt.openvpn.R.string.channel_name_status,
            NotificationManager.IMPORTANCE_LOW,
            Color.BLUE,
            lights = true
        )
        mkChannel(
            de.blinkt.openvpn.core.OpenVPNService.NOTIFICATION_CHANNEL_USERREQ_ID,
            de.blinkt.openvpn.R.string.channel_name_userreq,
            NotificationManager.IMPORTANCE_HIGH,
            Color.CYAN,
            lights = true
        )
    }

    private fun createNotificationChannels() {
        val channel = NotificationChannel(
            NOTIFICATION_CHANNEL_VPN,
            getString(R.string.vpn_notification_channel),
            NotificationManager.IMPORTANCE_LOW
        ).apply {
            description = "Shows VPN connection status"
            setShowBadge(false)
        }

        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(channel)
    }

    companion object {
        const val NOTIFICATION_CHANNEL_VPN = "vpn_status"
        const val NOTIFICATION_ID_VPN = 1

        lateinit var instance: PrivycsApp
            private set
    }
}
