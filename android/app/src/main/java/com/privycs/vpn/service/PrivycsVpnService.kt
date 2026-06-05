package com.privycs.vpn.service

import android.app.Notification
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.Build
import androidx.core.app.NotificationCompat
import com.privycs.vpn.MainActivity
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.data.models.VpnStatus
import com.privycs.vpn.util.PrivycsLogger
import com.privycs.vpn.widget.VpnWidget
import de.blinkt.openvpn.core.OpenVPNService
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

class PrivycsVpnService : VpnService() {

    companion object {
        private const val TAG = "PrivycsVpnService"

        const val ACTION_CONNECT = "com.privycs.vpn.CONNECT"
        const val ACTION_DISCONNECT = "com.privycs.vpn.DISCONNECT"
        const val ACTION_KILL_SWITCH_RETRY = "com.privycs.vpn.KILL_SWITCH_RETRY"
        const val ACTION_ENGAGE_SINKHOLE = "com.privycs.vpn.ENGAGE_SINKHOLE"

        // v0.9.14.75: opt-in foreground-keepalive for on-demand
        // reaction in standby. PrivycsApp fires START_MONITOR at app
        // start when both Connect-on-Demand and KeepMonitorAlive are
        // enabled in settings; the service then runs as a foreground
        // service WITHOUT a tunnel, just to keep NetworkMonitor's
        // 30 s tick + NetworkCallback alive across Doze. STOP_MONITOR
        // tears it back down (notification action or settings toggle).
        const val ACTION_START_MONITOR = "com.privycs.vpn.START_MONITOR"
        const val ACTION_STOP_MONITOR = "com.privycs.vpn.STOP_MONITOR"

        // Pool actions — fired by PoolAlarmReceiver via AlarmManager
        // and by direct user-tap on a pool-activate button.
        const val ACTION_POOL_CONNECT = "com.privycs.vpn.POOL_CONNECT"
        const val ACTION_POOL_PRE_WARM = "com.privycs.vpn.POOL_PRE_WARM"
        const val ACTION_POOL_ROTATE = "com.privycs.vpn.POOL_ROTATE"

        const val EXTRA_CONNECTION_ID = "connection_id"
        const val EXTRA_PROTOCOL = "protocol"
        const val EXTRA_CONFIG_CONTENT = "config_content"
        const val EXTRA_CONNECTION_NAME = "connection_name"
        const val EXTRA_POOL_ID = "pool_id"

        // v0.9.14.69 — dropped from 2000 → 1000 to match desktop's
        // statusEmitterInterval bump. Connection-time + traffic feel
        // live instead of stepping in 2 s chunks. Both tunnel
        // implementations' getStatus() is cheap enough that doubling
        // the call rate is invisible in CPU/battery profiles.
        private const val STATUS_POLL_INTERVAL_MS = 1000L

        // Up-timeout verify-phase budget. After a protocol-specific
        // connect() returns successfully we poll the tunnel for live
        // traffic (rxBytes growth or localAddress assignment) up to
        // this many ms before declaring the handshake silently dead
        // and triggering failover. 20 s is a balance:
        //   - too short → flaky-network false positives cycle through
        //     every config in the connection's bag before any of them
        //     gets a fair shot at completing a handshake
        //   - too long → user stares at "Connecting…" for ages when
        //     the first config really is dead and we should rotate
        // Mirrors desktop's failoverWatchdog default. AWG/WG normally
        // see a peer-response within 1-3 s on a healthy network;
        // 20 s leaves room for slow cellular + retries.
        private const val UP_TIMEOUT_MS = 20_000L

        // Poll cadence inside the verify-phase. Cheaper than the
        // lifetime status poller because it stops the moment we see
        // any traffic. 500 ms picks up sub-second handshake responses
        // without burning CPU on per-tick getStatus() calls.
        private const val UP_VERIFY_POLL_MS = 500L
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    // Tracks the currently-running status-poll coroutine so a re-call
    // of startStatusPolling can cancel the previous one before
    // launching a new one. Without this, every Connect / Reconnect /
    // PoolRotation entry-point spawned a fresh poller without
    // cancelling the previous, and they accumulated. User found two
    // pollers at iteration=850 + iteration=403 running concurrently
    // for the same WIREGUARD protocol — a poller leaked from each
    // pool rotation. v0.9.14.16 fix.
    private var statusPollingJob: kotlinx.coroutines.Job? = null

    // v0.9.15.46: vanilla WireGuard is now also served by the
    // amneziawg-android GoBackend (AmneziaWG-go is a strict superset of
    // wireguard-go; the Config parser accepts vanilla WG configs with
    // all AWG-specific Optional fields defaulting to empty). Eliminates
    // the long-standing "switching to WireGuard hangs 90s" bug: the
    // `com.wireguard.android:tunnel` Maven AAR shipped its own
    // `GoBackend$VpnService` whose manifest entry did not always survive
    // merger (v0.9.15.39 added an explicit declaration but the failure
    // mode persisted on switch-to-WG after another protocol's lifecycle
    // destroyed the cached static CompletableFuture<VpnService> in the
    // library). One backend, one VpnService internal class, one code
    // path — no more asymmetry. The `goBackend` + `wireGuardTunnel`
    // fields and the `com.wireguard.*` import were removed alongside.
    private var awgBackend: org.amnezia.awg.backend.GoBackend? = null
    private var amneziaTunnel: AmneziaTunnel? = null
    private var openVpnTunnel: OpenVpnTunnel? = null
    private var ipSecTunnel: IpSecTunnel? = null

    // Serialises all coroutines that mutate tunnel state
    // (handleConnect's teardown+create chain, handleDisconnect's
    // teardown, forceTeardownAfterSinkhole, pool-failure teardowns).
    // Pre-fix these ran on the same `scope` without serialisation,
    // so two intents arriving back-to-back (rapid user re-taps,
    // ConnectCoordinator USER-preempts a running connect, switchConfig
    // disconnect-then-connect overlapping with TunnelHealthMonitor
    // recovery, etc.) launched concurrent coroutines that read/wrote
    // the same singleton tunnel fields. Symptom on Android: the
    // user's "Netzwerkverbindung crashed beim mehrmaligen
    // Wiederverbinden" — VpnService.Builder.establish() of the new
    // tunnel collided with the old tunnel's still-running goroutines,
    // leaving a half-up TUN fd that blocked all traffic until the
    // service died and Always-On kicked back in.
    //
    // teardownAllProtocols itself is intentionally NOT wrapped — it
    // is the raw inner operation. Callers are responsible for
    // holding the mutex.
    private val tunnelMutex = Mutex()
    private var currentConnectionName: String = ""
    private var currentConnectionId: String = ""
    private var currentProtocol: VpnProtocol? = null
    private var connectStartTime: Long = 0L

    // Kill-Switch sinkhole tun fd. When non-null, a block-all tunnel
    // is established via VpnService.Builder and all traffic is
    // dropped at the tun interface. Cleared when the user toggles
    // Kill Switch off or a real tunnel replaces it.
    private var sinkholeTunFd: android.os.ParcelFileDescriptor? = null

    // v0.9.15.74 (B-4): sinkhole intent active (state SINKHOLE) but
    // VpnService.Builder.establish() failed, so no block-all fd
    // exists and traffic is NOT actually blocked. Drives an honest
    // notification instead of the reassuring "traffic blocked"
    // template; reset on a successful establish / on leaving the
    // sinkhole. The 3s watchdog keeps retrying establish meanwhile.
    @Volatile
    private var sinkholeEstablishFailed = false

    // v0.9.15.74 (B-4 hybrid persistence): records whether the Kill
    // Switch was actively protecting (state ARMED/SINKHOLE) at the
    // last observed transition. Read on a START_STICKY null-intent
    // respawn (OEM killed our running process) to re-engage the
    // sinkhole; a cold device boot triggers no sticky restart so it
    // is never consulted there. Plain SharedPreferences for a fast
    // synchronous read at service start (no DataStore coroutine).
    private val killSwitchPrefs by lazy {
        getSharedPreferences("kill_switch", Context.MODE_PRIVATE)
    }

    // Kill-Switch network watcher. Android-level onLost() fires
    // when the default non-VPN network goes away - e.g. airplane
    // mode, WiFi + mobile both off, device loses signal. Most
    // tunnel plugins take 30-90s to report the drop themselves
    // (WireGuard handshake timeout, IPSec DPD, OpenVPN ping-
    // restart), so relying on them alone leaves a long window
    // where traffic could leak. This callback closes that gap.
    private var killSwitchNetworkCallback: android.net.ConnectivityManager.NetworkCallback? = null

    override fun onCreate() {
        super.onCreate()
        // v0.9.15.46: single backend for both WG and AWG protocols.
        // The amneziawg-android GoBackend's Config parser accepts
        // vanilla WG configs unchanged; the underlying wireguard-go
        // fork behaves identically to vanilla wg-go when all AWG-
        // Optional fields are empty. See `amneziaTunnel` doc above.
        awgBackend = org.amnezia.awg.backend.GoBackend(this)
        PrivycsLogger.d(TAG, "VPN service created")

        // Cold-start zombie cleanup: a previous PrivycsVpnService instance may
        // have crashed (process death, OOM kill, OS restart) and left a
        // SEPARATE foreground service running. ics-openvpn's OpenVPNService is
        // its own android.app.Service — it survives PrivycsVpnService death
        // and keeps its UDP socket open, sending keepalives to our gateway.
        // teardownAllProtocols() walks the three singleton tunnel fields
        // (wireGuardTunnel/openVpnTunnel/ipSecTunnel) which on cold-start are
        // all null, so its disconnect calls become no-ops and the zombie
        // OpenVPNService keeps running parallel to whatever new connect we're
        // about to attempt. Server-side this manifests as the SAME client
        // appearing connected via TWO protocols simultaneously even though
        // the user only triggered one — observed 2026-05-07 with Peter-
        // Android-Shielded showing as connected via OpenVPN AND IPSec.
        //
        // Force-stop any leftover protocol-specific service unconditionally.
        // stopService is a no-op if the service isn't running, so this is
        // safe on a true fresh start. We only have a separate-service problem
        // for OpenVPN today; charon/IPSec runs in our own process via
        // libcharon.so (no separate service to clean up), and WireGuard's
        // GoBackend lives in our process too. If we ever add separate
        // services for those, mirror the call here.
        try {
            stopService(Intent(this, OpenVPNService::class.java))
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "Cold-start zombie cleanup: stopService(OpenVPNService) failed: ${e.message}")
        }

        // If the service is being created (or recreated via
        // START_STICKY) while the process-global KillSwitchManager
        // is already in SINKHOLE state, establish the block-all
        // fd synchronously RIGHT NOW - before onStartCommand gets
        // to run its null-intent handleAlwaysOnReconnect path.
        // Without this, there's a race window where the plugin
        // reconnect starts before the state.collect coroutine
        // below (which dispatches async on Main) has had a chance
        // to fire enterSinkholeMode, so for several hundred ms
        // traffic could flow without the sinkhole fd in place.
        if (com.privycs.vpn.util.KillSwitchManager.isSinkholeActive()) {
            PrivycsLogger.i(TAG, "onCreate: state=SINKHOLE at service start → establishing sinkhole synchronously")
            enterSinkholeMode()
        }

        // Observe Kill Switch state transitions. We track the previous
        // state so we can distinguish SINKHOLE->ARMED (a legitimate
        // reconnect - the plugin's fresh establish() call already
        // replaced our sinkhole fd, nothing to repair) from
        // SINKHOLE->IDLE (user disarmed while sinkhole was active -
        // the sinkhole fd was the only live tun and closing it leaves
        // no tun at all; the plugin may still claim "connected" but
        // the underlying fd is gone and traffic leaks direct. This is
        // the zombie state reported as "tunnel shows connected after
        // airplane-mode-recovery + KS-disable but whatsmyip reveals
        // the local ISP IP"). On that transition we force a clean
        // plugin teardown so the UI stops lying and the user can tap
        // Connect for a fresh tunnel.
        scope.launch {
            var previousKillSwitchState = com.privycs.vpn.util.KillSwitchManager.state.value
            com.privycs.vpn.util.KillSwitchManager.state.collect { state ->
                val previous = previousKillSwitchState
                previousKillSwitchState = state
                // B-4 hybrid persistence: persist whether the Kill
                // Switch is actively protecting so a START_STICKY
                // respawn after an OEM kill can re-engage it.
                killSwitchPrefs.edit()
                    .putBoolean(
                        "was_protecting",
                        state != com.privycs.vpn.util.KillSwitchManager.State.IDLE,
                    )
                    .apply()
                when (state) {
                    com.privycs.vpn.util.KillSwitchManager.State.SINKHOLE -> enterSinkholeMode()
                    else -> {
                        exitSinkholeMode()
                        if (previous == com.privycs.vpn.util.KillSwitchManager.State.SINKHOLE) {
                            when (state) {
                                com.privycs.vpn.util.KillSwitchManager.State.IDLE -> {
                                    PrivycsLogger.i(TAG, "KS state SINKHOLE->IDLE: tearing down stale plugin (zombie tunnel recovery)")
                                    scope.launch { forceTeardownAfterSinkhole() }
                                }
                                com.privycs.vpn.util.KillSwitchManager.State.ARMED -> {
                                    // Reconnect succeeded out of sinkhole.
                                    // The notification was last set with
                                    // sinkholeMode=true (or via
                                    // updateNotification while
                                    // isSinkholeActive() returned true,
                                    // which forced the danger styling).
                                    // Force a refresh now that state is
                                    // ARMED so the user sees the
                                    // connected status, not stale "Kill
                                    // Switch active" text.
                                    val name = currentConnectionName.takeIf { it.isNotBlank() } ?: "VPN"
                                    PrivycsLogger.i(TAG, "KS state SINKHOLE->ARMED: refreshing notification to connected")
                                    updateNotification(getString(R.string.vpn_notification_connected, name))
                                }
                                else -> { /* unreachable - SINKHOLE handled above */ }
                            }
                        }
                    }
                }
            }
        }

        registerKillSwitchNetworkWatcher()

        // Polling fallback: some OEM builds (Samsung One UI, MIUI,
        // OxygenOS) have observed onLost quirks when a VPN is the
        // active default - the callback either fires late or not at
        // all. Rather than trust a single signal, poll every 3s
        // while armed: if the system has no non-VPN network at all,
        // that's an unambiguous drop regardless of whether any
        // callback fired. Cheap check (no wake-locks, no network
        // traffic), runs only during the armed window.
        scope.launch {
            while (isActive) {
                delay(3000)
                val state = com.privycs.vpn.util.KillSwitchManager.state.value
                when (state) {
                    com.privycs.vpn.util.KillSwitchManager.State.ARMED -> {
                        val cm = getSystemService(CONNECTIVITY_SERVICE) as android.net.ConnectivityManager
                        if (!hasAnyNonVpnNetwork(cm)) {
                            PrivycsLogger.i(TAG, "Kill switch poll: no non-VPN network while armed → engageSinkhole")
                            com.privycs.vpn.util.KillSwitchManager.engageSinkhole(
                                "poll: no non-VPN network",
                            )
                        }
                    }
                    com.privycs.vpn.util.KillSwitchManager.State.SINKHOLE -> {
                        // Watchdog: state flow says we must be
                        // blocking, but the fd is gone. That can
                        // happen if the VpnService was recreated
                        // by START_STICKY (fresh instance, fd ref
                        // lost), or if the fd got closed by a
                        // plugin teardown path we don't control.
                        // Re-establishing closes the leak window
                        // within one poll cycle (≤3s) even when
                        // the primary state.collect observer
                        // didn't fire - e.g. because the state
                        // value was already SINKHOLE when this
                        // service instance started up, so there's
                        // no "transition" event for the collector.
                        if (sinkholeTunFd == null) {
                            PrivycsLogger.w(TAG, "Kill switch poll: state=SINKHOLE but fd=null → re-establishing")
                            enterSinkholeMode()
                        }
                    }
                    else -> { /* IDLE: nothing to do */ }
                }
            }
        }
    }

    /**
     * Register a ConnectivityManager callback that fires the
     * moment the underlying non-VPN network disappears - not
     * 30-90s later when the tunnel plugin's own keepalive
     * finally times out.
     *
     * Why NOT registerDefaultNetworkCallback: when our VPN is
     * active it BECOMES the default network. Airplane mode takes
     * down WiFi/Mobile underneath, but the VPN tun fd is still
     * open and stays the default from Android's perspective, so
     * onLost never fires. The v0.9.9.1 implementation had this
     * bug.
     *
     * Right mechanism: registerNetworkCallback with a filter
     * that requires NET_CAPABILITY_NOT_VPN. This observes every
     * non-VPN network directly and ignores our own tunnel. When
     * airplane mode drops WiFi + Mobile, both match the filter
     * and fire onLost independently of what the VPN is doing.
     */
    private fun registerKillSwitchNetworkWatcher() {
        val cm = getSystemService(CONNECTIVITY_SERVICE) as android.net.ConnectivityManager
        val callback = object : android.net.ConnectivityManager.NetworkCallback() {
            override fun onLost(network: android.net.Network) {
                super.onLost(network)
                PrivycsLogger.d(TAG, "Kill switch onLost fired for network=$network, armed=${com.privycs.vpn.util.KillSwitchManager.isArmed()}")
                scope.launch {
                    // Grace period for WiFi-to-mobile handoffs:
                    // during a handoff both the losing and gaining
                    // network fire events in quick succession. We
                    // only want to engage the sinkhole if NO
                    // non-VPN network remains after the dust
                    // settles.
                    delay(1500)
                    if (!com.privycs.vpn.util.KillSwitchManager.isArmed()) {
                        PrivycsLogger.d(TAG, "Kill switch onLost post-delay: not armed, skipping")
                        return@launch
                    }
                    if (hasAnyNonVpnNetwork(cm)) {
                        PrivycsLogger.d(TAG, "Kill switch onLost post-delay: another non-VPN network present, skipping")
                        return@launch
                    }
                    PrivycsLogger.i(TAG, "Network watcher: all non-VPN networks lost while armed → engageSinkhole")
                    com.privycs.vpn.util.KillSwitchManager.engageSinkhole(
                        "all non-VPN networks lost",
                    )
                }
            }

            override fun onAvailable(network: android.net.Network) {
                super.onAvailable(network)
                PrivycsLogger.d(TAG, "Kill switch onAvailable fired for network=$network")
                // v0.9.10.2: DO NOT refresh the sinkhole fd on
                // onAvailable. Closing the tun fd causes Android to
                // destroy the VpnService; START_STICKY then recreates
                // it, which re-registers this network callback, which
                // fires onAvailable again for the already-available
                // WiFi, which would trigger refreshSinkhole again -
                // infinite service-respawn loop (200+ cycles in 8s
                // observed on v0.9.10.1). The VpnService framework
                // guarantees VPN-route precedence as long as the tun
                // fd is alive, so no refresh is needed when new non-
                // VPN networks appear. The sinkhole fd keeps
                // blackholing traffic correctly regardless of
                // underlying network changes.
            }
        }
        killSwitchNetworkCallback = callback
        try {
            val request = android.net.NetworkRequest.Builder()
                .addCapability(android.net.NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .addCapability(android.net.NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                .build()
            cm.registerNetworkCallback(request, callback)
            PrivycsLogger.d(TAG, "Kill switch network watcher registered (non-VPN filter)")
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "Kill switch network watcher registration failed", e)
            killSwitchNetworkCallback = null
        }
    }

    @Suppress("DEPRECATION") // allNetworks: one-shot probe, NetworkCallback would need more state than it's worth
    private fun hasAnyNonVpnNetwork(cm: android.net.ConnectivityManager): Boolean {
        return cm.allNetworks.any { net ->
            val caps = cm.getNetworkCapabilities(net) ?: return@any false
            !caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_VPN) &&
                caps.hasCapability(android.net.NetworkCapabilities.NET_CAPABILITY_INTERNET)
        }
    }

    /**
     * Stand up a VpnService.Builder tun fd that captures ALL traffic
     * and never reads/writes it, effectively dropping every packet.
     * addDisallowedApplication(ourPackage) is critical - without it,
     * we would block ourselves from making the "Retry Connect"
     * outgoing request.
     */
    private fun enterSinkholeMode() {
        if (sinkholeTunFd != null) return  // already active
        try {
            PrivycsLogger.i(TAG, "enterSinkholeMode: establishing block-all tunnel")
            val builder = Builder()
                .setSession("Privycs VPN (Kill Switch)")
                .addAddress("10.255.255.2", 32)
                .addRoute("0.0.0.0", 0)
                .addRoute("::", 0)
                .addDisallowedApplication(packageName)
            sinkholeTunFd = builder.establish()
            if (sinkholeTunFd == null) {
                // B-4: establish() failed — the sinkhole intent is
                // active but no block-all fd exists, so traffic is
                // NOT blocked. Surface the truth instead of the
                // reassuring template; the 3s watchdog retries.
                sinkholeEstablishFailed = true
                PrivycsLogger.w(
                    TAG,
                    "enterSinkholeMode: establish() returned null — Kill Switch is NOT " +
                        "blocking traffic (VPN permission revoked?); watchdog will retry",
                )
                updateNotification(getString(R.string.vpn_notification_kill_switch_failed))
            } else {
                sinkholeEstablishFailed = false
                updateNotification(
                    getString(R.string.vpn_notification_kill_switch_active),
                    sinkholeMode = true,
                )
                sendWidgetUpdate(connected = false)
                // Proactively push a disconnected status so the UI
                // reflects sinkhole state IMMEDIATELY rather than
                // waiting up to 2s for the next status-polling tick.
                // updateStatus will be masked (sinkhole active) into
                // a proper disconnected VpnStatus internally.
                VpnServiceManager.getInstance(this).updateStatus(
                    com.privycs.vpn.data.models.VpnStatus(),
                )
            }
        } catch (e: Exception) {
            PrivycsLogger.e(TAG, "enterSinkholeMode failed", e)
        }
    }

    private fun exitSinkholeMode() {
        sinkholeEstablishFailed = false
        val fd = sinkholeTunFd ?: return
        try {
            PrivycsLogger.i(TAG, "exitSinkholeMode: closing block-all tunnel")
            fd.close()
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "exitSinkholeMode: fd close failed", e)
        } finally {
            sinkholeTunFd = null
        }
    }

    /**
     * Clean up stale plugin state after a SINKHOLE -> IDLE transition.
     *
     * Scenario: user toggles Kill Switch off while the sinkhole is
     * active. Our exitSinkholeMode() closes the block-all fd, but the
     * underlying tunnel plugin (WireGuard / OpenVPN / strongSwan)
     * still has its pre-sinkhole "UP" state cached internally - and
     * its tun fd was already replaced by our sinkhole when we engaged
     * it, so closing the sinkhole leaves NO tun fd at all.
     *
     * Symptom: UI shows "connected", widget shows connected, but
     * whatsmyip.com reveals the ISP IP because there's no tun to
     * capture traffic. Only a manual disconnect -> reconnect repairs
     * the state machine.
     *
     * Fix: force a clean plugin-side teardown so the status flow
     * reflects reality ("Disconnected"). The user can then tap
     * Connect to get a fresh tunnel. We deliberately do NOT
     * auto-reconnect here - the user just explicitly disabled the
     * Kill Switch, and surprising them with an automatic reconnect
     * would be poor UX. Clear state > clever state.
     */
    private suspend fun forceTeardownAfterSinkhole() { tunnelMutex.withLock {
        // v0.9.15.46: single AmneziaWG-backed tunnel covers both WG +
        // AWG protocols (see `amneziaTunnel` doc). Sinkhole-teardown
        // semantics unchanged — both protocols use the same
        // VpnService.Builder TUN slot that the sinkhole wants to occupy.
        try { amneziaTunnel?.disconnect() } catch (e: Exception) { PrivycsLogger.w(TAG, "Post-sinkhole WG/AWG teardown: ${e.message}") }
        amneziaTunnel = null
        try { openVpnTunnel?.disconnect() } catch (e: Exception) { PrivycsLogger.w(TAG, "Post-sinkhole OpenVPN teardown: ${e.message}") }
        openVpnTunnel = null
        try { ipSecTunnel?.disconnect() } catch (e: Exception) { PrivycsLogger.w(TAG, "Post-sinkhole IPSec teardown: ${e.message}") }
        ipSecTunnel = null

        val manager = VpnServiceManager.getInstance(this)
        manager.updateStatus(com.privycs.vpn.data.models.VpnStatus())
        com.privycs.vpn.util.ConnectCoordinator.markDisconnected()
        connectStartTime = 0L
        sendWidgetUpdate(connected = false)
        updateNotification(getString(R.string.vpn_notification_disconnected))
        PrivycsLogger.i(TAG, "Post-sinkhole teardown complete - plugin state cleared, UI reflects disconnected")

        // Post-teardown: if Connect-on-Demand is enabled and its
        // rules currently match, automatically reconnect. The user
        // explicitly disabled the Kill Switch but their COD intent
        // is still "keep me connected when the network matches".
        // Using USER source bypasses pause-timers and the
        // coordinator's strict rule for non-USER disconnect.
        try {
            val settings = PrivycsApp.instance.settingsRepository.getSettingsBlocking()
            if (!PrivycsApp.instance.networkRulesRepository.hasRules) {
                PrivycsLogger.d(TAG, "Post-sinkhole: no network rules, not auto-reconnecting")
                return
            }
            // Brief settle delay so the plugin teardown above has
            // time to finish releasing native-side state before the
            // reconnect tries to establish a new tun fd.
            delay(500)
            val nm = com.privycs.vpn.service.NetworkMonitor.getInstance(this)
            nm.reevaluate()
            val ns = nm.networkState.value
            if (!ns.shouldConnect) {
                PrivycsLogger.d(TAG, "Post-sinkhole: COD rules do not match current network, not auto-reconnecting (${ns.ruleMatch})")
                return
            }
            // Pool-active wins. If the user's selection is a Pool
            // we reconnect via the pool path - same dead-end fix
            // as in NetworkMonitor / VpnPauseTimer: getActive()
            // returns null for pool users, so without this branch
            // post-sinkhole COD resume would silently no-op.
            val poolReg = PrivycsApp.instance.poolRepository.registry.value
            val activePoolId = poolReg.activeId
            val activePool = poolReg.pools.firstOrNull { it.id == activePoolId }
            if (activePoolId.isNotEmpty() && activePool != null) {
                PrivycsLogger.i(TAG, "Post-sinkhole: COD rules match, auto-reconnecting to pool ${activePool.name}")
                com.privycs.vpn.util.ConnectCoordinator.requestPoolConnect(
                    this,
                    com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                    activePoolId,
                    activePool.name,
                )
                return
            }
            val active = PrivycsApp.instance.connectionRepository.getActive() ?: return
            PrivycsLogger.i(TAG, "Post-sinkhole: COD rules match, auto-reconnecting to ${active.name}")
            com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                this,
                com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                active,
            )
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "Post-sinkhole COD auto-reconnect failed: ${e.message}")
        }
    } } // tunnelMutex.withLock + function

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_CONNECT -> {
                val connectionId = intent.getStringExtra(EXTRA_CONNECTION_ID) ?: ""
                val protocolStr = intent.getStringExtra(EXTRA_PROTOCOL) ?: ""
                val configContent = intent.getStringExtra(EXTRA_CONFIG_CONTENT) ?: ""
                val connectionName = intent.getStringExtra(EXTRA_CONNECTION_NAME) ?: ""

                startForeground(
                    PrivycsApp.NOTIFICATION_ID_VPN,
                    buildNotification(getString(R.string.vpn_notification_connecting_to, connectionName))
                )

                handleConnect(connectionId, protocolStr, configContent, connectionName)
            }

            ACTION_DISCONNECT -> {
                handleDisconnect()
            }

            ACTION_START_MONITOR -> {
                // Foreground-monitor mode (v0.9.14.75 opt-in). Keep
                // the service alive in the background so NetworkMonitor's
                // tick + NetworkCallback survive Doze. No tunnel is
                // started; the notification is min-priority so the
                // user-drawer listing is unobtrusive. Idempotent —
                // if a tunnel already brought us to foreground, this
                // call replaces the connecting/connected notification
                // with the monitor one only when the service is
                // otherwise idle (currentProtocol == null).
                if (currentProtocol == null) {
                    PrivycsLogger.i(TAG, "ACTION_START_MONITOR — entering foreground-monitor mode")
                    startForeground(
                        PrivycsApp.NOTIFICATION_ID_VPN,
                        buildNotification(
                            getString(R.string.vpn_notification_monitoring),
                            monitorMode = true,
                        ),
                    )
                    // Bootstrap NetworkMonitor too — PrivycsApp also
                    // does this on its own COD-enabled branch, but
                    // we re-run idempotently in case start-monitor
                    // landed via a different code path (e.g. Settings
                    // toggle flip while app was in background).
                    com.privycs.vpn.service.NetworkMonitor.getInstance(applicationContext).start()
                }
            }

            ACTION_STOP_MONITOR -> {
                PrivycsLogger.i(TAG, "ACTION_STOP_MONITOR — leaving foreground-monitor mode")
                // Persist the toggle off so the next app start doesn't
                // re-arm monitor. If a tunnel is currently running,
                // we leave the service alive — only the user-visible
                // notification action gets disarmed; the running
                // tunnel's notification stays.
                scope.launch {
                    PrivycsApp.instance.settingsRepository.setKeepMonitorAlive(false)
                    if (currentProtocol == null) {
                        stopForeground(STOP_FOREGROUND_REMOVE)
                        stopSelf()
                    }
                }
            }

            ACTION_KILL_SWITCH_RETRY -> {
                // Notification "Retry Connect" tap. Fire a fresh
                // USER-source connect at the active target (pool or
                // single connection) so the coordinator's gate
                // accepts it and the sinkhole is replaced by a real
                // tunnel on success.
                scope.launch {
                    val poolReg = PrivycsApp.instance.poolRepository.registry.value
                    val activePoolId = poolReg.activeId
                    val activePool = poolReg.pools.firstOrNull { it.id == activePoolId }
                    if (activePoolId.isNotEmpty() && activePool != null) {
                        com.privycs.vpn.util.ConnectCoordinator.requestPoolConnect(
                            this@PrivycsVpnService,
                            com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                            activePoolId,
                            activePool.name,
                        )
                        return@launch
                    }
                    val active = PrivycsApp.instance.connectionRepository.getActive()
                    if (active == null) {
                        PrivycsLogger.w(TAG, "Kill Switch retry: no active connection or pool to reconnect to")
                        return@launch
                    }
                    com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                        this@PrivycsVpnService,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                        active,
                    )
                }
            }

            ACTION_ENGAGE_SINKHOLE -> {
                // SettingsRepository starts us via this action when
                // the user enables Kill Switch while no service is
                // running and there's a configured connection. We
                // need a live VpnService instance to actually hold
                // the block-all tun fd — otherwise KillSwitchManager
                // _state says SINKHOLE but no fd exists and traffic
                // flows direct (the "UI lies, internet still works"
                // bug observed in v0.9.10.2).
                startForeground(
                    PrivycsApp.NOTIFICATION_ID_VPN,
                    buildNotification(getString(R.string.vpn_notification_kill_switch_active), sinkholeMode = true),
                )
                // onCreate already called enterSinkholeMode if state
                // was SINKHOLE at startup; this is idempotent
                // (early-returns when sinkholeTunFd != null). Belt
                // and braces in case the state flow racing with
                // onCreate left us in a weird intermediate state.
                if (com.privycs.vpn.util.KillSwitchManager.isSinkholeActive()) {
                    enterSinkholeMode()
                } else {
                    PrivycsLogger.w(TAG, "ACTION_ENGAGE_SINKHOLE but state=${com.privycs.vpn.util.KillSwitchManager.state.value}; stopping")
                    stopForeground(STOP_FOREGROUND_REMOVE)
                    stopSelf()
                }
            }

            ACTION_POOL_CONNECT -> {
                // User-triggered pool activation. Picks a member via
                // policy and brings up the tunnel. Re-arms the
                // rotation scheduler for round-robin pools.
                val poolId = intent.getStringExtra(EXTRA_POOL_ID) ?: ""
                if (poolId.isEmpty()) {
                    PrivycsLogger.w(TAG, "ACTION_POOL_CONNECT without pool id")
                    return START_NOT_STICKY
                }
                startForeground(
                    PrivycsApp.NOTIFICATION_ID_VPN,
                    buildNotification(getString(R.string.vpn_notification_connecting_pool))
                )
                handlePoolConnect(poolId)
            }

            ACTION_POOL_PRE_WARM -> {
                val poolId = intent.getStringExtra(EXTRA_POOL_ID) ?: ""
                if (poolId.isEmpty()) {
                    PrivycsLogger.w(TAG, "ACTION_POOL_PRE_WARM without pool id")
                    return START_NOT_STICKY
                }
                // Sequence-guard: drop intents from older arm()
                // calls that got queued before a fresher arm()
                // bumped the sequence. Without this guard, a
                // user-triggered manual rotation racing a queued
                // scheduled rotation would run pickAndConnect
                // twice in quick succession on the same pool,
                // tearing down the just-set-up tunnel.
                if (!isFreshAlarmSeq(intent)) {
                    PrivycsLogger.d(TAG, "dropping stale PRE_WARM (seq mismatch)")
                    return START_NOT_STICKY
                }
                handlePoolPreWarm(poolId)
            }

            ACTION_POOL_ROTATE -> {
                val poolId = intent.getStringExtra(EXTRA_POOL_ID) ?: ""
                if (poolId.isEmpty()) {
                    PrivycsLogger.w(TAG, "ACTION_POOL_ROTATE without pool id")
                    return START_NOT_STICKY
                }
                if (!isFreshAlarmSeq(intent)) {
                    PrivycsLogger.d(TAG, "dropping stale ROTATE (seq mismatch)")
                    return START_NOT_STICKY
                }
                handlePoolRotate(poolId)
            }

            else -> {
                startForeground(
                    PrivycsApp.NOTIFICATION_ID_VPN,
                    buildNotification(getString(R.string.vpn_notification_reconnecting))
                )
                // B-4 hybrid persistence: intent == null means the OS
                // recreated this service via START_STICKY after our
                // process was killed (typically an aggressive OEM
                // battery killer) — NOT a cold device boot, which
                // starts us via an explicit intent or not at all. If
                // the Kill Switch was protecting when we were killed,
                // re-engage the sinkhole now so traffic stays blocked
                // until the reconnect below succeeds.
                if (intent == null &&
                    killSwitchPrefs.getBoolean("was_protecting", false)
                ) {
                    PrivycsLogger.i(
                        TAG,
                        "START_STICKY respawn + Kill Switch was protecting → re-engaging sinkhole",
                    )
                    com.privycs.vpn.util.KillSwitchManager.forceSinkhole(
                        "process respawn after kill — Kill Switch was protecting",
                    )
                    enterSinkholeMode()
                }
                // Always-on VPN restart: reconnect with last active connection
                handleAlwaysOnReconnect()
            }
        }

        return START_STICKY
    }

    // ========================================================================
    // POOL FEATURE — handlers for ACTION_POOL_*. Delegates to PoolConnector
    // which owns the picker/probe/health-check logic.
    // ========================================================================

    private val poolConnector: PoolConnector by lazy {
        PoolConnector(
            context = this,
            pools = PrivycsApp.instance.poolRepository,
            tunnelOps = poolTunnelOps
        )
    }

    private val poolScheduler: PoolRotationScheduler by lazy {
        PoolRotationScheduler(this)
    }

    private val poolTunnelOps = object : PoolConnector.PoolTunnelOps {
        override suspend fun bringUp(
            member: com.privycs.vpn.data.models.PoolMember,
            splitTunnel: com.privycs.vpn.data.models.PoolSplitTunnel?
        ): Boolean {
            // handleConnect is fire-and-forget — it launches its own
            // scope.launch { ... } that internally awaits
            // teardownAllProtocols() (~1500ms) and only THEN calls
            // connectWireGuard/connectOpenVpn/connectIpSec which set
            // the protocol-specific tunnel field. The previous fixed
            // 300ms delay before returning beat the tunnel reliably,
            // so Layer-B's bytesReceived() poll always saw a null
            // tunnel, returned 0L, timed out after 5s, and marked the
            // member unreachable — making pool connect fail
            // systematically.
            //
            // Fix: kick off handleConnect, then POLL for the
            // protocol-specific tunnel reference to appear. Returns
            // true as soon as the tunnel object is in place (which
            // means establish() has been called and Layer-B can
            // meaningfully query bytesReceived()). Returns false if
            // the tunnel never appeared within the budget — usually
            // a sign that handleConnect bailed (KS sinkhole, no
            // network, parse failure, etc).
            // Split-tunnel injection: when the pool has a split-
            // tunnel config (bypass CIDRs and/or "exclude private
            // networks" toggle), patch the member's config text
            // BEFORE handing it to handleConnect so the WG tunnel
            // / OpenVPN profile sees the modified AllowedIPs /
            // route directives. Disabled / inapplicable configs
            // pass through unchanged with a log message.
            var effectiveConfig = if (splitTunnel != null && splitTunnel.isActive()) {
                val result = com.privycs.vpn.data.SplitTunnelInjector.inject(
                    configContent = member.config.configContent,
                    protocol = member.config.protocol,
                    splitTunnel = splitTunnel
                )
                if (!result.applied && result.skippedReason != null) {
                    PrivycsLogger.i(TAG, "pool ${member.name}: split-tunnel skipped (${result.skippedReason})")
                }
                result.patched
            } else {
                member.config.configContent
            }
            // v0.9.14.96: chain in IPv6 leak killswitch injection. Runs
            // AFTER the split-tunnel patch so the split-tunnel's
            // AllowedIPs rewrite (which can collapse the v6 catch-all)
            // is followed by our re-add of ::/0. Always-on (no
            // setting); IpV6KillswitchInjector is idempotent so
            // already-v6-covered configs pass through unchanged.
            run {
                val v6 = com.privycs.vpn.data.IpV6KillswitchInjector.inject(
                    effectiveConfig, member.config.protocol
                )
                if (v6.applied) {
                    PrivycsLogger.i(TAG, "pool ${member.name}: ipv6-killswitch injected ::/0 sink")
                    effectiveConfig = v6.patched
                } else if (v6.skippedReason != null) {
                    PrivycsLogger.d(TAG, "pool ${member.name}: ipv6-killswitch skipped (${v6.skippedReason})")
                }
            }

            return try {
                handleConnect(
                    connectionId = "pool:${member.id}",
                    protocolStr = member.config.protocol.name.lowercase(),
                    configContent = effectiveConfig,
                    connectionName = member.name
                )
                val budgetMs = 8_000L
                val pollIntervalMs = 200L
                val deadline = System.currentTimeMillis() + budgetMs
                while (System.currentTimeMillis() < deadline) {
                    val tunnelReady = when (member.config.protocol) {
                        // v0.9.15.46: WG + AWG both backed by amneziaTunnel
                        VpnProtocol.WIREGUARD -> amneziaTunnel != null
                        VpnProtocol.AMNEZIAWG -> amneziaTunnel != null
                        VpnProtocol.OPENVPN -> openVpnTunnel != null
                        VpnProtocol.IPSEC -> ipSecTunnel != null
                    }
                    if (tunnelReady) {
                        PrivycsLogger.d(TAG, "pool bringUp: ${member.config.protocol} tunnel ready " +
                                "after ${System.currentTimeMillis() - (deadline - budgetMs)}ms")
                        return true
                    }
                    kotlinx.coroutines.delay(pollIntervalMs)
                }
                PrivycsLogger.w(TAG, "pool bringUp: ${member.config.protocol} tunnel did not appear " +
                        "within ${budgetMs}ms - handleConnect likely bailed")
                false
            } catch (e: kotlinx.coroutines.CancellationException) {
                // Re-throw cancellation so the picker's retry loop
                // also dies. Without this re-throw, the service
                // could be in mid-destruction and the caught
                // CancellationException made the outer for-loop
                // try the next member - which then saw the previous
                // member's still-orphaned wireGuardTunnel reference
                // and falsely returned "tunnel ready after 0ms"
                // (the v0.9.11.47 death-loop observed in 20:42-20:44
                // logs: cl-scl → co-bog → us-dal → za-jnb → au-syd
                // → jp-osa, all "tunnel ready after 0ms", no actual
                // tunnel matched the picked member).
                throw e
            } catch (e: Exception) {
                PrivycsLogger.e(TAG, "pool bringUp failed: ${e.message}", e)
                false
            }
        }

        override suspend fun bringDown(): Boolean = try {
            // Direct teardown WITHOUT stopSelf. Earlier version
            // called handleDisconnect() which at line 1250-1251
            // unconditionally calls stopForeground +stopSelf. The
            // service died ~150ms after every pool connect attempt,
            // cancelling pickAndConnect's coroutine mid-run, and
            // the WireGuard tunnel that handleConnect had just
            // brought up was orphaned in the GoBackend JNI process
            // (kept logging "Failed to write packet to TUN device:
            // input/output error" until the next pool tap created
            // a fresh service that ran into the same death spiral).
            //
            // Mirrors desktop's PickAndConnectActivePool which does
            // a wasConnected-gated disconnectInternal() that does
            // NOT terminate the surrounding goroutine.
            tunnelMutex.withLock { teardownAllProtocols() }
            true
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (e: Exception) {
            PrivycsLogger.e(TAG, "pool bringDown failed: ${e.message}", e)
            false
        }

        override suspend fun bytesReceived(): Long = withContext(Dispatchers.IO) {
            try {
                // v0.9.15.46: WG + AWG both flow through amneziaTunnel.
                amneziaTunnel?.bytesReceived() ?: 0L
            } catch (e: Exception) {
                0L
            }
        }

        override suspend fun userCountry(): String {
            // CACHED-ONLY in the connect critical path. Mirrors
            // desktop's app_pool.go:899-902 which reads only
            // selfIPDetector.Cached(). A cold cache here would force
            // a 9s synchronous probe inline, freezing the picker
            // and pushing wrong-country picks (S2/S3) when the probe
            // times out. App startup + ActivatePool background
            // goroutines warm the cache - if neither has run, we
            // accept "" and let the picker degrade to home-region-
            // unbiased pick (with RestrictRegions still applying).
            //
            // The cache is invalidated on network roam, refreshed
            // by the next non-critical-path call (UI fields, About
            // screen). The 30-min Geo-Nearest "wrong country" window
            // observed in v0.9.11.46 came from this method calling
            // the live probe synchronously and timing out.
            return PrivycsApp.instance.selfIpDetector.cachedResult()?.country.orEmpty()
        }
    }

    /**
     * Returns true if the alarm-seq carried by `intent` is the
     * latest armed sequence (i.e. this intent is the freshest
     * fire of its kind, not a stale leftover that got queued
     * before a more recent arm() bumped the sequence).
     *
     * Intents missing the EXTRA_ARM_SEQ extra (older app version
     * still sending alarms after upgrade) are accepted — this is
     * the conservative fallback so we don't drop legitimate
     * intents during the upgrade window.
     */
    private fun isFreshAlarmSeq(intent: Intent): Boolean {
        if (!intent.hasExtra(PoolRotationScheduler.EXTRA_ARM_SEQ)) return true
        val seq = intent.getLongExtra(PoolRotationScheduler.EXTRA_ARM_SEQ, 0L)
        val latest = PoolRotationScheduler.latestArmSequence.get()
        return seq >= latest
    }

    private fun handlePoolConnect(poolId: String) {
        scope.launch {
            val raw = PrivycsApp.instance.poolRepository.get(poolId) ?: run {
                // Pool was deleted between the user tapping Connect
                // and this handler running (rare but possible: tap
                // pool, race with delete, or alarm fires for an
                // already-deleted pool). Surface as VpnStatus.error
                // so the UI exits the "Connecting..." state instead
                // of hanging there forever.
                PrivycsLogger.e(TAG, "pool $poolId not found - deleted while connect was queued")
                broadcastPoolError("Pool no longer exists")
                return@launch
            }

            // Empty pool guard. An import that produced zero valid
            // members (all .conf files unparseable, ZIP corrupted)
            // creates a Pool with members=[]. The picker returns
            // null and PoolConnector logs "no candidate after 0
            // attempts" but the user only sees a stuck UI. Catch
            // here and surface a clearer message.
            if (raw.members.isEmpty()) {
                PrivycsLogger.e(TAG, "pool ${raw.name}: empty - no members imported")
                broadcastPoolError("Pool ${raw.name} has no members")
                return@launch
            }

            // Just-in-time auto-restrict: if the pool has no
            // RestrictRegions and we know the user's country (cache
            // populated by the app-boot warm or PoolDetailHost's
            // pre-activate warm), apply the home region in memory
            // BEFORE the picker runs. Mirrors desktop's
            // app_pool.go:909-915. Without this, Round-Robin pinballs
            // across all continents (cl-scl → co-bog → us-dal → ...
            // observed in v0.9.11.47 logs).
            val pool = applyHomeRegionRestrictIfMissing(raw)

            val picked = poolConnector.pickAndConnect(pool)
            if (picked == null) {
                // All attempts failed. Direct tunnel teardown only -
                // do NOT call handleDisconnect because that ends
                // with stopSelf and would have killed us mid-flow.
                // Distinguish "all members marked unreachable" (TTL
                // and recent-failure exclusion path exhausted) from
                // "members eligible but actual connect failed".
                // Precompute unreachable set upfront because
                // isMemberUnreachable is suspend (mutex-guarded).
                var unreachableCount = 0
                for (m in pool.members) {
                    if (PrivycsApp.instance.poolRepository.state
                            .isMemberUnreachable(pool.id, m.id)
                    ) unreachableCount++
                }
                val msg = if (unreachableCount == pool.members.size) {
                    "Pool ${pool.name}: all members marked unreachable - tap Reset on the pool detail to retry"
                } else {
                    "Pool ${pool.name}: no member could connect (${pool.members.size - unreachableCount} eligible, all failed)"
                }
                PrivycsLogger.e(TAG, msg)
                tunnelMutex.withLock { teardownAllProtocols() }
                broadcastPoolError(msg)
                return@launch
            }
            onPoolMemberConnected(pool, picked, isRotation = false)
        }
    }

    private fun handlePoolPreWarm(poolId: String) {
        scope.launch {
            val pool = PrivycsApp.instance.poolRepository.get(poolId) ?: run {
                PrivycsLogger.d(TAG, "pre-warm: pool $poolId no longer exists - skipping")
                return@launch
            }
            poolConnector.preWarm(pool)
            // After pre-warm picks pendingMember (or fails), broadcast
            // the updated status so the UI shows "Next: <name>" 60s
            // ahead of rotation. Without this, the user sees no
            // pre-warm signal and assumes nothing is happening (S6).
            broadcastPoolStatus(pool)
        }
    }

    private fun handlePoolRotate(poolId: String) {
        scope.launch {
            val raw = PrivycsApp.instance.poolRepository.get(poolId) ?: run {
                // Pool deleted between alarm fire and rotation start.
                // Cancel any further alarms (defensive - the delete
                // path should have done this already) and surface
                // the error so the UI doesn't show a stale
                // countdown.
                PrivycsLogger.e(TAG, "rotate: pool $poolId deleted - cancelling alarms")
                try {
                    poolScheduler.cancelAll()
                } catch (e: Exception) { /* ignore */ }
                broadcastPoolError("Pool no longer exists")
                return@launch
            }
            val pool = applyHomeRegionRestrictIfMissing(raw)
            val picked = poolConnector.pickAndConnect(pool)
            if (picked != null) {
                onPoolMemberConnected(pool, picked, isRotation = true)
            } else {
                // All attempts failed mid-rotation. Tear down the
                // tunnel cleanly but do NOT stopSelf - the next
                // rotation tick (or a manual retry) needs the
                // service alive to act on it.
                PrivycsLogger.e(TAG, "pool ${pool.name}: rotation - all attempts failed")
                tunnelMutex.withLock { teardownAllProtocols() }
                broadcastPoolError("Pool ${pool.name}: rotation failed - all members in scope unreachable")
            }
        }
    }

    /**
     * Surface a pool-related error via VpnStatus. Used by all the
     * "pool-deleted" / "all-unreachable" / "rotation-failed" paths
     * so the UI can show a single error message and exit the
     * "Connecting..." state. Without this the service path returned
     * silently and the user saw the spinner spin forever.
     */
    private fun broadcastPoolError(msg: String) {
        try {
            val mgr = com.privycs.vpn.service.VpnServiceManager.getInstance(this@PrivycsVpnService)
            mgr.updateStatus(com.privycs.vpn.data.models.VpnStatus(error = msg))
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "broadcastPoolError dispatch failed: ${e.message}")
        }
    }

    /**
     * Just-in-time home-region auto-restrict. Returns the pool with
     * RestrictRegions populated when:
     *   - the input pool has no restriction set yet
     *   - the SelfIp cache contains a known country code
     *
     * Otherwise returns the input unchanged. The persisted-update
     * side-effect runs only when the auto-restrict actually applied.
     *
     * Mirrors desktop's app_pool.go:909-915. Without this,
     * Round-Robin pools without an explicit user-set region hammer
     * through ALL continents in alphabetical order (the cl-scl →
     * co-bog → us-dal → za-jnb → au-syd → jp-osa pattern observed
     * in v0.9.11.47).
     */
    private suspend fun applyHomeRegionRestrictIfMissing(
        pool: com.privycs.vpn.data.models.Pool
    ): com.privycs.vpn.data.models.Pool {
        if (pool.restrictRegions.isNotEmpty()) return pool
        val country = PrivycsApp.instance.selfIpDetector.cachedResult()?.country.orEmpty()
        if (country.isEmpty()) {
            PrivycsLogger.d(TAG, "auto-restrict skip: SelfIp cache cold for pool ${pool.name}")
            return pool
        }
        val homeRegion = com.privycs.vpn.data.PoolPicker.regionForCountry(country)
        if (homeRegion.isEmpty() || homeRegion == "Other") {
            PrivycsLogger.d(TAG, "auto-restrict skip: country=$country maps to '$homeRegion'")
            return pool
        }
        PrivycsLogger.i(TAG, "auto-restrict pool ${pool.name} to $homeRegion (user country=$country)")
        val updated = pool.copy(restrictRegions = listOf(homeRegion))
        PrivycsApp.instance.poolRepository.update(updated)
        return updated
    }

    /**
     * Common post-connect bookkeeping for pool members. Called from
     * BOTH handlePoolConnect (initial activate) and handlePoolRotate
     * (subsequent rotations) so the same status-broadcast +
     * scheduler re-arm + scheduledRotationAt logic runs in one place.
     *
     * Mirrors desktop's app_pool.go:1062-1070 + the rotator's
     * fireRotationLocked which re-schedules itself via
     * `r.scheduledRotation = time.Now().Add(intervalMin*Min)`.
     *
     * Three side effects, in this exact order:
     *   1. Persist scheduledRotationAt = now + effectiveInterval. The
     *      UI countdown reads this from VpnStatus.nextRotationAt.
     *   2. Re-arm the AlarmManager pre-warm + rotate alarms for the
     *      next cycle (Round-Robin only).
     *   3. Broadcast the pool-aware VpnStatus so the Connect screen
     *      renders pool name, current member, country, and the live
     *      countdown anchor.
     */
    private suspend fun onPoolMemberConnected(
        pool: com.privycs.vpn.data.models.Pool,
        member: com.privycs.vpn.data.models.PoolMember,
        @Suppress("UNUSED_PARAMETER") isRotation: Boolean
    ) {
        val pools = PrivycsApp.instance.poolRepository

        // Step 1: scheduledRotationAt. For non-RR pools we keep zero
        // so the UI hides the countdown.
        val rotationAt: Long = if (pool.policy == com.privycs.vpn.data.models.PoolPolicy.ROUND_ROBIN) {
            val intervalMs = poolScheduler.effectiveIntervalMs(pool.rotation.intervalMin)
            System.currentTimeMillis() + intervalMs
        } else {
            0L
        }
        pools.state.setScheduledRotationAt(pool.id, rotationAt)

        // Step 2: re-arm the alarm chain for the next rotation cycle.
        // Pool member-state writes inside arm() use the scheduler's
        // effectiveIntervalMs, which already applies the battery-saver
        // doubling so step 1's value matches.
        if (pool.policy == com.privycs.vpn.data.models.PoolPolicy.ROUND_ROBIN) {
            poolScheduler.arm(pool.id, pool.rotation.intervalMin)
        }

        // Step 3: enrich and push the VpnStatus. Single source of
        // truth: poolName / activeMember / pendingMember / countdown
        // anchor all flow through manager.updateStatus(). Compose
        // observers (ConnectScreen, PoolIndicatorCard) collect the
        // resulting StateFlow and recompose live.
        broadcastPoolStatus(pool)
    }

    /**
     * Reads the latest pool runtime state and pushes a VpnStatus
     * carrying the pool context into VpnServiceManager. Called after
     * connect, rotation, AND pre-warm (so the "Next: X" line lights
     * up 60s ahead of the rotation tick).
     *
     * Reuses the current VpnStatus's connected/uptime/rxBytes/txBytes
     * values when available — those come from the underlying tunnel's
     * own poll loop, we just overlay the pool-level fields on top.
     * Without the overlay step, the WG/OVPN/IPSec status pushers
     * blow away the pool fields on every poll tick.
     */
    private suspend fun broadcastPoolStatus(pool: com.privycs.vpn.data.models.Pool) {
        val pools = PrivycsApp.instance.poolRepository
        val activeMemId = pools.state.activeMemberId(pool.id)
        val pendingMemId = pools.state.pendingMemberId(pool.id)
        val activeMember = pool.members.firstOrNull { it.id == activeMemId }
        val pendingMember = pool.members.firstOrNull { it.id == pendingMemId }
        val rotationAt = pools.state.scheduledRotationAt(pool.id)

        val manager = com.privycs.vpn.service.VpnServiceManager.getInstance(this)
        val current = manager.status.value
        // serverEndpoint AND localAddress are pulled from the active
        // member's stored config so the Connect screen's
        // ConnectionDetails panel (VPN IP / Endpoint / last
        // handshake) shows the same fields for pools as it does for
        // single connections. Pre-fix only serverEndpoint was set;
        // localAddress was left blank, which combined with the
        // ConnectScreen gate `if (activeConnection != null)` hid
        // the entire detail panel for pool users. activeConnection
        // is null when a pool is the active selection because pool
        // activation explicitly clears the single-connection
        // activeId.
        manager.updateStatus(
            current.copy(
                connectionName = pool.name,
                connectionId = "pool:${pool.id}",
                poolId = pool.id,
                poolName = pool.name,
                poolPolicy = pool.policy.name,
                activeMemberName = activeMember?.name.orEmpty(),
                activeMemberCountry = activeMember?.country.orEmpty(),
                pendingMemberName = pendingMember?.name.orEmpty(),
                pendingMemberCountry = pendingMember?.country.orEmpty(),
                nextRotationAt = rotationAt,
                serverEndpoint = activeMember?.config?.serverAddress.orEmpty(),
                localAddress = activeMember?.config?.localAddress.orEmpty(),
            )
        )
    }

    /**
     * Called by the OS when the user swipes the app out of the
     * Recent-Apps screen. v0.9.14.77 mitigation for aggressive OEM
     * task-killers (Samsung One UI, Xiaomi MIUI, Huawei EMUI, Oppo,
     * Vivo): even though we're a foreground service that should
     * survive task removal per Android spec, those OEMs kill the
     * service anyway. We schedule a self-restart via AlarmManager
     * with `setAndAllowWhileIdle` (inexact — needs no alarm
     * permission) so the service comes back within a few seconds of
     * the swipe even if the OEM killed us; the alarm survives Doze.
     *
     * Only re-arms when:
     *   - Foreground-keep-monitor-alive is enabled (otherwise the
     *     user explicitly opted out of always-on monitoring)
     *   - OR a tunnel is currently up (so user-data flow shouldn't
     *     vanish on a swipe)
     */
    override fun onTaskRemoved(rootIntent: android.content.Intent?) {
        super.onTaskRemoved(rootIntent)
        scope.launch {
            val s = try {
                PrivycsApp.instance.settingsRepository.getSettingsBlocking()
            } catch (_: Throwable) {
                null
            }
            val keepAlive = s?.keepMonitorAlive == true &&
                PrivycsApp.instance.networkRulesRepository.hasRules
            val haveTunnel = currentProtocol != null
            if (!keepAlive && !haveTunnel) {
                PrivycsLogger.d(TAG, "onTaskRemoved: no keep-alive + no tunnel — letting service die naturally")
                return@launch
            }

            PrivycsLogger.i(
                TAG,
                "onTaskRemoved: scheduling self-restart in 5s (keepAlive=$keepAlive haveTunnel=$haveTunnel)"
            )
            try {
                val intent = android.content.Intent(
                    applicationContext,
                    PrivycsVpnService::class.java,
                ).apply {
                    action = if (haveTunnel) {
                        // No connect-id; service comes back, sees
                        // currentProtocol still set in-memory IF the
                        // process survived; if not, the OS-level
                        // Always-On VPN reconnect code path engages.
                        ACTION_START_MONITOR
                    } else {
                        ACTION_START_MONITOR
                    }
                }
                val pi = android.app.PendingIntent.getForegroundService(
                    applicationContext,
                    7777,
                    intent,
                    android.app.PendingIntent.FLAG_IMMUTABLE or
                        android.app.PendingIntent.FLAG_UPDATE_CURRENT,
                )
                val am = applicationContext.getSystemService(android.content.Context.ALARM_SERVICE)
                    as android.app.AlarmManager
                am.setAndAllowWhileIdle(
                    android.app.AlarmManager.ELAPSED_REALTIME_WAKEUP,
                    android.os.SystemClock.elapsedRealtime() + 5_000L,
                    pi,
                )
            } catch (t: Throwable) {
                PrivycsLogger.e(TAG, "onTaskRemoved: self-restart schedule failed", t)
            }
        }
    }

    override fun onDestroy() {
        killSwitchNetworkCallback?.let { cb ->
            try {
                (getSystemService(CONNECTIVITY_SERVICE) as android.net.ConnectivityManager)
                    .unregisterNetworkCallback(cb)
            } catch (e: Exception) {
                PrivycsLogger.d(TAG, "Kill switch callback unregister failed (already gone?): ${e.message}")
            }
            killSwitchNetworkCallback = null
        }
        // Defensive: explicitly close the sinkhole fd before
        // cancelling the scope. The OS will close any tun fd held by
        // VpnService on destroy anyway, but releasing it ourselves
        // (a) lets us null the field so any zombie references see
        // null instead of a closed-fd crash, (b) makes the lifecycle
        // semantics explicit for code review, (c) matches the
        // pattern in exitSinkholeMode for symmetry.
        exitSinkholeMode()
        // Unregister the battery-saver receiver that
        // PoolRotationScheduler.arm() registers via
        // ensureBatterySaverReceiverRegistered. Without this, every
        // service-destroy cycle leaks an IntentReceiver
        // (visible as IntentReceiverLeaked in logcat) AND prevents
        // the dying Service instance from being GC'd because the
        // receiver closure references it.
        try {
            poolScheduler.unregisterBatterySaverReceiver()
        } catch (e: Exception) {
            PrivycsLogger.d(TAG, "battery-saver unregister failed: ${e.message}")
        }
        scope.cancel()
        super.onDestroy()
        PrivycsLogger.d(TAG, "VPN service destroyed")
    }

    override fun onRevoke() {
        // VPN permission revoked by user or system. Typical triggers:
        // the user disables our Always-On toggle, another VPN app
        // takes over the VPN slot, or the user taps Disconnect on the
        // system VPN settings page.
        //
        // CRITICAL self-revoke detection (v0.9.14.18):
        // Android also invokes onRevoke whenever a VpnService session
        // ENDS — including when our OWN code closes the underlying
        // ParcelFileDescriptor as part of pool rotation
        // (wireGuardTunnel.disconnect → GoBackend.setState(DOWN) →
        // PFD close → Android framework → onRevoke). In that case
        // there is no actual permission revocation; we are about to
        // open a fresh session for the next pool member within a
        // few hundred ms. Falling through to handleDisconnect →
        // stopSelf would kill the service mid-rotation, leaving a
        // 30+ second VPN-blackout window before AlarmManager fires
        // the next POOL_CONNECT intent and the service comes back.
        // User-reported symptom: "rotation hängengeblieben" with
        // a logcat showing onDestroy + 35s gap + service restart.
        //
        // Detection: ConnectCoordinator's State.Connecting means we
        // are mid-cycle. The state was set when requestPoolConnect
        // accepted the rotation tick, and won't transition to
        // Connected until the new tunnel reports up. Any onRevoke
        // during Connecting is by definition self-induced — the
        // framework would not be revoking us when we are still
        // arming the next session.
        val coordState = com.privycs.vpn.util.ConnectCoordinator.state.value
        if (coordState is com.privycs.vpn.util.ConnectCoordinator.State.Connecting) {
            PrivycsLogger.i(
                TAG,
                "onRevoke: self-revoke during Connecting (source=${coordState.source}, target=${coordState.connectionId}) — ignoring, NOT calling stopSelf"
            )
            // DO NOT call super.onRevoke() — default impl calls
            // stopSelf() which is exactly what we are trying to
            // avoid here. Service stays alive, the in-flight
            // connectWireGuard / connectIpSec / connectOpenVpn
            // will complete and re-establish the tunnel.
            return
        }

        // Genuine external revoke. Stamp the timestamp so
        // NetworkMonitor skips on-demand auto-reconnect for the next
        // few seconds - otherwise it collides with the in-flight
        // service teardown and spawns a second GoBackend on the same
        // /dev/tun (observed symptom: "Failed to write packet to TUN
        // device: input/output error" + keepalive storm).
        PrivycsLogger.w(TAG, "VPN permission revoked (external — coordinator state=$coordState)")
        com.privycs.vpn.util.AlwaysOnDetector.stampSystemRevoke(this)
        handleDisconnect()
        super.onRevoke()
    }

    private fun handleConnect(
        connectionId: String,
        protocolStr: String,
        configContent: String,
        connectionName: String
    ) {
        val newProtocol = VpnProtocol.fromString(protocolStr)

        scope.launch {
            // Serialise against any concurrent connect/disconnect/
            // teardown so the singleton tunnel fields aren't mutated
            // from two coroutines at once. See `tunnelMutex` doc.
            tunnelMutex.withLock {
            try {
                // GUARD 0: hardcore Kill Switch lock. ConnectCoordinator
                // also gates this in requestConnect/markAlwaysOnConnecting,
                // but a stale ACTION_CONNECT intent could already be in
                // flight from before the sinkhole engaged - or some
                // future caller could bypass the coordinator. Refuse
                // unconditionally so the sinkhole tun fd stays in place
                // and the only release path remains the user toggling
                // KS off in Settings.
                if (com.privycs.vpn.util.KillSwitchManager.isSinkholeActive()) {
                    PrivycsLogger.w(TAG, "handleConnect refused: sinkhole active - manual KS toggle off required")
                    com.privycs.vpn.util.ConnectCoordinator.markDisconnected()
                    return@launch
                }

                // GUARD: refuse connect attempts when there is no
                // underlying non-VPN network at all. Without this guard
                // the WG handshake hangs forever (no UDP reply possible),
                // the ConnectCoordinator stays stuck in Connecting state,
                // and when the user later disabled the Kill Switch the
                // tunnel could not establish - traffic would flow direct
                // (= VPN LEAK) until the 90s watchdog released the
                // Connecting state. Connect attempts with no network are
                // pointless regardless of KS state, so refuse universally.
                //
                // Reset the coordinator to Idle so the next attempt
                // (after network returns) can run cleanly. Update the
                // notification to either "Kill Switch active" or just
                // "No network" depending on KS state.
                run {
                    val cm = getSystemService(CONNECTIVITY_SERVICE)
                        as android.net.ConnectivityManager
                    if (!hasAnyNonVpnNetwork(cm)) {
                        val sinkhole = com.privycs.vpn.util.KillSwitchManager
                            .isSinkholeActive()
                        PrivycsLogger.w(TAG, "handleConnect refused: no underlying non-VPN network (sinkhole=$sinkhole)")
                        com.privycs.vpn.util.ConnectCoordinator.markDisconnected()
                        if (sinkhole) {
                            updateNotification(
                                getString(R.string.vpn_notification_kill_switch_no_network),
                                sinkholeMode = true,
                            )
                        } else {
                            updateNotification(getString(R.string.vpn_notification_cannot_connect_no_network))
                        }
                        return@launch
                    }
                }

                // CRITICAL: tear down ANY previous protocol tunnel before
                // starting a new one. Android VpnService allows only one
                // active TUN per user; if a previous tunnel's native-side
                // state (WireGuard GoBackend goroutines, strongSwan charon,
                // OpenVPN subprocess + management thread) is still alive
                // when the new protocol calls VpnService.Builder.establish(),
                // the new tunnel fd collides with the old one's writes. The
                // symptom is a connected-looking UI where no app traffic
                // reaches the remote server (server shows only keepalives)
                // and the old protocol's goroutines spam
                // "Failed to write packet to TUN device: input/output error"
                // until ping-restart kills the new tunnel ~60s later.
                //
                // Kill EVERY possible leftover, not just the one matching
                // currentProtocol: a zombie from a crashed previous connect
                // may have currentProtocol=null but still hold a tunnel
                // object referenced from the singleton field.
                teardownAllProtocols()

                currentConnectionId = connectionId
                currentConnectionName = connectionName
                currentProtocol = newProtocol

                if (newProtocol == null) {
                    PrivycsLogger.e(TAG, "Unknown protocol: $protocolStr")
                    val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
                    manager.updateStatus(VpnStatus(error = getString(R.string.vpn_error_unknown_protocol, protocolStr)))
                    stopSelf()
                    return@launch
                }

                // Multi-config failover, mirroring desktop's
                // protocol_failover.go: tryFailoverProtocol. If the
                // primary config fails, walk the rest of the active
                // connection's orderedConfigs() (skipping the one
                // that just died) and try each in sequence. Persist
                // the swap via setActiveConfig so the UI pill row
                // reflects which config is actually live. First
                // success short-circuits the loop; if every config
                // fails the catch block below surfaces the LAST
                // error (caller sees a deterministic outcome rather
                // than the first-attempt's error masking later
                // attempts' progress).
                //
                // Pre-fix: handleConnect tried exactly one config
                // (the active one) and dropped to the catch on
                // failure — surfaced "Connection failed" with no
                // retry. Even if the user had a perfectly-working
                // backup config configured. TunnelHealthMonitor's
                // failover only fires AFTER a successful Up() when
                // the tunnel later goes dead via ICMP; the initial-
                // up path had no equivalent.
                val triedIds = mutableListOf<String>()
                val originalConfigId = PrivycsApp.instance.connectionRepository
                    .getById(connectionId)?.activeConfigId ?: ""
                var lastError: Throwable? = null
                var success = false

                // First attempt uses the supplied content + protocol
                // (already the active config). Subsequent attempts
                // read the next config from the registry.
                var attemptProto: VpnProtocol? = newProtocol
                var attemptContent: String = configContent
                var attemptConfigId: String = originalConfigId

                // Smart Decision Engine (active): when Automatic protocol
                // selection is on, the engine picks the FIRST protocol too —
                // override with its country-aware top choice. [v1.0.9]
                run {
                    val s = PrivycsApp.instance.settingsRepository.getSettingsBlocking()
                    if (s.autoProtocolSelection) {
                        val conn = PrivycsApp.instance.connectionRepository.getById(connectionId)
                        conn?.orderedConfigs(com.privycs.vpn.engine.EngineShadow.effectiveOrder(s))
                            ?.firstOrNull()?.let { c ->
                                attemptProto = c.protocol
                                attemptContent = c.configContent
                                attemptConfigId = c.id
                            }
                    }
                }

                while (attemptProto != null) {
                    val proto: VpnProtocol = attemptProto!!
                    val label = "${proto.label}/${attemptConfigId.take(8)}"
                    try {
                        PrivycsLogger.i(TAG, "handleConnect: try $label")
                        currentProtocol = proto
                        when (proto) {
                            VpnProtocol.WIREGUARD -> connectWireGuard(attemptContent, awg = false)
                            VpnProtocol.AMNEZIAWG -> connectWireGuard(attemptContent, awg = true)
                            VpnProtocol.OPENVPN -> connectOpenVpn(attemptContent)
                            VpnProtocol.IPSEC -> connectIpSec(attemptContent)
                        }
                        // v0.9.15.41: verifyTunnelTraffic-Block AUSKOMMENTIERT.
                        // Hintergrund: User-Test bestätigt dass pill-switch
                        // (AWG → WG) auf v0.9.15.19 noch funktionierte und
                        // seitdem broken ist. Diff zwischen den beiden
                        // Tags zeigt diesen Verify-Block (eingeführt in
                        // v0.9.15.20) als heißesten Regression-Kandidaten:
                        // er hält den Connect-Pfad bis zu 20 s blockierend
                        // bevor er als "stuck" wahrgenommen wird.
                        //
                        // Statt zu löschen lassen wir den Block hier
                        // stehen — die Health-Monitor-Recovery (10s ping)
                        // fängt silent-fail-Tunnel sowieso nach connect.
                        // Falls wir den Verify-Phase wieder brauchen,
                        // einfach uncomment und ggf. auf source==USER
                        // gaten (USER skip, automatic-mode keep).
                        //
                        // tunnelMutex.unlock()
                        // val verifyOk = try {
                        //     verifyTunnelTraffic(proto, label)
                        //     true
                        // } catch (verifyErr: Throwable) {
                        //     tunnelMutex.lock()
                        //     throw verifyErr
                        // }
                        // if (verifyOk) {
                        //     tunnelMutex.lock()
                        // }
                        success = true
                        if (attemptConfigId.isNotEmpty() && attemptConfigId != originalConfigId) {
                            PrivycsLogger.i(
                                TAG,
                                "failover succeeded: $originalConfigId → $attemptConfigId (tried: $triedIds)"
                            )
                            PrivycsApp.instance.connectionRepository
                                .setActiveConfig(connectionId, attemptConfigId)
                            com.privycs.vpn.util.EventNotifier.failover(
                                applicationContext,
                                "Now connected via ${proto.label} — the " +
                                    "previous connection failed and " +
                                    "automatically failed over.",
                            )
                        }
                        break
                    } catch (e: Throwable) {
                        PrivycsLogger.w(TAG, "handleConnect: $label FAILED: ${e.message}")
                        triedIds.add(attemptConfigId)
                        lastError = e
                        // Each failed attempt may have left native
                        // state behind (half-up GoBackend, dangling
                        // strongSwan SA, OpenVPN subprocess). Tear
                        // it down before the next attempt so the
                        // VpnService.Builder slot is free.
                        try { teardownAllProtocols() } catch (_: Throwable) { /* best-effort */ }
                    }

                    // Pick the next config to try, in user-configured
                    // failover order (default = AWG → WG → OVPN →
                    // IPSec). v0.9.15.70.
                    val refreshed = PrivycsApp.instance.connectionRepository.getById(connectionId)
                    // Smart Decision Engine drives the order when Automatic is on. [v1.0.9]
                    val failoverOrder = com.privycs.vpn.engine.EngineShadow.effectiveOrder(
                        PrivycsApp.instance.settingsRepository.getSettingsBlocking())
                    val nextCfg = refreshed?.orderedConfigs(failoverOrder)?.firstOrNull { cfg ->
                        cfg.id !in triedIds
                    }
                    if (nextCfg == null) {
                        PrivycsLogger.w(TAG, "handleConnect: no more failover candidates (tried: $triedIds)")
                        attemptProto = null
                    } else {
                        attemptProto = nextCfg.protocol
                        attemptContent = nextCfg.configContent
                        attemptConfigId = nextCfg.id
                    }
                }

                if (!success) {
                    throw (lastError ?: RuntimeException("connect failed (no candidates)"))
                }
            } catch (e: Exception) {
                // Reverted in v0.9.14.59: the v0.9.14.54-introduced
                // CancellationException catch-and-rethrow pattern was
                // intended to silence "Connection failed: Job was
                // cancelled" red banners during legitimate protocol
                // switches and network roams, but it ALSO swallowed
                // real failures whose underlying coroutine internals
                // surface as CancellationException (timeouts, scope
                // crashes, native-tunnel hangs that cancel the parent
                // job). The result was protocol-switch instability —
                // failing connects vanished silently and the user saw
                // nothing — plus a knock-on effect where stopSelf()
                // was skipped and zombie tunnels lingered into the
                // next connect attempt. Honest fail behaviour: every
                // exception (cancellation included) becomes a visible
                // error banner + stopSelf(). The cosmetic "Job was
                // cancelled" banner during a fast protocol switch is
                // accepted as the lesser evil. Stable behaviour beats
                // pretty banners.
                PrivycsLogger.e(TAG, "Connect failed", e)
                val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
                manager.updateStatus(VpnStatus(error = getString(R.string.vpn_error_connection_failed, e.message ?: "")))
                stopSelf()
            }
            } // tunnelMutex.withLock
        }
    }

    /**
     * Aggressively dispose all protocol tunnels and give native-side
     * cleanup time to complete before the next tunnel grabs the VpnService
     * slot. Safe to call even when no tunnel is active (all disconnect()
     * calls swallow exceptions on already-down tunnels).
     *
     * Delay rationale:
     * - WireGuard GoBackend select-loop goroutines: ~300-500ms to exit
     * - strongSwan charon IKE_SA_DELETE handshake: ~1-2s round-trip
     * - OpenVPN subprocess SIGTERM + management-socket close: ~200-500ms
     *
     * We wait 1500ms total so even the slowest (charon) has a chance to
     * finish. This shows up to the user as a brief pause on protocol
     * switch, which is acceptable vs the broken-tunnel symptom.
     */
    /**
     * Poll the active tunnel for traffic-proof-of-life after a
     * successful connect() return. Returns normally on success;
     * throws UpTimeoutException when the verify budget is exhausted
     * without seeing any rx data or localAddress assignment. The
     * surrounding failover loop in handleConnect catches the throw
     * and rotates to the next config candidate.
     *
     * Why polling rxBytes rather than e.g. peer.lastHandshake epoch:
     * rxBytes is universally available on all four protocol tunnels
     * (WG, AWG, OpenVPN, IPSec) and is the most direct signal that
     * the remote actually replied. handshake-epoch is WG/AWG-only
     * and exposed as a string in our VpnStatus; rxBytes covers
     * every protocol the same way.
     *
     * localAddress assignment is the secondary signal — protocols
     * that push an IP (OpenVPN, IPSec via IKE_AUTH) only assign
     * AFTER the negotiation completes, so localAddress != "" is
     * also proof the remote is reachable.
     */
    private class UpTimeoutException(msg: String) : RuntimeException(msg)

    private suspend fun verifyTunnelTraffic(proto: VpnProtocol, label: String) {
        val deadline = System.currentTimeMillis() + UP_TIMEOUT_MS
        var lastRx = 0L
        var checks = 0
        // v0.9.15.29: track whether we ever saw connected=true. Only
        // after that initial UP transition does a subsequent
        // connected=false indicate a real mid-verify disconnect.
        // The pre-v0.9.15.29 logic threw UpTimeoutException on the
        // FIRST !status.connected reading — which killed every IPSec
        // connect attempt mid-IKE because IKE_SA + CHILD_SA
        // negotiation takes ~10-30s and tunnel.connected stays false
        // throughout. The pill-switch user-test path then declared
        // IPSec failed after just 1× 500ms poll, fell over to the
        // next protocol slot, and landed on the wrong-peer AWG
        // config (sGRJ .1 instead of 88QO .2) which the user saw
        // as "tunnel kommt nicht hoch / falsche IP".
        var sawConnected = false
        while (System.currentTimeMillis() < deadline) {
            delay(UP_VERIFY_POLL_MS)
            checks++
            val status = when (proto) {
                VpnProtocol.WIREGUARD -> amneziaTunnel?.getStatus(currentConnectionName, currentConnectionId, VpnProtocol.WIREGUARD)
                VpnProtocol.AMNEZIAWG -> amneziaTunnel?.getStatus(currentConnectionName, currentConnectionId, VpnProtocol.AMNEZIAWG)
                VpnProtocol.OPENVPN -> openVpnTunnel?.getStatus(currentConnectionName, currentConnectionId)
                VpnProtocol.IPSEC -> ipSecTunnel?.getStatus(currentConnectionName, currentConnectionId)
            } ?: continue
            if (status.connected) {
                sawConnected = true
            } else if (sawConnected) {
                // Was up, then went away — real mid-verify drop.
                throw UpTimeoutException(
                    "verify[$label]: tunnel dropped after initial connect (checks=$checks)"
                )
            }
            // Strong signals — handshake complete + traffic flowing,
            // or a server-pushed local address arrived (OpenVPN,
            // IPSec). Either is "we're verified, hand back to the
            // long-lived status poller".
            if (status.rxBytes > 0 || status.localAddress.isNotBlank() ||
                status.lastHandshake.isNotBlank() && status.lastHandshake != "0") {
                PrivycsLogger.i(
                    TAG,
                    "verify[$label]: traffic confirmed after ${checks}× ${UP_VERIFY_POLL_MS}ms " +
                        "(rx=${status.rxBytes} local=${status.localAddress} hs=${status.lastHandshake})",
                )
                return
            }
            lastRx = status.rxBytes
        }
        throw UpTimeoutException(
            "verify[$label]: no traffic observed in ${UP_TIMEOUT_MS}ms " +
                "(${checks}× polls, lastRx=$lastRx, sawConnected=$sawConnected) — likely blackholed/handshake-stuck"
        )
    }

    private suspend fun teardownAllProtocols() {
        val hadSomething = amneziaTunnel != null ||
            openVpnTunnel != null || ipSecTunnel != null
        // v0.9.15.46: single AmneziaWG-backed tunnel serves both WG and
        // AWG protocols. The old wireGuardTunnel field + WG branch
        // were removed alongside `com.wireguard.android:tunnel` Maven
        // dependency to kill the switch-to-WG manifest-merger hang.
        try { amneziaTunnel?.disconnect() } catch (e: Exception) { PrivycsLogger.w(TAG, "WG/AWG teardown: ${e.message}") }
        amneziaTunnel = null
        try { openVpnTunnel?.disconnect() } catch (e: Exception) { PrivycsLogger.w(TAG, "OpenVPN teardown: ${e.message}") }
        openVpnTunnel = null
        try { ipSecTunnel?.disconnect() } catch (e: Exception) { PrivycsLogger.w(TAG, "IPSec teardown: ${e.message}") }
        ipSecTunnel = null

        if (hadSomething) {
            PrivycsLogger.i(TAG, "Previous tunnel torn down, waiting 1500ms for native-side cleanup")
            delay(1500)
        }
    }

    private suspend fun connectWireGuard(configContent: String, awg: Boolean) {
        // Three text-level patches before the parser sees the
        // config: Per-App VPN allow/exclude (existing), DNS
        // override (v0.9.11.53), and IPv6 leak killswitch
        // (v0.9.14.96). Each patches a different aspect of the
        // [Interface]/[Peer] sections so the parser builds a
        // VpnService.Builder that honours them. amneziawg-android's
        // config parser is API-compatible with wireguard-android
        // (same upstream wg-quick file format plus AWG keys), so
        // all three patches apply unchanged on either backend.
        val perAppPatched = patchWireGuardPerAppVpn(configContent)
        val dnsPatched = patchWireGuardDnsOverride(perAppPatched)
        val patchedConfig = ipv6PatchOrPassThrough(
            dnsPatched,
            if (awg) com.privycs.vpn.data.models.VpnProtocol.AMNEZIAWG
            else com.privycs.vpn.data.models.VpnProtocol.WIREGUARD
        )

        // v0.9.15.46: both WG and AWG route through the AmneziaWG
        // backend. The `awg` flag now only controls (a) which key set
        // ipv6PatchOrPassThrough patches against (no functional diff
        // — both protocols use the same AllowedIPs key) and (b) the
        // protocol identity reported in status. The backend itself is
        // identical for both. See class-level comment on `amneziaTunnel`
        // for the asymmetry-elimination rationale.
        val backend = awgBackend
            ?: throw IllegalStateException("AWG GoBackend not initialized")
        val tunnel = AmneziaTunnel(backend)
        amneziaTunnel = tunnel
        PrivycsLogger.i(TAG, "connectWireGuard: ${if (awg) "AMNEZIAWG" else "WIREGUARD"} via AWG backend")
        tunnel.connect(patchedConfig, "privycs0")

        connectStartTime = System.currentTimeMillis()
        updateNotification(getString(R.string.vpn_notification_connected, currentConnectionName))
        sendWidgetUpdate(connected = true)
        startStatusPolling()
    }

    /**
     * Reads Settings.dnsOverride and parses it into individual IPs.
     * Accepts comma-, space-, or whitespace-separated input. Empty
     * strings are filtered out so the user can paste lazy whitespace
     * without breaking the protocol-specific formatters downstream.
     */
    private fun resolveDnsOverrideServers(): List<String> {
        // Resolution priority chain (most-specific wins):
        //   1. Active pool's per-pool dnsOverride (if non-empty)
        //   2. Active single-connection's per-connection dnsOverride
        //      (if non-empty)
        //   3. Global Settings.dnsOverride
        //   4. Empty - the protocol's own DNS push wins
        //
        // Pool active and connection active are mutually exclusive
        // in the data model (pool activation clears single's
        // activeId), so these two branches don't both fire on the
        // same connect; the chain just enumerates all possible
        // override sources by precedence.
        val raw = try {
            val poolReg = PrivycsApp.instance.poolRepository.registry.value
            val activePool = poolReg.pools.firstOrNull { it.id == poolReg.activeId }
            val perPool = activePool?.dnsOverride.orEmpty().trim()
            if (perPool.isNotEmpty()) {
                perPool
            } else {
                val activeConn = PrivycsApp.instance.connectionRepository.getActive()
                val perConn = activeConn?.dnsOverride.orEmpty().trim()
                if (perConn.isNotEmpty()) {
                    perConn
                } else {
                    PrivycsApp.instance.settingsRepository.getSettingsBlocking().dnsOverride
                }
            }
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "DNS override read failed: ${e.message}")
            return emptyList()
        }
        if (raw.isBlank()) return emptyList()
        return com.privycs.vpn.util.DnsValidator.parseServers(raw)
    }

    /**
     * Replaces (or inserts) the `DNS = ...` line in the
     * [Interface] section of a WireGuard config with the user's
     * manual DNS-server override from Settings. No-op when the
     * override is empty - the config's own DNS line wins.
     *
     * Earlier the override field was only persisted to DataStore
     * and never reached any tunnel - the v0.9.11.52 audit found
     * zero callers in the connect path. v0.9.11.53 closes that
     * gap for all three protocols, this is the WireGuard arm.
     */

    /**
     * v0.9.14.96 — passive IPv6 leak detection. Returns true when
     * the OS has a non-VPN underlying network with an IPv6 default
     * route AND our active VPN does NOT have an IPv6 default route
     * — i.e. v6 traffic will exit via the underlying network instead
     * of our tunnel. Used post-IPSec-connect to detect when
     * server-side traffic-selector negotiation narrowed our v6
     * route away.
     *
     * Pure ConnectivityManager query — no network probes, no I/O.
     * Safe to call at any time; returns false (no leak) on any
     * indeterminate state (no underlying v6 to leak through).
     */
    private fun detectV6Leak(): Boolean {
        val cm = getSystemService(CONNECTIVITY_SERVICE) as android.net.ConnectivityManager
        var underlyingHasV6Default = false
        var vpnHasV6Default = false
        for (network in cm.allNetworks) {
            val caps = cm.getNetworkCapabilities(network) ?: continue
            val link = cm.getLinkProperties(network) ?: continue
            val isVpn = caps.hasTransport(
                android.net.NetworkCapabilities.TRANSPORT_VPN
            )
            for (route in link.routes) {
                val dest = route.destination ?: continue
                val addr = dest.address ?: continue
                if (dest.prefixLength != 0) continue
                if (addr !is java.net.Inet6Address) continue
                if (isVpn) {
                    vpnHasV6Default = true
                } else {
                    underlyingHasV6Default = true
                }
            }
        }
        return underlyingHasV6Default && !vpnHasV6Default
    }

    /**
     * v0.9.14.96 — central wrapper around IpV6KillswitchInjector.
     * Always-on per user requirement: leaving v6 leakable through
     * a v4-only tunnel is a critical security bug, so there is
     * NO setting to disable this. The injector itself is idempotent
     * (no-op on configs that already cover v6), and IPSec falls
     * back to best-effort negotiation with the server. Used by
     * connectWireGuard / connectOpenVpn / connectIpSec on the
     * single-connection path. Pool path also calls this directly.
     */
    private fun ipv6PatchOrPassThrough(
        configContent: String,
        protocol: com.privycs.vpn.data.models.VpnProtocol,
    ): String {
        val res = com.privycs.vpn.data.IpV6KillswitchInjector.inject(configContent, protocol)
        if (res.applied) {
            PrivycsLogger.i(TAG, "ipv6-killswitch: patched ${protocol.name} config")
        } else if (res.skippedReason != null) {
            PrivycsLogger.d(TAG, "ipv6-killswitch: skipped ${protocol.name} — ${res.skippedReason}")
        }
        return res.patched
    }

    private fun patchWireGuardDnsOverride(configContent: String): String {
        val servers = resolveDnsOverrideServers()
        if (servers.isEmpty()) return configContent
        val newLine = "DNS = ${servers.joinToString(", ")}"

        val lines = configContent.lines().toMutableList()
        var interfaceStart = -1
        var interfaceEnd = lines.size
        for (i in lines.indices) {
            val trimmed = lines[i].trim()
            if (trimmed.equals("[Interface]", ignoreCase = true)) {
                interfaceStart = i
            } else if (interfaceStart >= 0 && trimmed.startsWith("[")) {
                interfaceEnd = i
                break
            }
        }
        if (interfaceStart < 0) {
            PrivycsLogger.w(TAG, "DNS override (WG): [Interface] section not found, override skipped")
            return configContent
        }

        // Replace existing DNS line if present, else insert at end
        // of [Interface] section (just before next section header or
        // end of file).
        var replaced = false
        for (i in (interfaceStart + 1) until interfaceEnd) {
            val trimmed = lines[i].trim()
            if (trimmed.startsWith("DNS", ignoreCase = true) &&
                trimmed.replaceBefore('=', "").startsWith("=")
            ) {
                lines[i] = newLine
                replaced = true
                break
            }
        }
        if (!replaced) {
            lines.add(interfaceEnd, newLine)
        }
        PrivycsLogger.i(TAG, "DNS override (WG): applied ${servers.joinToString(",")} (${if (replaced) "replaced" else "inserted"})")
        return lines.joinToString("\n")
    }

    /**
     * Inject ExcludedApplications / IncludedApplications into the
     * [Interface] section of a WireGuard config based on the
     * "split_tunnel" SharedPreferences. In INCLUDE mode we also
     * append our own package name so the service can reach the
     * VPN server (otherwise the handshake traffic itself would be
     * filtered out and the tunnel would never establish).
     *
     * Returns the config unchanged if no apps are selected or the
     * [Interface] header cannot be located.
     */
    private fun patchWireGuardPerAppVpn(configContent: String): String {
        val prefs = getSharedPreferences("split_tunnel", Context.MODE_PRIVATE)
        val mode = prefs.getString("mode", "exclude") ?: "exclude"
        val packages = prefs.getStringSet("packages", emptySet()) ?: emptySet()
        if (packages.isEmpty()) {
            PrivycsLogger.d(TAG, "Per-App VPN (WG): no apps configured, config unchanged")
            return configContent
        }

        // Degenerate-include guard: include-mode where the only
        // selected package is our own VPN client means the user
        // accidentally configured "tunnel nothing" (we always self-
        // include for the handshake, so a zero-user-app include
        // results in no user traffic going through the tunnel at
        // all). Skip injection so the default (everything through
        // the tunnel) takes over and log a clear warning so the
        // user can see why their per-app picks aren't honored.
        if (mode == "include" &&
            packages.size == 1 &&
            packages.first() == packageName
        ) {
            PrivycsLogger.w(
                TAG,
                "Per-App VPN (WG): include-mode but only our own package " +
                "selected - skipping injection (no user apps would tunnel). " +
                "Pick at least one user app or switch to exclude-mode."
            )
            return configContent
        }

        val finalPackages = if (mode == "include") {
            // Critical: our own package MUST be in the allow-list or
            // the WG handshake itself gets blocked and the tunnel
            // never comes up.
            packages + packageName
        } else {
            packages
        }
        val key = if (mode == "include") "IncludedApplications" else "ExcludedApplications"
        val value = finalPackages.joinToString(", ")
        // Defensive: empty value would emit "IncludedApplications = "
        // which the WG parser may interpret as include-nothing,
        // routing user apps direct (= VPN leak). Should be unreachable
        // given the guards above, but bail explicitly to avoid the
        // injection emitting a malformed line.
        if (value.isBlank()) {
            PrivycsLogger.e(TAG, "Per-App VPN (WG): empty package list - aborting injection")
            return configContent
        }
        val injected = "$key = $value"

        val lines = configContent.lines().toMutableList()
        var interfaceIdx = -1
        var insertIdx = -1
        for (i in lines.indices) {
            val trimmed = lines[i].trim()
            if (trimmed.equals("[Interface]", ignoreCase = true)) {
                interfaceIdx = i
            } else if (interfaceIdx >= 0 && trimmed.startsWith("[")) {
                insertIdx = i
                break
            }
        }
        if (interfaceIdx < 0) {
            PrivycsLogger.w(TAG, "Per-App VPN (WG): [Interface] section not found, config unchanged")
            return configContent
        }
        val at = if (insertIdx > 0) insertIdx else lines.size
        lines.add(at, injected)
        PrivycsLogger.i(TAG, "Per-App VPN (WG): mode=$mode, ${finalPackages.size} packages injected")
        return lines.joinToString("\n")
    }

    private suspend fun connectOpenVpn(configContent: String) {
        // OpenVPN is owned by ics-openvpn's OpenVPNService which runs in the
        // :openvpn process and calls VpnService.Builder() internally. Our
        // own VpnService instance does NOT establish a tun fd for OpenVPN -
        // only one VpnService at a time can hold the slot, and we hand it
        // to OpenVPNService. PrivycsVpnService stays alive purely as a
        // controller so handleDisconnect() has an instance to dispatch
        // through.
        val tunnel = OpenVpnTunnel(applicationContext)
        openVpnTunnel = tunnel

        // Mirror IPSec: forward live state transitions into VpnServiceManager
        // so the UI reflects CONNECTING / CONNECTED / DISCONNECTED without
        // needing a poll loop. Upstream VpnStatus fires its StateListener on
        // every relevant native event (AUTH, GET_CONFIG, CONNECTED, ...)
        // and we translate them to our 3-state enum in OpenVpnTunnel.mapLevel.
        val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
        tunnel.onStateChanged = { s ->
            val connected = s == OpenVpnTunnel.State.CONNECTED
            manager.updateStatus(tunnel.getStatus(currentConnectionName, currentConnectionId))
            when (s) {
                OpenVpnTunnel.State.CONNECTING -> updateNotification(getString(R.string.vpn_notification_connecting_protocol, currentConnectionName, "OpenVPN"))
                OpenVpnTunnel.State.CONNECTED  -> updateNotification(getString(R.string.vpn_notification_connected_protocol, currentConnectionName, "OpenVPN"))
                OpenVpnTunnel.State.DISCONNECTING -> updateNotification(getString(R.string.vpn_notification_disconnecting))
                OpenVpnTunnel.State.DISCONNECTED -> updateNotification(getString(R.string.vpn_notification_disconnected))
                OpenVpnTunnel.State.FAILED -> updateNotification(getString(R.string.vpn_notification_openvpn_failed))
            }
            sendWidgetUpdate(connected = connected)
        }

        // DNS override: prepend two directives at the top of the
        // config so they take effect before any server-pushed
        // values arrive. `pull-filter ignore "dhcp-option DNS"`
        // drops the server's DNS push; `dhcp-option DNS X.X.X.X`
        // emits one line per override IP. ics-openvpn's parser
        // recognises both directives; the resulting profile has
        // mDns1/mDns2 set from our override instead of the
        // server's value.
        val dnsPatchedOvpn = patchOpenVpnDnsOverride(configContent)
        // v0.9.14.96: chain in IPv6 leak killswitch — appends
        // route-ipv6 ::/0 + redirect-gateway ipv6 directives if
        // not already present and the user setting is on.
        val patchedConfigOvpn = ipv6PatchOrPassThrough(
            dnsPatchedOvpn, com.privycs.vpn.data.models.VpnProtocol.OPENVPN
        )

        // Pass currentConnectionId (the stable VpnConnection.id we hand
        // through from VpnServiceManager) so OpenVpnTunnel forces the
        // same deterministic UUID PrivycsApp.preloadOpenVpnProfiles()
        // used at app boot - this closes the pre-load <-> connect UUID
        // race.
        tunnel.connect(patchedConfigOvpn, currentConnectionName, this@PrivycsVpnService, currentConnectionId)

        connectStartTime = System.currentTimeMillis()
        sendWidgetUpdate(connected = false)
        // Shared poll loop: state-listener callbacks drive connected/uptime,
        // byte counters tick live via ByteCountListener. We still run the
        // periodic poll for parity with WireGuard/IPSec so the UI refresh
        // cadence is uniform across protocols.
        startStatusPolling()
    }

    /**
     * Prepends `pull-filter ignore "dhcp-option DNS"` plus one
     * `dhcp-option DNS <ip>` directive per override server to an
     * OpenVPN config. The ignore-filter drops the server-pushed
     * DNS value so our explicit dhcp-option lines win.
     *
     * No-op when the override is empty - server-pushed DNS keeps
     * its original behavior.
     */
    private fun patchOpenVpnDnsOverride(configContent: String): String {
        val servers = resolveDnsOverrideServers()
        if (servers.isEmpty()) return configContent
        val sb = StringBuilder()
        sb.appendLine("pull-filter ignore \"dhcp-option DNS\"")
        for (s in servers) sb.appendLine("dhcp-option DNS $s")
        sb.append(configContent)
        PrivycsLogger.i(TAG, "DNS override (OVPN): applied ${servers.joinToString(",")}")
        return sb.toString()
    }

    private suspend fun connectIpSec(configContent: String) {
        // For IPSec the actual VPN tunnel is owned by strongSwan's
        // CharonVpnService (started indirectly by IpSecTunnel.connect via
        // VpnStateService). We keep this service alive as a thin controller
        // so handleDisconnect() still has an instance to route through.
        // Only one VpnService can hold the tunnel slot at a time, so we do
        // NOT call VpnService.Builder.establish() here - CharonVpnService
        // holds the TUN.
        val tunnel = IpSecTunnel(applicationContext)
        ipSecTunnel = tunnel

        // Forward strongSwan's live state transitions into our VpnStatus so
        // the UI reflects CONNECTING/CONNECTED/DISCONNECTED without polling.
        val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
        tunnel.onStateChanged = { s ->
            val connected = s == IpSecTunnel.State.CONNECTED
            manager.updateStatus(tunnel.getStatus(currentConnectionName, currentConnectionId))
            when (s) {
                IpSecTunnel.State.CONNECTING -> updateNotification(getString(R.string.vpn_notification_connecting_protocol, currentConnectionName, "IPSec"))
                IpSecTunnel.State.CONNECTED  -> updateNotification(getString(R.string.vpn_notification_connected_protocol, currentConnectionName, "IPSec"))
                IpSecTunnel.State.DISCONNECTING -> updateNotification(getString(R.string.vpn_notification_disconnecting))
                IpSecTunnel.State.DISCONNECTED -> updateNotification(getString(R.string.vpn_notification_disconnected))
            }
            sendWidgetUpdate(connected = connected)
        }

        // v0.9.14.96: IPv6 leak killswitch for IPSec — best-effort
        // patches remote_ts in the .sswan JSON to include ::/0.
        // strongSwan negotiates traffic selectors with the server
        // during IKE_AUTH; a v4-only server may narrow back to
        // 0.0.0.0/0, in which case v6 still leaks (server-side
        // limitation, see IpV6KillswitchInjector kdoc). Costs
        // nothing on negotiation when server agrees.
        val patchedSswan = ipv6PatchOrPassThrough(
            configContent, com.privycs.vpn.data.models.VpnProtocol.IPSEC
        )

        tunnel.connect(
            patchedSswan,
            currentConnectionName,
            this@PrivycsVpnService,
            dnsOverrideServers = resolveDnsOverrideServers()
        )

        // v0.9.14.96: post-connect IPv6 leak check for IPSec.
        // strongSwan negotiates traffic selectors with the server
        // during IKE_AUTH; a v4-only server may narrow our requested
        // ::/0 back to 0.0.0.0/0, leaving v6 unprotected. Our config
        // injection happened best-effort; this passive check
        // determines whether the negotiated tunnel actually captured
        // v6, and warns the user if not. WireGuard / OpenVPN don't
        // need this — their routes are unilateral, no negotiation.
        scope.launch {
            kotlinx.coroutines.delay(5_000) // wait for tunnel to fully establish
            if (detectV6Leak()) {
                PrivycsLogger.w(TAG, "ipv6-leak-warning: IPSec tunnel does not capture v6, native v6 default route active")
                // Surface to UI via VpnServiceManager error field; UI
                // can subscribe via status flow and show a banner.
                VpnServiceManager.getInstance(this@PrivycsVpnService).emitWarning(
                    getString(R.string.ipv6_leak_warning)
                )
            } else {
                PrivycsLogger.d(TAG, "ipv6-leak check: tunnel captures v6 OR OS has no v6 — clean")
            }
        }

        connectStartTime = System.currentTimeMillis()
        sendWidgetUpdate(connected = false)
        // Start the shared poll loop - VpnStateListener only fires on STATE
        // changes, not every second, so uptime stays frozen at the moment
        // of CONNECTED without a periodic push. Byte counters remain 0
        // (charon's public API does not expose per-SA byte counters through
        // VpnStateService; that requires a separate native bridge).
        startStatusPolling()
    }

    private fun handleDisconnect() {
        // Cancel any armed pool-rotation alarms first. Without this
        // a user-initiated disconnect leaves the AlarmManager schedule
        // intact: 60s before the next rotation tick PoolAlarmReceiver
        // wakes, fires ACTION_POOL_PRE_WARM, and shortly after
        // ACTION_POOL_ROTATE — which restarts the service and brings
        // the tunnel back up against the user's stated intent. The
        // alarms are scoped to whatever poolId was last armed, so a
        // blanket cancelAll() is sufficient.
        try {
            poolScheduler.cancelAll()
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "pool alarm cancel failed: ${e.message}")
        }

        // Clear scheduledRotationAt for the active pool so the UI
        // countdown immediately stops ticking. Without this clear,
        // VpnStatus.nextRotationAt still carries a future epoch
        // value that the PoolIndicatorCard would happily count down
        // against - showing a phantom "Next rotation in 4:32" on a
        // disconnected tunnel. Mirrors desktop's
        // pool_rotator.go:213-215 where Status() returns Active=false
        // when getIsActive() reports tunnel down.
        scope.launch {
            val activePoolId = PrivycsApp.instance.poolRepository.registry.value.activeId
            if (activePoolId.isNotEmpty()) {
                PrivycsApp.instance.poolRepository.state
                    .setScheduledRotationAt(activePoolId, 0L)
            }
        }
        scope.launch {
            // Serialise against any concurrent handleConnect /
            // forceTeardownAfterSinkhole / pool-failure teardown.
            // See `tunnelMutex` doc on the class for the failure
            // mode this prevents.
            tunnelMutex.withLock {
                // v0.9.15.33: ALWAYS tear down every tunnel singleton
                // unconditionally, ignoring currentProtocol. Pre-this
                // change the `when (currentProtocol)` branched to only
                // ONE tunnel — if pill-switch / boot-respawn / earlier
                // failed-connect had left a sibling singleton non-null
                // (or a separate VPN-service like CharonVpnService /
                // OpenVPNService alive in its own process), that
                // leftover survived the disconnect and Android kept
                // showing the VPN-active key icon. The previous
                // currentProtocol-only branch was a performance
                // micro-optimisation that bought nothing once the
                // tunnel.disconnect() calls are individually
                // null-safe (each `?.disconnect()` no-ops on a null
                // singleton). Worth the audit-cleanliness.
                try {
                    // Capture singleton-active flags BEFORE nulling so
                    // we can gate the per-protocol explicit stop
                    // intents below. v0.9.15.37 regression: sending
                    // DISCONNECT_VPN to OpenVPNService via startService
                    // RESURRECTS the service if it wasn't already
                    // running (ics-openvpn's onCreate logs "Restarting
                    // OpenVPN Service (App crashed probably crashed or
                    // killed for memory pressure)" and re-loads the
                    // last connected profile via LAST_CONNECTED_PROFILE
                    // persistence → auto-reconnect kicks in 1-2 s
                    // later → claims the system VPN slot → clobbers
                    // any new WG/AWG/IPSec tunnel that was meanwhile
                    // brought up. Same risk shape for CharonVpnService.
                    // Only fire the intents when the respective
                    // tunnel singleton was actually instantiated.
                    // v0.9.15.46: wg+awg unified — single `wgAwgActive` flag
                    val wgAwgActive = amneziaTunnel != null
                    val ovpnActive = openVpnTunnel != null
                    val ipsecActive = ipSecTunnel != null
                    PrivycsLogger.i(
                        TAG,
                        "handleDisconnect: nuclear teardown — " +
                            "wgAwg=$wgAwgActive ovpn=$ovpnActive ipsec=$ipsecActive " +
                            "currentProtocol=$currentProtocol",
                    )
                    try { amneziaTunnel?.disconnect() } catch (e: Exception) {
                        PrivycsLogger.w(TAG, "WG/AWG disconnect: ${e.message}")
                    }
                    amneziaTunnel = null
                    try { openVpnTunnel?.disconnect() } catch (e: Exception) {
                        PrivycsLogger.w(TAG, "OpenVPN disconnect: ${e.message}")
                    }
                    openVpnTunnel = null
                    try { ipSecTunnel?.disconnect() } catch (e: Exception) {
                        PrivycsLogger.w(TAG, "IPSec disconnect: ${e.message}")
                    }
                    ipSecTunnel = null

                    // Belt-and-braces: explicit stop intents — gated
                    // on the captured *Active flags so we don't wake
                    // a service that wasn't running. Plus: when
                    // OpenVPN WAS running, clear ics-openvpn's
                    // LAST_CONNECTED_PROFILE preference BEFORE sending
                    // the disconnect intent. Without that step
                    // OpenVPNService re-reads the profile on its next
                    // start-cycle (Android START_STICKY + null-intent
                    // restart) and auto-reconnects, defeating the
                    // disconnect entirely.
                    if (ipsecActive) {
                        try {
                            val charonStop = Intent(
                                this@PrivycsVpnService,
                                org.strongswan.android.logic.CharonVpnService::class.java,
                            ).apply {
                                action = org.strongswan.android.logic
                                    .CharonVpnService.DISCONNECT_ACTION
                            }
                            startService(charonStop)
                        } catch (e: Exception) {
                            PrivycsLogger.w(TAG, "CharonVpnService stop intent: ${e.message}")
                        }
                    }
                    if (ovpnActive) {
                        try {
                            de.blinkt.openvpn.core.ProfileManager
                                .setConntectedVpnProfileDisconnected(this@PrivycsVpnService)
                        } catch (e: Exception) {
                            PrivycsLogger.w(TAG, "OpenVPN clear last-profile: ${e.message}")
                        }
                        // v0.9.15.51: removed the redundant
                        //   startService(intent.setAction(DISCONNECT_VPN))
                        //   stopService(intent)
                        // pair that used to live here. It was supposed to be a
                        // belt-and-braces stop signal but in practice TRIGGERED
                        // the post-disconnect zombie pattern responsible for the
                        // "switch from OpenVPN to WireGuard/AmneziaWG silently
                        // fails" symptom.
                        //
                        // Root cause: ICS-OpenVPN's OpenVPNService.onStartCommand
                        // (vendor/.../OpenVPNService.java:516-556) has NO handler
                        // for the DISCONNECT_VPN action. When we send a
                        // startService with that action, the service:
                        //   1. logs "Building configuration..."
                        //   2. mCommandHandler.post(startOpenVPN)
                        //   3. returns START_STICKY ← marks service as sticky
                        //   4. async startOpenVPN → fetchVPNProfile → no profile
                        //      → bail → stopSelf
                        // The service eventually stops, but between #1 and #4
                        // (~10-30ms) it occupies the system VPN slot in
                        // "Assuming always on" limbo. If our WG/AWG GoBackend$
                        // VpnService brings itself up during that window
                        // (typical 100-200ms after teardown), Android sees two
                        // VpnServices in the app and destroys ours as the
                        // "redundant" one. Coroutines die with
                        // JobCancellationException, user sees
                        // "Connection Failed Job Cancelled".
                        //
                        // OpenVpnTunnel.disconnect() already does the right
                        // teardown — AIDL stopVPN(false), wait for
                        // LEVEL_NOTCONNECTED state callback, explicit
                        // stopService() at the end. No further intent needed.
                    }
                } catch (e: Exception) {
                    PrivycsLogger.e(TAG, "Error during disconnect", e)
                }
            } // tunnelMutex.withLock

            val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
            // Capture KS state BEFORE clearing manager status. We
            // cannot rely on `manager.status.value.connected` here
            // because VpnServiceManager.disconnect() already wipes
            // _status to VpnStatus() optimistically (line 168) BEFORE
            // ACTION_DISCONNECT reaches this service - by the time we
            // read it here, status.connected is already false even if
            // the tunnel was running.
            //
            // KillSwitchManager.isArmed() is the authoritative truth:
            // it returns true iff state is ARMED or SINKHOLE, and
            // arm() only fires after a status push with connected=true,
            // so an ARMED state is a reliable proxy for "this session
            // had a working tunnel". User-initiated disconnects with
            // KS enabled leave state ARMED (ConnectCoordinator skips
            // disarm when KS is on), so we see ARMED here exactly in
            // the case we want to engage the sinkhole.
            val ksWasArmed = com.privycs.vpn.util.KillSwitchManager.isArmed()
            manager.updateStatus(VpnStatus())

            // Explicit lifecycle signal to the coordinator that the
            // teardown is done. Without this the coordinator stayed in
            // Disconnecting forever because updateStatus's connected-
            // to-disconnected transition doesn't fire when _status was
            // already wiped optimistically by VpnServiceManager.disconnect
            // before the service even got here.
            com.privycs.vpn.util.ConnectCoordinator.markDisconnected()

            connectStartTime = 0L
            sendWidgetUpdate(connected = false)

            // Industry-standard hardcore Kill Switch: if KS is enabled
            // AND we just disconnected from a working tunnel AND the
            // OS-level Always-On VPN is NOT taking over, force the
            // sinkhole so traffic stays blocked. The natural state-flow
            // path (engageSinkhole via updateStatus's connected->
            // disconnected transition) is racy on some Android versions
            // and was observed not engaging in user testing. Explicit
            // forceSinkhole here closes that race - the state-flow
            // observer then establishes the sinkhole tun fd before we
            // even get to the delay() check below.
            try {
                val ksEnabled = PrivycsApp.instance.settingsRepository
                    .getSettingsBlocking().killSwitchEnabled
                val alwaysOn = com.privycs.vpn.util.AlwaysOnDetector.detected.value
                if (ksEnabled && ksWasArmed && !alwaysOn) {
                    PrivycsLogger.i(TAG, "handleDisconnect: KS enabled + KS was armed → forceSinkhole")
                    com.privycs.vpn.util.KillSwitchManager.forceSinkhole(
                        "manual disconnect with KS armed",
                    )
                }
            } catch (e: Exception) {
                PrivycsLogger.w(TAG, "handleDisconnect: KS sinkhole engage failed: ${e.message}")
            }

            // Critical: if Kill Switch is enabled, the manager.updateStatus
            // call above (or our explicit forceSinkhole) just engaged the
            // sinkhole. The sinkhole tun fd is now alive and blocking
            // traffic. If we go on to call stopSelf, Android will destroy
            // this VpnService, which will close the sinkhole fd, which
            // means traffic flows direct - completely defeating the user's
            // Kill Switch intent. Stay alive in sinkhole mode.
            delay(150)
            if (com.privycs.vpn.util.KillSwitchManager.isSinkholeActive()) {
                PrivycsLogger.i(TAG, "handleDisconnect: Kill Switch sinkhole engaged - keeping service alive in block-all mode")
                updateNotification(
                    getString(R.string.vpn_notification_kill_switch_active),
                    sinkholeMode = true,
                )
                return@launch
            }

            // v0.9.14.75 — keep-monitor-alive opt-in. If the user
            // turned on "Always monitor" + has Connect-on-Demand
            // enabled, swap the connecting/connected notification
            // for the low-priority monitor one and stay foreground.
            // NetworkMonitor's tick + NetworkCallback survive Doze
            // because the service stays foreground. Without this
            // check we'd stopSelf and fall back to the 15-min
            // WorkManager backstop.
            val s = PrivycsApp.instance.settingsRepository.getSettingsBlocking()
            if (PrivycsApp.instance.networkRulesRepository.hasRules && s.keepMonitorAlive) {
                PrivycsLogger.i(TAG, "handleDisconnect: keep-monitor-alive enabled - staying foreground in monitor mode")
                currentProtocol = null
                currentConnectionId = ""
                currentConnectionName = ""
                startForeground(
                    PrivycsApp.NOTIFICATION_ID_VPN,
                    buildNotification(
                        getString(R.string.vpn_notification_monitoring),
                        monitorMode = true,
                    ),
                )
                return@launch
            }

            // v0.9.15.55: pill-switch reconnect guard. The pill-switch
            // (switchProtocol → performSwitch) does requestDisconnect
            // THEN requestConnect on the SAME PrivycsVpnService
            // instance. handleDisconnect runs first and reaches this
            // point; the unconditional stopSelf() below used to be
            // delivered LATE by Android (handleStopService) — after
            // the subsequent connect had already brought the new
            // tunnel up — destroying the live service ~30 ms after
            // "tunnel up". The new tunnel's status never reached the
            // coordinator (service dead), the UI sat on the spinner,
            // and 90 s later the Connect watchdog fired. Trace
            // signature: AmneziaTunnel "step 3/3 tunnel up" →
            // "Received handshake response" → PrivycsVpnService
            // "VPN service destroyed" within ~30 ms, only on
            // OpenVPN → WG/AWG (the OpenVPN-zombie auto-restart that
            // previously masked this is gone since v0.9.15.52).
            //
            // Same guard shape as onRevoke (search "self-revoke during
            // Connecting"): if the coordinator is already in
            // Connecting (a fresh connect — the pill-switch's second
            // half — has been accepted on this service), the service
            // MUST stay alive for that connect. Skip stopSelf; the
            // in-flight connectWireGuard/connectIpSec/connectOpenVpn
            // owns the lifecycle from here and will emit its own
            // terminal state.
            val coordState = com.privycs.vpn.util.ConnectCoordinator.state.value
            if (coordState is com.privycs.vpn.util.ConnectCoordinator.State.Connecting) {
                PrivycsLogger.i(
                    TAG,
                    "handleDisconnect: coordinator already Connecting " +
                        "(source=${coordState.source}, target=${coordState.connectionId}) " +
                        "— pill-switch reconnect in flight, NOT calling stopSelf",
                )
                return@launch
            }

            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
        }
    }

    private fun handleAlwaysOnReconnect() {
        scope.launch {
            // Detection: every null-intent wake-up is a signal. If it
            // happened within the detection window after a user-
            // initiated disconnect, we are definitely under system
            // Always-On control. AlwaysOnDetector persists the flag so
            // the UI can subsequently show the pause-or-settings sheet
            // instead of a disconnect that Always-On would just undo.
            com.privycs.vpn.util.AlwaysOnDetector.onAlwaysOnReconnectTriggered(
                this@PrivycsVpnService
            )

            // Pause honor: if the UI asked for a temporary pause (user
            // tapped "Pause 5 minutes" in the Always-On disconnect
            // sheet), we must NOT start the tunnel. stopSelf so Android
            // does not keep the foreground notification alive for a
            // service that is intentionally doing nothing.
            if (com.privycs.vpn.util.AlwaysOnDetector.isPausedNow(this@PrivycsVpnService)) {
                PrivycsLogger.i(TAG, "handleAlwaysOnReconnect: pause flag active - skipping reconnect")
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return@launch
            }

            // Coordinator gate: claim the Connecting slot with source
            // ALWAYS_ON. If some other source already holds the slot
            // (user just tapped Connect, or NetworkMonitor fired first
            // because boot raced with on-demand eval), bail out -
            // letting them finish rather than race against their
            // handleConnect is what prevents the multi-tunnel /dev/tun
            // collision.
            val connRepoEarly = PrivycsApp.instance.connectionRepository
            val activeConnEarly = connRepoEarly.getActive()
            if (activeConnEarly == null) {
                PrivycsLogger.d(TAG, "handleAlwaysOnReconnect: no active connection, stopping")
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return@launch
            }

            // v1.0.5.22: master-toggle is now the single authoritative
            // gate for every auto-reconnect path. When the user turns
            // Auto-tunnel OFF the engine must NOT bring the tunnel
            // back up — not on Always-On VPN service restart, not on
            // the periodic backstop tick, not on network-restore.
            // Only USER-source intents (Connect tap, tile, widget)
            // may start the tunnel. v1.0.5.2 had this gate conditional
            // on rules existing, which left a hole: with master OFF
            // the gate was bypassed and we still reconnected. User-
            // reported "wenn on demand off ist soll NUR manuell
            // connected werden".
            //
            // Master ON + rules present: evaluate as before.
            // Master ON + no rules: no specific gating signal — fall
            // through to the previous Always-On reconnect behaviour
            // (user explicitly enabled the engine).
            val settings = PrivycsApp.instance.settingsRepository.getSettingsBlocking()
            if (!settings.networkRulesEnabled) {
                PrivycsLogger.i(
                    TAG,
                    "handleAlwaysOnReconnect: Auto-tunnel master OFF — refusing system-restart reconnect (manual-only mode)",
                )
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return@launch
            }
            val rulesRepo = PrivycsApp.instance.networkRulesRepository
            if (rulesRepo.rules.value.isNotEmpty()) {
                val shouldConnect = NetworkMonitor.getInstance(this@PrivycsVpnService)
                    .evaluateRulesNow()
                if (!shouldConnect) {
                    PrivycsLogger.i(
                        TAG,
                        "handleAlwaysOnReconnect: network rules do not match current network, yielding",
                    )
                    stopForeground(STOP_FOREGROUND_REMOVE)
                    stopSelf()
                    return@launch
                }
            }

            val claimed = com.privycs.vpn.util.ConnectCoordinator
                .markAlwaysOnConnecting(activeConnEarly)
            if (!claimed) {
                PrivycsLogger.i(TAG, "handleAlwaysOnReconnect: coordinator slot taken, yielding")
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return@launch
            }

            val connRepo = PrivycsApp.instance.connectionRepository
            // Android wakes this VpnService with a null intent only
            // when its system-level Always-On VPN toggle is ON
            // (Settings -> Network & Internet -> VPN -> Privycs ->
            // Always-on VPN) or when the foreground service gets
            // restarted by the system after a crash. In either case,
            // "we have an active connection" is the only precondition
            // we care about - the former app-level alwaysOn toggle
            // was removed in v0.9.9.6 (Connect-on-Demand covers the
            // same need with finer-grained rule evaluation).
            val activeConn = connRepo.getActive()
            if (activeConn == null) {
                PrivycsLogger.d(TAG, "No active connection to restore, stopping")
                stopForeground(STOP_FOREGROUND_REMOVE)
                stopSelf()
                return@launch
            }

            val config = activeConn.getActiveConfig()
            if (config != null) {
                handleConnect(
                    activeConn.id,
                    activeConn.activeProtocol.name,
                    config.configContent,
                    activeConn.name
                )
            }
        }
    }

    /**
     * Poll tunnel statistics and update the global VPN status.
     * Delegates to the active protocol's tunnel implementation.
     */
    private fun startStatusPolling() {
        // Cancel any previously-running poller. Without this, each
        // tunnel-bring-up entry-point (handleConnect / handleReconnect
        // / handlePoolConnect) accumulated a fresh poller goroutine
        // because scope.launch spawns unconditionally. Symptom: two+
        // "iteration=N" lines in logcat per tick from the same
        // PrivycsVpnService PID, each with its own counter (e.g.
        // iter=850 + iter=403 ticking in parallel). Result: doubled
        // status-broadcast traffic, doubled proto.Status() reads,
        // possible UI oscillation. v0.9.14.16 fix.
        statusPollingJob?.cancel()
        val previousIterLog = if (statusPollingJob != null) "(cancelling previous poller)" else "(no previous poller)"
        statusPollingJob = scope.launch {
            val manager = VpnServiceManager.getInstance(this@PrivycsVpnService)
            var iter = 0
            PrivycsLogger.d(TAG, "startStatusPolling: $previousIterLog")
            // Delay BEFORE the first status read so the tunnel has a
            // chance to transition out of DISCONNECTED. Otherwise the
            // very first iteration races the state listener and sees
            // state=DISCONNECTED, the stillTransient check fails
            // (neither CONNECTING nor CONNECTED yet), and the loop
            // breaks immediately with "Tunnel went down unexpectedly".
            // That single-iteration death left the UI stuck at uptime
            // 0 and rxBytes/txBytes frozen at the onStateChanged
            // callback's one-shot values for the rest of the session.
            delay(STATUS_POLL_INTERVAL_MS)
            val loopStart = System.currentTimeMillis()
            PrivycsLogger.i(TAG, "startStatusPolling: loop starting, scope.isActive=$isActive, currentProtocol=$currentProtocol")
            while (isActive) {
                iter++
                PrivycsLogger.i(TAG, "startStatusPolling: iteration=$iter protocol=$currentProtocol")
                val status = when (currentProtocol) {
                    VpnProtocol.WIREGUARD -> {
                        amneziaTunnel?.getStatus(currentConnectionName, currentConnectionId, VpnProtocol.WIREGUARD)
                    }
                    VpnProtocol.AMNEZIAWG -> {
                        amneziaTunnel?.getStatus(currentConnectionName, currentConnectionId, VpnProtocol.AMNEZIAWG)
                    }
                    VpnProtocol.OPENVPN -> {
                        openVpnTunnel?.getStatus(currentConnectionName, currentConnectionId)
                    }
                    VpnProtocol.IPSEC -> {
                        ipSecTunnel?.getStatus(currentConnectionName, currentConnectionId)
                    }
                    null -> null
                }

                if (status != null) {
                    manager.updateStatus(status)
                    sendWidgetUpdate(status.connected)
                    // Persist the runtime-assigned VPN inner IP back
                    // to the connection registry so the Configs screen
                    // shows it after reload, even before the next
                    // connect. WireGuard's Address is parsed from the
                    // .conf at import (always present); OpenVPN +
                    // IPSec only learn their inner IP after IKE_AUTH /
                    // TLS, so we update here once per poll cycle while
                    // the tunnel is up. Cheap no-op when address is
                    // unchanged or empty (see updateLocalAddress).
                    if (status.connected && status.localAddress.isNotBlank() &&
                        currentConnectionId.isNotBlank() && currentProtocol != null
                    ) {
                        PrivycsApp.instance.connectionRepository.updateLocalAddress(
                            currentConnectionId,
                            currentProtocol!!,
                            status.localAddress
                        )
                    }

                    if (!status.connected) {
                        // Break only on hard DISCONNECTED AND outside the
                        // initial warm-up window. The first 15 seconds are
                        // always treated as "still coming up" regardless
                        // of current state, because OpenVPN state goes
                        // DISCONNECTED -> CONNECTING -> ... -> CONNECTED
                        // and we race the state listener on every fresh
                        // poll-loop start. Without the warm-up guard we
                        // would break on iteration 1 before the state
                        // listener even fired once.
                        val warmingUp = System.currentTimeMillis() - loopStart < 15_000L
                        val stillTransient = warmingUp || when (currentProtocol) {
                            VpnProtocol.IPSEC -> {
                                val s = ipSecTunnel?.getState()
                                s == IpSecTunnel.State.CONNECTING ||
                                        s == IpSecTunnel.State.CONNECTED
                            }
                            VpnProtocol.OPENVPN -> {
                                // Symmetric to IPSec: OpenVPN spends the first
                                // ~2-5s of a fresh connect in CONNECTING while
                                // TLS handshake + PUSH_REPLY roundtrip complete.
                                // getStatus().connected is false throughout;
                                // breaking the poll loop here would freeze
                                // uptime at 0 even though the tunnel is about
                                // to go live.
                                val s = openVpnTunnel?.getState()
                                s == OpenVpnTunnel.State.CONNECTING ||
                                        s == OpenVpnTunnel.State.CONNECTED
                            }
                            else -> false
                        }
                        if (!stillTransient) {
                            PrivycsLogger.w(TAG, "Tunnel went down unexpectedly")
                            updateNotification(getString(R.string.vpn_notification_disconnected))
                            break
                        }
                    }
                }
                delay(STATUS_POLL_INTERVAL_MS)
            }
        }
    }

    private fun buildNotification(
        text: String,
        sinkholeMode: Boolean = false,
        monitorMode: Boolean = false,
        sinkholeFailed: Boolean = false,
    ): Notification {
        val openIntent = Intent(this, MainActivity::class.java)
        val pendingOpen = PendingIntent.getActivity(
            this, 0, openIntent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )

        val title = when {
            sinkholeFailed -> getString(R.string.vpn_notification_title_kill_switch_inactive)
            sinkholeMode -> getString(R.string.vpn_notification_title_kill_switch_active)
            monitorMode -> getString(R.string.vpn_notification_title_monitoring)
            else -> getString(R.string.vpn_notification_title)
        }

        val builder = NotificationCompat.Builder(this, PrivycsApp.NOTIFICATION_CHANNEL_VPN)
            .setContentTitle(title)
            .setContentText(text)
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .setContentIntent(pendingOpen)
            .setOngoing(true)
            .setSilent(true)
            .also { b ->
                // Monitor mode = lowest visible priority + min
                // importance per-notification so the user-drawer
                // listing is as quiet as the "Charging" indicator.
                // Channel is already IMPORTANCE_LOW; this trims the
                // per-notification rank further on Android 7's pre-
                // channel devices and stays harmless on newer.
                if (monitorMode) {
                    b.priority = NotificationCompat.PRIORITY_MIN
                    b.setCategory(NotificationCompat.CATEGORY_SERVICE)
                }
            }

        if (monitorMode) {
            // Monitor-mode action: "Stop monitoring" disables the
            // keep-alive setting and lets the service stop. Faster
            // than diving into Settings.
            val stopIntent = Intent(this, PrivycsVpnService::class.java).apply {
                action = ACTION_STOP_MONITOR
            }
            val pendingStop = PendingIntent.getService(
                this, 3, stopIntent,
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
            builder.addAction(
                android.R.drawable.ic_menu_close_clear_cancel,
                getString(R.string.vpn_notification_action_stop_monitoring),
                pendingStop,
            )
            return builder.build()
        }

        if (sinkholeMode) {
            // Sinkhole mode: offer a Retry Connect action that
            // attempts a fresh connect via the coordinator. The
            // normal Disconnect action is deliberately omitted -
            // the user must open the app to disarm the Kill Switch
            // (matches the user-specified UX: "nur App-öffnen +
            // manuelles Abschalten vom Kill-Switch-Toggle").
            val retryIntent = Intent(this, PrivycsVpnService::class.java).apply {
                action = ACTION_KILL_SWITCH_RETRY
            }
            val pendingRetry = PendingIntent.getService(
                this, 2, retryIntent,
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
            )
            builder.addAction(
                android.R.drawable.ic_menu_rotate,
                getString(R.string.vpn_notification_action_retry_connect),
                pendingRetry,
            )
        } else {
            val disconnectIntent = Intent(this, PrivycsVpnService::class.java).apply {
                action = ACTION_DISCONNECT
            }
            val pendingDisconnect = PendingIntent.getService(
                this, 1, disconnectIntent,
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
            )
            builder.addAction(
                android.R.drawable.ic_menu_close_clear_cancel,
                getString(R.string.action_disconnect),
                pendingDisconnect
            )
        }

        return builder.build()
    }

    private fun updateNotification(text: String, sinkholeMode: Boolean = false) {
        // While the sinkhole is active, EVERY notification update
        // must stay on the "Kill Switch Active" template, regardless
        // of what caller is pushing. Otherwise the status poll loop
        // from the still-alive tunnel plugin (reporting
        // "Connected to X") overwrites our sinkhole notification
        // within 2 seconds of engaging it and the user never sees
        // the kill-switch message.
        val killSwitchActive = com.privycs.vpn.util.KillSwitchManager.isSinkholeActive()
        // B-4: sinkhole intent active but establish() failed → the
        // Kill Switch is NOT blocking; the notification must say so
        // rather than show the reassuring "traffic blocked" text.
        val sinkholeFailed = killSwitchActive && sinkholeEstablishFailed
        val effectiveText = when {
            sinkholeFailed ->
                getString(R.string.vpn_notification_kill_switch_unprotected)
            killSwitchActive -> getString(R.string.vpn_notification_kill_switch_active)
            else -> text
        }
        val effectiveSinkhole = sinkholeMode || killSwitchActive
        val notification = buildNotification(
            effectiveText,
            sinkholeMode = effectiveSinkhole,
            sinkholeFailed = sinkholeFailed,
        )
        val manager = getSystemService(NOTIFICATION_SERVICE) as android.app.NotificationManager
        manager.notify(PrivycsApp.NOTIFICATION_ID_VPN, notification)
    }

    /**
     * Send a broadcast to update the home screen widget with current VPN status.
     */
    private fun sendWidgetUpdate(connected: Boolean) {
        val uptimeSeconds = if (connected && connectStartTime > 0) {
            (System.currentTimeMillis() - connectStartTime) / 1000
        } else {
            0L
        }

        // v0.9.15.46: wg+awg unified — variant flows from
        // currentProtocol (user-selected identity), not from which
        // backend object is non-null. The amneziaTunnel field now
        // backs BOTH protocols.
        val variant = when (currentProtocol) {
            VpnProtocol.AMNEZIAWG -> "amneziawg"
            VpnProtocol.WIREGUARD -> "wireguard"
            else -> ""
        }
        VpnWidget.sendStatusUpdate(
            context = this,
            connected = connected,
            connectionName = currentConnectionName,
            protocol = currentProtocol?.shortLabel ?: "",
            uptime = uptimeSeconds,
            variant = variant,
        )
    }
}
