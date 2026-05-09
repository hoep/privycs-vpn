package com.privycs.vpn.service

import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.util.ConnectCoordinator
import com.privycs.vpn.util.PrivycsLogger
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
import java.util.concurrent.TimeUnit

/**
 * Periodic ICMP-ping liveness check for the active tunnel.
 *
 * Closes the "tunnel up but no traffic" gap that protocols other
 * than WireGuard cannot detect on their own:
 *
 *   - WireGuard already has handshake / RX-bytes verification in
 *     PoolConnector at connect time, but no ongoing check.
 *   - OpenVPN's management-socket reports "connected" even when
 *     the underlying transport is silently dropping packets.
 *   - IPSec's CharonVpnService reports CONNECTED while the
 *     remote SA may have aged out without notifying us.
 *
 * The monitor runs only while the VpnServiceManager.status reports
 * connected=true (started/stopped by VpnServiceManager.updateStatus).
 * Pings target a known reliable address (default 1.1.1.1) every
 * 60 seconds. Three consecutive failures fire a USER-source
 * disconnect through the Coordinator; the post-disconnect
 * NetworkMonitor / pool-rotator path then drives recovery (pool
 * member rotation OR COD-driven reconnect for single connections).
 *
 * Why ICMP via subprocess instead of a Kotlin socket: Java's
 * InetAddress.isReachable does ICMP only on Linux/Android with
 * fairly opaque privilege requirements; the subprocess ping(8)
 * binary is universally available on Android and gives us a
 * reliable exit-code signal. Process spawn cost (5-10 ms) is
 * negligible at a 60s cadence.
 */
object TunnelHealthMonitor {

    private const val TAG = "TunnelHealthMonitor"

    private const val PING_TARGET_DEFAULT = "1.1.1.1"
    // v0.9.14.96: tightened ping interval 60 s → 20 s per user
    // request ("ich brauche aber schnellere action als 60s").
    // 3-fail threshold remains, so worst-case dead-tunnel detection
    // is now 60 s (3 × 20 s) instead of 3 min (3 × 60 s). Battery
    // cost is 3× more pings — each spawning ping(8) for a single
    // ICMP packet, ~5 ms CPU + ~70 bytes network — totalling a
    // few-percent increase in idle-VPN battery use. Acceptable
    // trade for 3× faster recovery from silent tunnel death.
    private const val PING_INTERVAL_MS = 20_000L
    private const val PING_TIMEOUT_S = 2L
    private const val DEAD_THRESHOLD = 3
    // Recovery grace: after firing disconnect, the next ping cycle
    // would happen 60s later anyway, but we add an extra 30s so the
    // disconnect + reconnect lifecycle has time to settle before
    // we start counting failures again on the new tunnel.
    private const val RECOVERY_GRACE_MS = 30_000L

    /**
     * Visible health state for UI consumers. Connect-Screen shows
     * a small traffic-light pill driven by this flow.
     */
    enum class State { INACTIVE, HEALTHY, DEGRADED, RECOVERING }

    private val _state = MutableStateFlow(State.INACTIVE)
    val state: StateFlow<State> = _state.asStateFlow()

    private var job: Job? = null
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    /**
     * Start monitoring. Idempotent - calling start() while already
     * running cancels the previous job and starts fresh, useful when
     * the connect target changes (member rotation, protocol switch).
     */
    fun start(target: String = PING_TARGET_DEFAULT) {
        synchronized(this) {
            job?.cancel()
            val effectiveTarget = target.ifBlank { PING_TARGET_DEFAULT }
            PrivycsLogger.i(TAG, "starting tunnel health monitor (target=$effectiveTarget)")
            // First tick is HEALTHY-by-assumption: we just started
            // because the tunnel just came up. The actual ping at
            // T+60s either confirms or flips to DEGRADED.
            _state.value = State.HEALTHY
            job = scope.launch {
                var consecutiveFailures = 0
                while (isActive) {
                    delay(PING_INTERVAL_MS)
                    val ok = runPing(effectiveTarget)
                    if (ok) {
                        if (consecutiveFailures > 0) {
                            PrivycsLogger.i(TAG, "tunnel health restored after $consecutiveFailures fails")
                        }
                        consecutiveFailures = 0
                        _state.value = State.HEALTHY
                    } else {
                        consecutiveFailures++
                        PrivycsLogger.w(
                            TAG,
                            "ping to $effectiveTarget failed ($consecutiveFailures/$DEAD_THRESHOLD)"
                        )
                        if (consecutiveFailures >= DEAD_THRESHOLD) {
                            _state.value = State.RECOVERING
                            PrivycsLogger.e(TAG, "tunnel dead - triggering recovery")
                            triggerRecovery()
                            consecutiveFailures = 0
                            // Pause counting failures during the
                            // disconnect-then-reconnect cycle so we
                            // do not stack false-positives while the
                            // new tunnel is coming up.
                            delay(RECOVERY_GRACE_MS)
                        } else {
                            _state.value = State.DEGRADED
                        }
                    }
                }
            }
        }
    }

    /**
     * Stop monitoring. Called from VpnServiceManager when the
     * status transitions to disconnected. Idempotent.
     */
    fun stop() {
        synchronized(this) {
            job?.cancel()
            job = null
            _state.value = State.INACTIVE
        }
    }

    private fun runPing(target: String): Boolean {
        return try {
            val proc = ProcessBuilder(
                "ping", "-c", "1", "-W", PING_TIMEOUT_S.toString(), target
            ).redirectErrorStream(true).start()
            // waitFor(timeout) so a stuck ping cannot hang the
            // monitor coroutine indefinitely. We give it
            // PING_TIMEOUT_S + 1 second of slack to account for
            // ping(8)'s own teardown after its internal timeout.
            val finished = proc.waitFor(PING_TIMEOUT_S + 1L, TimeUnit.SECONDS)
            if (!finished) {
                proc.destroy()
                false
            } else {
                proc.exitValue() == 0
            }
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "ping exec failed: ${e.message}")
            false
        }
    }

    private suspend fun triggerRecovery() {
        val context = PrivycsApp.instance.applicationContext
        try {
            // USER source so the disconnect respects the same
            // Coordinator gates as a manual tap. Note: it does NOT
            // stamp AlwaysOnDetector.userDisconnect — that only
            // fires from VpnServiceManager.disconnect() — so the
            // post-disconnect 30s manual-cooldown does not block
            // our follow-up reconnect.
            ConnectCoordinator.requestDisconnect(
                context,
                ConnectCoordinator.IntentSource.USER,
            )
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "recovery requestDisconnect failed: ${e.message}")
            return
        }

        // Settle delay: let the disconnect path finish before we
        // hand a new connect intent to the Coordinator. 2 s is
        // empirically enough for the service teardown + state
        // transition back to Idle.
        delay(2_000L)

        // Drive the reconnect ourselves. The pre-existing path
        // assumed COD or the pool rotator would re-fire, which left
        // single-connection users stranded after a health-driven
        // disconnect when COD was off. Mirror desktop's
        // app.go:connectActiveTarget — pool wins if active,
        // otherwise reconnect the active single connection.
        try {
            val poolReg = PrivycsApp.instance.poolRepository.registry.value
            val activePoolId = poolReg.activeId
            val activePool = poolReg.pools.firstOrNull { it.id == activePoolId }
            if (activePoolId.isNotEmpty() && activePool != null) {
                ConnectCoordinator.requestPoolConnect(
                    context,
                    ConnectCoordinator.IntentSource.ON_DEMAND,
                    activePoolId,
                    activePool.name,
                )
                return
            }
            val connection = PrivycsApp.instance.connectionRepository.getActive()
            if (connection == null) {
                PrivycsLogger.w(TAG, "recovery: no active connection or pool, leaving disconnected")
                return
            }

            // Multi-protocol failover: if the connection has more than one
            // protocol configured, try a DIFFERENT one before reconnecting.
            // Blindly reconnecting with the same protocol that just died
            // (per the ICMP probe) is rarely the right move — if the
            // tunnel's transport is broken, the same transport will likely
            // break again. Switch the active protocol to the next available
            // alternative; if that connect fails ConnectCoordinator's
            // failure path will surface it and the user can manually
            // re-select. If only one protocol is configured we fall through
            // to the same-protocol reconnect below (no-op failover).
            val deadProto = connection.activeProtocol
            val nextProto = connection.availableProtocols().firstOrNull { it != deadProto }
            if (nextProto != null) {
                PrivycsLogger.i(
                    TAG,
                    "recovery: failover ${deadProto.label} → ${nextProto.label} " +
                        "(available: ${connection.availableProtocols().map { it.label }})",
                )
                PrivycsApp.instance.connectionRepository.setActiveProtocol(connection.id, nextProto)
                // Refresh the connection model after the protocol swap
                // so requestConnect sees the new activeProtocol on the
                // VpnConnection it queues.
                val refreshed = PrivycsApp.instance.connectionRepository.getById(connection.id) ?: connection
                ConnectCoordinator.requestConnect(
                    context,
                    ConnectCoordinator.IntentSource.ON_DEMAND,
                    refreshed,
                )
                return
            }

            ConnectCoordinator.requestConnect(
                context,
                ConnectCoordinator.IntentSource.ON_DEMAND,
                connection,
            )
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "recovery requestConnect failed: ${e.message}")
        }
    }
}
