package com.privycs.vpn.util

import android.content.Context
import android.content.Intent
import com.privycs.vpn.data.models.VpnConnection
import com.privycs.vpn.service.PrivycsVpnService
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/**
 * Central gatekeeper that serializes ALL connect/disconnect intents
 * across the five independent auto-connect paths in the app:
 *
 *   1. USER        - ConnectScreen / Widget / Tile taps
 *   2. ON_DEMAND   - NetworkMonitor rule matches on network change
 *   3. ALWAYS_ON   - System Always-On VPN respawn (null-intent wake)
 *   4. BOOT        - BootReceiver on BOOT_COMPLETED
 *   5. AUTO_START  - PrivycsApp.onCreate reviving NetworkMonitor
 *
 * Before this class existed, each source called
 * VpnServiceManager.connect() directly with its own guard logic. The
 * guards were not coordinated - e.g., NetworkMonitor checked
 * `!isConnecting` but handleAlwaysOnReconnect never SET isConnecting
 * because it lived inside the service process and bypassed the
 * manager entirely. The resulting three-way races produced
 * multi-tunnel writes to /dev/tun (the "Failed to write packet:
 * input/output error" + keepalive storm seen in v0.9.3.10..12).
 *
 * The coordinator fixes this structurally with:
 *
 *  - A Kotlin Mutex that serializes state transitions across threads
 *    and coroutines, so no two intent sources can concurrently
 *    observe + mutate state.
 *  - A state StateFlow exposing the CURRENT intent phase (Idle /
 *    Connecting / Connected / Disconnecting) that all sources see.
 *  - An IntentSource tag on every Connecting state so priority
 *    preemption works: USER intents can cancel automated intents
 *    in-flight, automated intents cannot cancel each other.
 *  - Gate checks baked into the accept path: system-revoke cooldown,
 *    always-on pause flag. Centralised here instead of duplicated in
 *    every caller.
 *  - A 90 s watchdog that force-resets state to Idle if a Connecting
 *    transition never reaches Connected (service crash / native
 *    tunnel hang), so stuck state can never permanently block
 *    future intents.
 *
 * Integration model: External intent sources (User/OnDemand/Boot/
 * Widget/Tile) call requestConnect() which fires the Service Intent.
 * The Service's internal Always-On respawn path calls
 * markAlwaysOnConnecting() to claim the slot without re-firing an
 * Intent (it's already in-service). The Service lifecycle calls
 * markConnected() / markDisconnected() as the native tunnel state
 * changes, keeping the coordinator state in sync with reality.
 */
object ConnectCoordinator {

    private const val TAG = "ConnectCoordinator"
    private const val WATCHDOG_TIMEOUT_MS = 90_000L
    // Disconnect is short (tunnel teardown typically <2s) so the
    // watchdog cut-off is tighter than the connect side. Belt-and-
    // suspenders in case the service was already stopped before the
    // ACTION_DISCONNECT intent arrived - without this, the
    // coordinator would hang in Disconnecting forever and block
    // subsequent connects.
    private const val DISCONNECT_WATCHDOG_TIMEOUT_MS = 5_000L

    enum class IntentSource {
        USER,
        ON_DEMAND,
        ALWAYS_ON,
        BOOT,
        WIDGET,
        TILE,
    }

    sealed class State {
        object Idle : State()
        data class Connecting(
            val sinceMs: Long,
            val source: IntentSource,
            val connectionId: String,
        ) : State()
        data class Connected(
            val sinceMs: Long,
            val connectionId: String,
        ) : State()
        data class Disconnecting(val sinceMs: Long) : State()
    }

    sealed class Result {
        object Accepted : Result()
        object AlreadyConnected : Result()
        object AlreadyIdle : Result()
        object AlreadyConnecting : Result()
        object AlreadyDisconnecting : Result()
        data class Gated(val reason: String) : Result()
        data class Error(val message: String) : Result()
    }

    private val mutex = Mutex()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)

    private val _state = MutableStateFlow<State>(State.Idle)
    val state: StateFlow<State> = _state.asStateFlow()

    private var watchdog: Job? = null

    /**
     * Internal target abstraction so the gate + state machine code
     * is shared between single-connection and pool intents. Both
     * fire intents at PrivycsVpnService but with different action +
     * payload; the Coordinator holds onto whichever target was
     * accepted so preemption fires the right re-connect intent.
     */
    private sealed class InternalTarget {
        abstract val targetId: String
        data class Single(val connection: VpnConnection) : InternalTarget() {
            override val targetId: String get() = connection.id
        }
        data class Pool(val poolId: String, val poolName: String) : InternalTarget() {
            // Pool target IDs are namespaced "pool:" so they cannot
            // collide with single-connection IDs in markConnected /
            // status broadcasts. VpnServiceManager already uses the
            // same convention for its tentative pool status.
            override val targetId: String get() = "pool:$poolId"
        }
    }

    /**
     * External-source connect request for a single VPN connection.
     * Fires the Service Intent when accepted. Callers: user taps,
     * NetworkMonitor (single-connection branch), BootReceiver,
     * Widget toggle, Tile click, post-sinkhole auto-reconnect.
     */
    suspend fun requestConnect(
        context: Context,
        source: IntentSource,
        connection: VpnConnection,
    ): Result = requestConnectInternal(context, source, InternalTarget.Single(connection))

    /**
     * External-source connect request for a Pool. Fires
     * ACTION_POOL_CONNECT at the Service when accepted. Callers:
     * user taps on a pool card, NetworkMonitor (pool branch),
     * post-pause auto-resume.
     *
     * Identical gate behaviour to requestConnect() so that Always-
     * On pause, manual pause, system-revoke cooldown and Kill
     * Switch sinkhole apply uniformly to pool intents - this is
     * what closes the COD-pool dead-end and the manual-disconnect
     * cooldown bypass on pool that existed when pool routing went
     * direct-to-Service.
     */
    suspend fun requestPoolConnect(
        context: Context,
        source: IntentSource,
        poolId: String,
        poolName: String,
    ): Result = requestConnectInternal(context, source, InternalTarget.Pool(poolId, poolName))

    private suspend fun requestConnectInternal(
        context: Context,
        source: IntentSource,
        target: InternalTarget,
    ): Result {
        return mutex.withLock {
            // Gate 0: hardcore Kill Switch lock. When the sinkhole is
            // engaged, the user has explicitly asked for an absolute
            // traffic block until they themselves toggle KS off. NO
            // connect intent is allowed to release the lock - not
            // user taps, not COD on network change, not widget/tile,
            // not boot-time auto-start, not Always-On respawn. The
            // sinkhole release path is exactly one: SettingsRepository
            // .updateKillSwitch(false) -> KillSwitchManager.disarm()
            // -> state SINKHOLE -> IDLE. Once IDLE, isSinkholeActive
            // returns false and this gate stops blocking; the
            // post-sinkhole COD reconnect inside forceTeardownAfter
            // Sinkhole then proceeds normally.
            if (KillSwitchManager.isSinkholeActive()) {
                PrivycsLogger.w(TAG, "requestConnect($source, ${target.targetId}) refused: sinkhole active - manual KS toggle off required")
                return@withLock Result.Gated("kill switch sinkhole active")
            }

            // Gate 1: system-revoke cooldown. The OS just tore our
            // service down; give the teardown time to settle before
            // firing a new connect that would collide.
            if (AlwaysOnDetector.isInSystemRevokeCooldown(context)) {
                PrivycsLogger.d(TAG, "requestConnect($source, ${target.targetId}): gated by system-revoke cooldown")
                return@withLock Result.Gated("system-revoke cooldown")
            }

            // Gate 2: always-on pause flag. User explicitly said "pause
            // VPN for N minutes" via the always-on disconnect sheet.
            // USER-source intents override the pause (user re-tapping
            // Connect signals they want it back on); everything else
            // yields.
            if (source != IntentSource.USER && AlwaysOnDetector.isPausedNow(context)) {
                PrivycsLogger.d(TAG, "requestConnect($source, ${target.targetId}): gated by always-on pause flag")
                return@withLock Result.Gated("always-on pause active")
            }

            // Gate 3: manual user-initiated pause (VpnPauseTimer).
            // Same semantics as Gate 2 - user said "leave me alone
            // for N minutes", so any non-USER reconnect attempt
            // (NetworkMonitor, widget auto-retry, etc.) yields.
            // A fresh USER tap cancels the pause and reconnects.
            if (source != IntentSource.USER && VpnPauseTimer.isPausedNow()) {
                PrivycsLogger.d(TAG, "requestConnect($source, ${target.targetId}): gated by manual pause flag")
                return@withLock Result.Gated("manual pause active")
            }

            when (val s = _state.value) {
                is State.Connected -> {
                    // v0.9.14.96: symmetric desync defence. If the
                    // Coordinator says Connected but the actual
                    // VpnServiceManager reports the tunnel is down
                    // (something tore down the service externally —
                    // OS revoke, kill via Settings → VPN, crash
                    // recovery loss), short-circuiting to
                    // AlreadyConnected here would silently absorb a
                    // legitimate connect intent and leave the user
                    // waiting forever for a tunnel that will never
                    // come back. Force a real connect instead.
                    val mgr = com.privycs.vpn.service.VpnServiceManager.getInstance(context)
                    if (!mgr.isConnected) {
                        PrivycsLogger.w(
                            TAG,
                            "requestConnect($source, ${target.targetId}): state=Connected but VpnServiceManager reports disconnected — DESYNC, forcing real connect"
                        )
                        fireConnectIntent(context, target)
                        _state.value = State.Connecting(System.currentTimeMillis(), source, target.targetId)
                        startWatchdog()
                        return@withLock Result.Accepted
                    }
                    PrivycsLogger.d(TAG, "requestConnect($source, ${target.targetId}): already connected")
                    Result.AlreadyConnected
                }
                is State.Connecting -> {
                    // Priority preemption: USER taps beat all automated
                    // sources. Otherwise, first-come-first-served.
                    if (source == IntentSource.USER && s.source != IntentSource.USER) {
                        PrivycsLogger.i(TAG, "requestConnect(USER, ${target.targetId}) preempting ${s.source}")
                        fireConnectIntent(context, target)
                        _state.value = State.Connecting(System.currentTimeMillis(), source, target.targetId)
                        startWatchdog()
                        Result.Accepted
                    } else {
                        PrivycsLogger.d(TAG, "requestConnect($source, ${target.targetId}): already connecting (owner=${s.source})")
                        Result.AlreadyConnecting
                    }
                }
                is State.Disconnecting -> {
                    // Don't queue. Let the caller retry after the
                    // disconnect settles (NetworkMonitor will re-evaluate
                    // on next network callback tick; user will tap again).
                    PrivycsLogger.d(TAG, "requestConnect($source, ${target.targetId}): disconnect in progress, reject")
                    Result.Gated("disconnect in progress")
                }
                is State.Idle -> {
                    PrivycsLogger.i(TAG, "requestConnect($source, ${target.targetId}): accepted -> Connecting")
                    fireConnectIntent(context, target)
                    _state.value = State.Connecting(System.currentTimeMillis(), source, target.targetId)
                    startWatchdog()
                    Result.Accepted
                }
            }
        }
    }

    /**
     * External-source disconnect request. Fires the Service Intent
     * when accepted.
     */
    suspend fun requestDisconnect(
        context: Context,
        source: IntentSource,
    ): Result {
        return mutex.withLock {
            when (_state.value) {
                is State.Idle -> {
                    // v0.9.14.96: defensive desync detection. The Coordinator
                    // state machine is updated by markConnected/markDisconnected
                    // calls from VpnServiceManager.updateStatus AND by the
                    // request* APIs. A spurious markDisconnected (e.g. transient
                    // status glitch during VPN-up) or a Coordinator init while
                    // VPN was already running can leave _state=Idle while the
                    // actual VpnService is alive and routing traffic. Without
                    // this guard, the on-demand disconnect path on a
                    // shouldConnect=false transition fires requestDisconnect →
                    // sees Idle → returns AlreadyIdle → tunnel STAYS UP. User
                    // sits inside a "VPN will not connect" SSID with a tunnel
                    // they expected to be torn down. v0.9.14.91 user report
                    // matched this exactly: "kein disconnect statt".
                    val mgr = com.privycs.vpn.service.VpnServiceManager.getInstance(context)
                    if (mgr.isConnected) {
                        PrivycsLogger.w(
                            TAG,
                            "requestDisconnect($source): state=Idle but VpnServiceManager reports connected — DESYNC, forcing real disconnect"
                        )
                        // Kill Switch handling mirrors the Connecting/Connected
                        // branch below — disarm only on user-class sources when
                        // KS is off; otherwise leave armed so the natural
                        // connected=false transition engages the sinkhole.
                        val killSwitchEnabled = com.privycs.vpn.PrivycsApp.instance
                            .settingsRepository.getSettingsBlocking().killSwitchEnabled
                        if (!killSwitchEnabled &&
                            (source == IntentSource.USER ||
                                source == IntentSource.WIDGET ||
                                source == IntentSource.TILE)
                        ) {
                            KillSwitchManager.disarm()
                        }
                        fireDisconnectIntent(context)
                        _state.value = State.Disconnecting(System.currentTimeMillis())
                        startDisconnectWatchdog()
                        return@withLock Result.Accepted
                    }
                    PrivycsLogger.d(TAG, "requestDisconnect($source): already idle")
                    Result.AlreadyIdle
                }
                is State.Disconnecting -> {
                    PrivycsLogger.d(TAG, "requestDisconnect($source): already disconnecting")
                    Result.AlreadyDisconnecting
                }
                is State.Connecting, is State.Connected -> {
                    PrivycsLogger.i(TAG, "requestDisconnect($source): accepted -> Disconnecting")
                    // Kill Switch semantics on user-initiated disconnect:
                    //
                    // - If KS setting is ON: leave state ARMED so the
                    //   subsequent connected=false transition in
                    //   VpnServiceManager.updateStatus naturally engages
                    //   the sinkhole. Industry-standard hardcore kill
                    //   switch behaviour: manual disconnect does NOT
                    //   grant unprotected internet access; user must
                    //   also disable KS (or reconnect) to release.
                    // - If KS setting is OFF: disarm as before so no
                    //   sinkhole engages after the clean user disconnect.
                    val killSwitchEnabled = com.privycs.vpn.PrivycsApp.instance
                        .settingsRepository.getSettingsBlocking().killSwitchEnabled
                    if (!killSwitchEnabled &&
                        (source == IntentSource.USER ||
                            source == IntentSource.WIDGET ||
                            source == IntentSource.TILE)
                    ) {
                        KillSwitchManager.disarm()
                    }
                    fireDisconnectIntent(context)
                    _state.value = State.Disconnecting(System.currentTimeMillis())
                    startDisconnectWatchdog()
                    Result.Accepted
                }
            }
        }
    }

    /**
     * Called from PrivycsVpnService.handleAlwaysOnReconnect BEFORE
     * handleConnect runs. Marks the slot as Connecting(ALWAYS_ON)
     * without firing an Intent (the Service is already in the
     * handleConnect path). Returns false if another source already
     * owns the slot - in that case handleAlwaysOnReconnect should
     * bail out rather than race.
     */
    suspend fun markAlwaysOnConnecting(connection: VpnConnection): Boolean {
        return mutex.withLock {
            // Hardcore Kill Switch lock applies to Always-On too.
            // Always-On bypasses requestConnect (the service was
            // respawned by the OS with a null intent and reaches
            // handleAlwaysOnReconnect directly), so the gate has to
            // be repeated here. Returning false makes
            // handleAlwaysOnReconnect bail out cleanly and the
            // sinkhole tun fd stays in place.
            if (KillSwitchManager.isSinkholeActive()) {
                PrivycsLogger.w(TAG, "markAlwaysOnConnecting refused: sinkhole active - manual KS toggle off required")
                return@withLock false
            }
            when (_state.value) {
                is State.Idle -> {
                    PrivycsLogger.i(TAG, "markAlwaysOnConnecting: claiming slot")
                    _state.value = State.Connecting(
                        System.currentTimeMillis(),
                        IntentSource.ALWAYS_ON,
                        connection.id,
                    )
                    startWatchdog()
                    true
                }
                is State.Connecting, is State.Connected -> {
                    PrivycsLogger.w(TAG, "markAlwaysOnConnecting: slot taken by ${_state.value}, bail")
                    false
                }
                is State.Disconnecting -> {
                    PrivycsLogger.w(TAG, "markAlwaysOnConnecting: disconnect in progress, bail")
                    false
                }
            }
        }
    }

    /**
     * Service lifecycle hook: tunnel is actually up. Called from
     * PrivycsVpnService after native tunnel establishes AND the
     * status poll observes connected=true.
     */
    suspend fun markConnected(connectionId: String) {
        mutex.withLock {
            PrivycsLogger.i(TAG, "markConnected: $connectionId")
            _state.value = State.Connected(System.currentTimeMillis(), connectionId)
            cancelWatchdog()
        }
    }

    /**
     * Service lifecycle hook: tunnel is down. Called from
     * PrivycsVpnService after native tunnel teardown completes OR
     * status poll observes disconnected state after a connected run.
     */
    suspend fun markDisconnected() {
        mutex.withLock {
            PrivycsLogger.i(TAG, "markDisconnected")
            _state.value = State.Idle
            cancelWatchdog()
        }
    }

    /** Read the current intent source of a Connecting state, if any. */
    fun currentConnectingSource(): IntentSource? {
        return (_state.value as? State.Connecting)?.source
    }

    /** True iff any connect/disconnect transition is in flight. */
    fun isBusy(): Boolean {
        return _state.value is State.Connecting || _state.value is State.Disconnecting
    }

    /** True iff the tunnel is reported connected. */
    fun isConnected(): Boolean {
        return _state.value is State.Connected
    }

    private fun fireConnectIntent(context: Context, target: InternalTarget) {
        when (target) {
            is InternalTarget.Single -> fireSingleConnectIntent(context, target.connection)
            is InternalTarget.Pool -> firePoolConnectIntent(context, target.poolId)
        }
    }

    private fun fireSingleConnectIntent(context: Context, connection: VpnConnection) {
        val config = connection.getActiveConfig() ?: run {
            PrivycsLogger.w(TAG, "fireConnectIntent: no active config for ${connection.id}")
            return
        }
        val intent = Intent(context, PrivycsVpnService::class.java).apply {
            action = PrivycsVpnService.ACTION_CONNECT
            putExtra(PrivycsVpnService.EXTRA_CONNECTION_ID, connection.id)
            putExtra(PrivycsVpnService.EXTRA_PROTOCOL, connection.activeProtocol.name)
            putExtra(PrivycsVpnService.EXTRA_CONFIG_CONTENT, config.configContent)
            putExtra(PrivycsVpnService.EXTRA_CONNECTION_NAME, connection.name)
        }
        try {
            context.startForegroundService(intent)
        } catch (e: Exception) {
            PrivycsLogger.e(TAG, "fireSingleConnectIntent failed: ${e.message}")
        }
    }

    private fun firePoolConnectIntent(context: Context, poolId: String) {
        // PoolPicker / PoolConnector own the actual member-pick +
        // tunnel setup once the service receives this intent. We
        // only need to hand off the pool id; the service-side
        // ACTION_POOL_CONNECT handler reads pool state from
        // PoolRepository to find the active pool config.
        val intent = Intent(context, PrivycsVpnService::class.java).apply {
            action = PrivycsVpnService.ACTION_POOL_CONNECT
            putExtra(PrivycsVpnService.EXTRA_POOL_ID, poolId)
        }
        try {
            context.startForegroundService(intent)
        } catch (e: Exception) {
            PrivycsLogger.e(TAG, "firePoolConnectIntent failed: ${e.message}")
        }
    }

    private fun fireDisconnectIntent(context: Context) {
        val intent = Intent(context, PrivycsVpnService::class.java).apply {
            action = PrivycsVpnService.ACTION_DISCONNECT
        }
        try {
            context.startService(intent)
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "fireDisconnectIntent failed: ${e.message}")
        }
    }

    /**
     * Force-reset state to Idle after WATCHDOG_TIMEOUT_MS if we're
     * still in Connecting. Covers the case where the service crashes
     * or the native tunnel never reports success, leaving the
     * coordinator locked out of all future intents.
     */
    private fun startWatchdog() {
        cancelWatchdog()
        watchdog = scope.launch {
            delay(WATCHDOG_TIMEOUT_MS)
            mutex.withLock {
                if (_state.value is State.Connecting) {
                    PrivycsLogger.w(TAG, "Connect watchdog fired: stuck 90s, reset to Idle")
                    _state.value = State.Idle
                }
            }
        }
    }

    /**
     * Force-reset to Idle after DISCONNECT_WATCHDOG_TIMEOUT_MS if we
     * are still in Disconnecting. Covers the case where the service
     * was already stopped when ACTION_DISCONNECT fired and the
     * intent vanished into the void, or where handleDisconnect
     * crashes before calling markDisconnected(). Without this, the
     * coordinator would stay in Disconnecting forever and block
     * every subsequent connect attempt.
     */
    private fun startDisconnectWatchdog() {
        cancelWatchdog()
        watchdog = scope.launch {
            delay(DISCONNECT_WATCHDOG_TIMEOUT_MS)
            mutex.withLock {
                if (_state.value is State.Disconnecting) {
                    PrivycsLogger.w(TAG, "Disconnect watchdog fired: stuck 5s, reset to Idle")
                    _state.value = State.Idle
                }
            }
        }
    }

    private fun cancelWatchdog() {
        watchdog?.cancel()
        watchdog = null
    }
}
