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
import kotlinx.coroutines.GlobalScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.first
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
        super.attachBaseContext(if (base != null) com.privycs.vpn.util.AppLocale.wrap(base) else null)
        // Crash handler: write the full stack trace to externalFilesDir
        // (/storage/emulated/0/Android/data/com.privycs.vpn/files/) so the
        // user can open it in any Files app without ADB or root. The
        // app-private filesDir (/data/data/com.privycs.vpn/files/) is
        // inaccessible to non-debuggable release builds. Installed in
        // attachBaseContext — earliest entrypoint, runs before any other
        // code in the app process. Chains to the previous handler so we
        // don't suppress Play Console's crash telemetry.
        if (base != null) {
            try {
                val prev = Thread.getDefaultUncaughtExceptionHandler()
                Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
                    try {
                        val ts = java.text.SimpleDateFormat(
                            "yyyyMMdd-HHmmss", java.util.Locale.US
                        ).format(java.util.Date())
                        val dir = base.getExternalFilesDir(null)
                        if (dir != null) {
                            dir.mkdirs()
                            val file = java.io.File(dir, "crash-$ts.txt")
                            file.writeText(buildString {
                                appendLine("Privycs VPN crash report")
                                appendLine("Time: ${java.util.Date()}")
                                appendLine("Version: ${com.privycs.vpn.BuildConfig.VERSION_NAME} (${com.privycs.vpn.BuildConfig.VERSION_CODE})")
                                appendLine("Thread: ${thread.name} (id=${thread.id})")
                                appendLine("Device: ${android.os.Build.MANUFACTURER} ${android.os.Build.MODEL}, Android ${android.os.Build.VERSION.RELEASE} (SDK ${android.os.Build.VERSION.SDK_INT})")
                                appendLine()
                                appendLine("== Throwable ==")
                                appendLine(android.util.Log.getStackTraceString(throwable))
                                appendLine()
                                appendLine("== All thread stacks ==")
                                for ((t, frames) in Thread.getAllStackTraces()) {
                                    appendLine("--- ${t.name} (id=${t.id}, state=${t.state}) ---")
                                    for (f in frames) appendLine("  at $f")
                                }
                            })
                        }
                    } catch (_: Throwable) { /* never block the handoff */ }
                    prev?.uncaughtException(thread, throwable)
                }
                Log.i("PrivycsApp", "attachBaseContext: crash handler installed → externalFilesDir/crash-*.txt")
            } catch (t: Throwable) {
                Log.e("PrivycsApp", "attachBaseContext: crash handler install FAILED", t)
            }
        }

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

    /** Per-network auto-tunnel rules (Phase 2). */
    lateinit var networkRulesRepository: com.privycs.vpn.data.NetworkRulesRepository
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

    /** Pro / Free entitlement — single source of truth. Created in
     *  every process (a cheap encrypted on-disk read). */
    lateinit var entitlementRepository: com.privycs.vpn.data.EntitlementRepository
        private set

    /** Google Play Billing for the one-time Pro purchase. Initialised
     *  in the main process only; null in the :openvpn subprocess. */
    var billingManager: com.privycs.vpn.billing.BillingManager? = null
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

        // Pre-warm the SettingsRepository @Volatile cache off the main
        // thread at app start. getSettingsBlocking() falls back to a
        // runBlocking { settingsFlow.first() } DataStore read when its
        // cache is still cold; ConnectCoordinator calls it inside
        // mutex.withLock on Dispatchers.Main, so a first connect/disconnect
        // that lands before the cache warms would block the main thread on
        // disk I/O (cold-start ANR window). The repository's own init
        // collector warms the cache eventually; this just forces the very
        // first read to happen here, on IO, before the user can tap
        // Connect. Cache semantics are unchanged — this only fills it
        // earlier. Cheap and idempotent (the value is also kept current by
        // the repository's settingsFlow collector).
        appScope.launch {
            try {
                settingsRepository.getSettingsBlocking()
            } catch (t: Throwable) {
                Log.w("PrivycsApp", "Settings cache pre-warm failed: ${t.message}")
            }
        }

        // Smart Decision Engine (shadow, v1.0.9): forward every tunnel-health
        // transition to the engine so it can explain what it WOULD do. The
        // StateFlow only emits on change, so this is one cheap observer; the
        // engine drives nothing. Connect/disconnect are observed from
        // ConnectCoordinator; the candidate set is the failover order.
        appScope.launch {
            com.privycs.vpn.service.TunnelHealthMonitor.state.collect { hs ->
                com.privycs.vpn.engine.EngineShadow.observeHealth(hs)
            }
        }

        // v1.0.7.3 — anonymous crash reporting. Default OFF. NO
        // runBlocking on the main thread: just observe the
        // settings flow on Dispatchers.IO and (re-)init the
        // Sentry SDK on every emission. The first emission lands
        // within milliseconds of DataStore opening the file, so
        // crash reporting comes online well before the user can
        // trigger any UI. Until then the redaction-pipeline +
        // captureException paths are fast no-ops.
        //
        // Why this is safer than the runBlocking I shipped in
        // v1.0.7-1.0.7.2: a corrupt DataStore (rare but seen on
        // upgrade across major Android versions) made the first()
        // call hang forever on the main thread, ANR'd the app at
        // startup, and the user reported "crashed sofort bei
        // start" 2026-05-31. With the IO-observer approach the
        // worst case is "crash reporting never inits" instead of
        // "app never starts".
        kotlinx.coroutines.GlobalScope.launch(kotlinx.coroutines.Dispatchers.IO) {
            try {
                settingsRepository.settingsFlow.collect { s ->
                    try {
                        com.privycs.vpn.util.CrashReporter.init(this@PrivycsApp, s.crashReportsEnabled)
                    } catch (t: Throwable) {
                        // Sentry init failure must NEVER take the app
                        // down. Logged for triage, then we move on.
                        Log.w("PrivycsApp", "CrashReporter init failed: ${t.message}")
                    }
                }
            } catch (t: Throwable) {
                Log.w("PrivycsApp", "Settings observer for CrashReporter failed: ${t.message}")
            }
        }

        networkRulesRepository = com.privycs.vpn.data.NetworkRulesRepository(this)
        poolStateRepository = PoolStateRepository(this)
        poolRepository = PoolRepository(this, poolStateRepository)
        // MMDB-first resolver chain: IP-endpoints get exact country
        // via the bundled MMDB; the rare hostname-only-with-pattern
        // case falls back to filename parsing.
        mmdbResolver = MmdbCountryResolver(this)
        val combinedResolver = CombinedCountryResolver(mmdbResolver, HostnameCountryResolver())
        poolImporter = PoolImporter(this, combinedResolver)
        selfIpDetector = SelfIpDetector(applicationContext, mmdbResolver)

        // Pro entitlement + Play Billing. EntitlementRepository is a
        // cheap on-disk read, created in every process; the Play
        // BillingClient runs in the main process only.
        entitlementRepository = com.privycs.vpn.data.EntitlementRepository(this)
        if (isMainProcess()) {
            billingManager = com.privycs.vpn.billing.BillingManager(
                this, entitlementRepository,
            ).also { it.start() }
        }

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
            val s = settingsRepository.getSettingsBlocking()
            // Convert any legacy "simple Connect-on-Demand" config into
            // network rules (idempotent — a no-op once migrated), then
            // gate the monitor on whether ANY rule exists. The post-
            // conversion engine is rule-driven; the legacy
            // connectOnDemand.enabled flag no longer starts it.
            networkRulesRepository.migrateLegacyCod(settingsRepository)
            networkRulesRepository.awaitLoaded()
            val codEnabled = networkRulesRepository.rules.value.isNotEmpty()
            if (codEnabled) {
                try {
                    com.privycs.vpn.service.NetworkMonitor.getInstance(applicationContext).start()
                    Log.i("PrivycsApp", "NetworkMonitor auto-started (on-demand enabled)")
                } catch (t: Throwable) {
                    Log.e("PrivycsApp", "NetworkMonitor auto-start failed", t)
                }

                // v0.9.14.97 — also register the process-death-surviving
                // PendingIntent-based NetworkCallback. The runtime callback
                // started inside NetworkMonitor.start() above dies with the
                // process if an aggressive OEM battery-killer terminates
                // our foreground service. The PendingIntent variant
                // (Manifest-bound) keeps firing even after our process is
                // dead — Android spawns a fresh process to handle it.
                // Idempotent.
                try {
                    com.privycs.vpn.util.CodWakeRegistrar.register(applicationContext)
                } catch (t: Throwable) {
                    Log.e("PrivycsApp", "CodWakeRegistrar registration failed", t)
                }

                // v0.9.14.75 — opt-in foreground-keepalive: if the
                // user has enabled "Always monitor" in settings, fire
                // ACTION_START_MONITOR so PrivycsVpnService comes up
                // as a foreground service (without a tunnel) and the
                // NetworkMonitor + system NetworkCallback survive
                // Doze. The service is idempotent — if a tunnel is
                // already up we skip the monitor-mode notification
                // and let the connection one stay.
                //
                // v0.9.15.24: gate now auto-fires when COD is on,
                // not just when the user explicitly opted in via
                // keepMonitorAlive. Reason: Doze blocks both the
                // runtime NetworkCallback (in-process) AND the
                // PendingIntent-based CodWakeReceiver (Manifest
                // broadcast) — both delivery paths are deferred to
                // the next maintenance window (~15 min). Only a
                // running foreground service is reliably exempt
                // from Doze, which lets WiFi-change events reach
                // NetworkMonitor real-time. User-reported symptom:
                // "phone joins except-list WiFi while in Doze, VPN
                // stays connected, only disconnects on wake."
                // Treating COD-on as implicit consent to a
                // persistent notification is the trade-off for
                // reliable rule reaction in standby.
                //
                // keepMonitorAlive setting is preserved for users
                // who want the FGS even WITHOUT COD (rare, but
                // valid for tunnel-health monitoring outside the
                // rule-trigger context).
                if (s.keepMonitorAlive || codEnabled) {
                    try {
                        val intent = android.content.Intent(
                            applicationContext,
                            com.privycs.vpn.service.PrivycsVpnService::class.java,
                        ).apply {
                            action = com.privycs.vpn.service.PrivycsVpnService.ACTION_START_MONITOR
                        }
                        androidx.core.content.ContextCompat.startForegroundService(
                            applicationContext, intent
                        )
                        Log.i("PrivycsApp", "Foreground-keepalive monitor started")
                    } catch (t: Throwable) {
                        Log.e("PrivycsApp", "Foreground-keepalive start failed", t)
                    }
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

        // ---- Event notifications (see util/EventNotifier) ----
        // security: kill-switch sinkhole engaged/cleared. Single
        // authoritative source = KillSwitchManager.state.
        appScope.launch {
            var last: com.privycs.vpn.util.KillSwitchManager.State? = null
            com.privycs.vpn.util.KillSwitchManager.state.collect { st ->
                if (st == last) return@collect
                last = st
                if (st == com.privycs.vpn.util.KillSwitchManager.State.SINKHOLE) {
                    com.privycs.vpn.util.EventNotifier
                        .sinkholeEngaged(applicationContext)
                } else {
                    com.privycs.vpn.util.EventNotifier
                        .sinkholeCleared(applicationContext)
                }
            }
        }
        // status: COD auto-connect. Rising edge of "Connected with
        // source == ON_DEMAND" on the Coordinator (the accurate point;
        // the matching auto-disconnect notification is fired inline at
        // NetworkMonitor's ON_DEMAND requestDisconnect site).
        appScope.launch {
            var wasCod = false
            com.privycs.vpn.util.ConnectCoordinator.state.collect { st ->
                val isCod = st is
                    com.privycs.vpn.util.ConnectCoordinator.State.Connected &&
                    st.source ==
                    com.privycs.vpn.util.ConnectCoordinator.IntentSource.ON_DEMAND
                if (isCod && !wasCod) {
                    val reason = try {
                        com.privycs.vpn.service.NetworkMonitor
                            .getInstance(applicationContext)
                            .networkState.value.ruleMatch
                    } catch (_: Throwable) {
                        ""
                    }
                    com.privycs.vpn.util.EventNotifier
                        .codConnected(applicationContext, reason)
                }
                wasCod = isCod
            }
        }
        // NOTE: the verbose per-networkState "diagnostics" observer
        // was removed — it posted a (separate-channel) notification
        // on every network event, so a single Wi-Fi/mobile change
        // could surface up to 3 notifications (status + diagnostics
        // + occasionally security). Diagnostics is now strictly
        // opt-in: EventNotifier.diagnostics() still exists but is
        // never auto-fired. Net result: at most ONE notification per
        // real COD action (connect/disconnect/failover, coalesced on
        // a single id), plus the rare security alert.

        // WorkManager-backed auto-tunnel backstop. NetworkCallback in
        // NetworkMonitor stays the primary fast-reaction path; the
        // worker is the slow safety net (15-min periodic, the OS
        // floor) that survives Doze, battery-saver, force-stop and
        // process-death cycles where the in-process scope would
        // otherwise miss events. KEEP existing schedule on app
        // restart so the next-tick timer is preserved instead of
        // reset.
        try {
            val constraints = androidx.work.Constraints.Builder()
                .setRequiredNetworkType(androidx.work.NetworkType.CONNECTED)
                .build()
            val request = androidx.work.PeriodicWorkRequestBuilder<
                com.privycs.vpn.service.AutoTunnelWorker
            >(15, java.util.concurrent.TimeUnit.MINUTES)
                .setConstraints(constraints)
                .setBackoffCriteria(
                    androidx.work.BackoffPolicy.EXPONENTIAL,
                    30, java.util.concurrent.TimeUnit.SECONDS,
                )
                .build()
            androidx.work.WorkManager.getInstance(applicationContext)
                .enqueueUniquePeriodicWork(
                    "auto-tunnel-backstop",
                    androidx.work.ExistingPeriodicWorkPolicy.KEEP,
                    request,
                )
            Log.i("PrivycsApp", "AutoTunnelWorker scheduled (15-min periodic)")
        } catch (t: Throwable) {
            Log.e("PrivycsApp", "AutoTunnelWorker scheduling failed", t)
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

        // Event-notification channels (security / status / diagnostics).
        // Configured by the user in Android's per-app notification
        // settings; the Settings screen deep-links there.
        com.privycs.vpn.util.EventNotifier.createChannels(manager)
    }

    /**
     * True when running in the app's main process (not the `:openvpn`
     * subprocess). PrivycsApp.onCreate runs in every process; the Play
     * BillingClient only belongs in the main one.
     */
    private fun isMainProcess(): Boolean {
        val name = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            android.app.Application.getProcessName()
        } else {
            val pid = android.os.Process.myPid()
            (getSystemService(ACTIVITY_SERVICE) as? android.app.ActivityManager)
                ?.runningAppProcesses?.firstOrNull { it.pid == pid }?.processName
        }
        return name == null || !name.contains(':')
    }

    companion object {
        const val NOTIFICATION_CHANNEL_VPN = "vpn_status"
        const val NOTIFICATION_ID_VPN = 1

        lateinit var instance: PrivycsApp
            private set
    }
}
