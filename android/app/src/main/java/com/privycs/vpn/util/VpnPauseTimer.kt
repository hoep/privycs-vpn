package com.privycs.vpn.util

import android.content.Context
import android.util.Log
import com.privycs.vpn.service.NetworkMonitor
import com.privycs.vpn.service.VpnServiceManager
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/**
 * Manual "pause VPN for N minutes" with automatic reconnect at
 * expiry (when Connect-on-Demand rules still say the tunnel
 * should be up).
 *
 * How it behaves:
 *
 * 1. `pauseFor(ctx, minutes)` disconnects the tunnel via
 *    ConnectCoordinator and starts a coroutine that watches the
 *    clock. The expiry timestamp is published as a StateFlow so
 *    the UI can show a live countdown.
 * 2. While the pause is active, ConnectCoordinator's
 *    non-USER-source gate blocks automatic reconnects (same
 *    mechanism as AlwaysOnDetector's pause). USER-source taps
 *    (app "Resume now" or a widget tap that is interpreted as
 *    resume) implicitly cancel the pause and reconnect normally.
 * 3. When the timer expires, the coroutine re-evaluates COD rules
 *    and issues an ON_DEMAND reconnect if shouldConnect is true.
 *    The pause flag is cleared before the reconnect call so the
 *    coordinator accepts it.
 *
 * Lifecycle: pause state lives in the app process. If the process
 * is killed mid-pause, the VPN stays disconnected and the next
 * reconnect is up to NetworkMonitor's callbacks or the user. This
 * is acceptable because the user initiated the pause - losing
 * auto-reconnect after an OS-induced process kill is a rare edge
 * case not worth persisting across reboots.
 */
object VpnPauseTimer {

    private const val TAG = "VpnPauseTimer"

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    private val _pauseUntilEpochMs = MutableStateFlow(0L)

    /**
     * Timestamp (epoch ms) at which the current pause expires.
     * `0L` means no pause is active. UI observes this to render
     * the countdown; non-UI callers should prefer [isPausedNow]
     * for a boolean check.
     */
    val pauseUntilEpochMs: StateFlow<Long> = _pauseUntilEpochMs.asStateFlow()

    private var job: Job? = null

    /** Boolean snapshot: is a pause currently active? */
    fun isPausedNow(): Boolean =
        _pauseUntilEpochMs.value > System.currentTimeMillis()

    /**
     * Remaining pause duration in seconds. Returns `0` when no
     * pause is active. Caller rounds up/down for display as needed.
     */
    fun remainingSeconds(): Int {
        val until = _pauseUntilEpochMs.value
        if (until == 0L) return 0
        return ((until - System.currentTimeMillis()) / 1000L).toInt().coerceAtLeast(0)
    }

    /**
     * Disconnect the tunnel and schedule an auto-reconnect check
     * after [minutes] minutes. Overwrites any existing pause.
     *
     * [context] may be any Android context; the application
     * context is used for subsequent service lookups.
     */
    fun pauseFor(context: Context, minutes: Int) {
        val appContext = context.applicationContext
        val durationMs = minutes.coerceAtLeast(1) * 60_000L
        val untilMs = System.currentTimeMillis() + durationMs
        _pauseUntilEpochMs.value = untilMs

        job?.cancel()
        job = scope.launch {
            try {
                ConnectCoordinator.requestDisconnect(
                    appContext,
                    ConnectCoordinator.IntentSource.WIDGET,
                )

                // Poll every 250ms so a cancel() takes effect
                // within a quarter-second instead of waiting out
                // the full pause duration.
                while (isActive && System.currentTimeMillis() < untilMs) {
                    delay(250)
                }

                if (!isActive) return@launch

                // Timer ran to completion - clear flag and run
                // COD reconnect check.
                _pauseUntilEpochMs.value = 0L

                val settings = com.privycs.vpn.PrivycsApp.instance
                    .settingsRepository.getSettingsBlocking()
                if (!settings.connectOnDemand.enabled) return@launch

                val nm = NetworkMonitor.getInstance(appContext)
                nm.reevaluate()
                val ns = nm.networkState.value
                if (!ns.shouldConnect) return@launch

                val manager = VpnServiceManager.getInstance(appContext)
                if (manager.isConnected) return@launch

                val conn = com.privycs.vpn.PrivycsApp.instance
                    .connectionRepository.getActive() ?: return@launch

                Log.i(TAG, "Pause expired, on-demand reconnect (${ns.ruleMatch})")
                ConnectCoordinator.requestConnect(
                    appContext,
                    ConnectCoordinator.IntentSource.ON_DEMAND,
                    conn,
                )
            } catch (e: Exception) {
                Log.w(TAG, "Pause timer failed", e)
            } finally {
                // Safety: if we exit for any reason past the
                // expiry time, clear the flag so the UI doesn't
                // show a stuck countdown.
                if (_pauseUntilEpochMs.value != 0L &&
                    System.currentTimeMillis() >= _pauseUntilEpochMs.value
                ) {
                    _pauseUntilEpochMs.value = 0L
                }
            }
        }
    }

    /**
     * Abort the active pause. Does NOT automatically reconnect -
     * caller decides whether to issue a connect request next.
     * Safe to call when no pause is active (no-op).
     */
    fun cancel() {
        job?.cancel()
        job = null
        _pauseUntilEpochMs.value = 0L
    }
}
