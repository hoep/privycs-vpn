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
     * External-source connect request. Fires the Service Intent when
     * accepted. Callers: user taps, NetworkMonitor, BootReceiver,
     * Widget toggle, Tile click.
     */
    suspend fun requestConnect(
        context: Context,
        source: IntentSource,
        connection: VpnConnection,
    ): Result {
        return mutex.withLock {
            // Gate 1: system-revoke cooldown. The OS just tore our
            // service down; give the teardown time to settle before
            // firing a new connect that would collide.
            if (AlwaysOnDetector.isInSystemRevokeCooldown(context)) {
                PrivycsLogger.d(TAG, "requestConnect($source): gated by system-revoke cooldown")
                return@withLock Result.Gated("system-revoke cooldown")
            }

            // Gate 2: always-on pause flag. User explicitly said "pause
            // VPN for N minutes" via the always-on disconnect sheet.
            // USER-source intents override the pause (user re-tapping
            // Connect signals they want it back on); everything else
            // yields.
            if (source != IntentSource.USER && AlwaysOnDetector.isPausedNow(context)) {
                PrivycsLogger.d(TAG, "requestConnect($source): gated by always-on pause flag")
                return@withLock Result.Gated("always-on pause active")
            }

            // Gate 3: manual user-initiated pause (VpnPauseTimer).
            // Same semantics as Gate 2 - user said "leave me alone
            // for N minutes", so any non-USER reconnect attempt
            // (NetworkMonitor, widget auto-retry, etc.) yields.
            // A fresh USER tap cancels the pause and reconnects.
            if (source != IntentSource.USER && VpnPauseTimer.isPausedNow()) {
                PrivycsLogger.d(TAG, "requestConnect($source): gated by manual pause flag")
                return@withLock Result.Gated("manual pause active")
            }

            when (val s = _state.value) {
                is State.Connected -> {
                    PrivycsLogger.d(TAG, "requestConnect($source): already connected")
                    Result.AlreadyConnected
                }
                is State.Connecting -> {
                    // Priority preemption: USER taps beat all automated
                    // sources. Otherwise, first-come-first-served.
                    if (source == IntentSource.USER && s.source != IntentSource.USER) {
                        PrivycsLogger.i(TAG, "requestConnect(USER) preempting ${s.source}")
                        fireConnectIntent(context, connection)
                        _state.value = State.Connecting(System.currentTimeMillis(), source, connection.id)
                        startWatchdog()
                        Result.Accepted
                    } else {
                        PrivycsLogger.d(TAG, "requestConnect($source): already connecting (owner=${s.source})")
                        Result.AlreadyConnecting
                    }
                }
                is State.Disconnecting -> {
                    // Don't queue. Let the caller retry after the
                    // disconnect settles (NetworkMonitor will re-evaluate
                    // on next network callback tick; user will tap again).
                    PrivycsLogger.d(TAG, "requestConnect($source): disconnect in progress, reject")
                    Result.Gated("disconnect in progress")
                }
                is State.Idle -> {
                    PrivycsLogger.i(TAG, "requestConnect($source): accepted -> Connecting")
                    fireConnectIntent(context, connection)
                    _state.value = State.Connecting(System.currentTimeMillis(), source, connection.id)
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
                    PrivycsLogger.d(TAG, "requestDisconnect($source): already idle")
                    Result.AlreadyIdle
                }
                is State.Disconnecting -> {
                    PrivycsLogger.d(TAG, "requestDisconnect($source): already disconnecting")
                    Result.AlreadyDisconnecting
                }
                is State.Connecting, is State.Connected -> {
                    PrivycsLogger.i(TAG, "requestDisconnect($source): accepted -> Disconnecting")
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

    private fun fireConnectIntent(context: Context, connection: VpnConnection) {
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
            PrivycsLogger.e(TAG, "fireConnectIntent failed: ${e.message}")
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
