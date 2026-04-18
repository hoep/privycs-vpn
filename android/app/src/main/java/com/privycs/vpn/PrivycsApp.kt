package com.privycs.vpn

import android.app.NotificationChannel
import android.app.NotificationManager
import android.graphics.Color
import android.os.Build
import com.privycs.vpn.data.ConnectionRepository
import com.privycs.vpn.data.SettingsRepository
import org.strongswan.android.logic.StrongSwanApplication

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

    lateinit var connectionRepository: ConnectionRepository
        private set

    lateinit var settingsRepository: SettingsRepository
        private set

    // Kept as a GC root for the AIDL ServiceConnection inside StatusListener;
    // losing this reference lets the OpenVPNStatusService unbind and we stop
    // receiving state/log/byte-count events from the :openvpn subprocess.
    @Suppress("unused")
    private var openVpnStatusListener: de.blinkt.openvpn.core.StatusListener? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
        connectionRepository = ConnectionRepository(this)
        settingsRepository = SettingsRepository(this)
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
