package com.privycs.vpn.service

import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.util.ConnectCoordinator
import com.privycs.vpn.util.PrivycsLogger
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
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
    private const val PING_INTERVAL_MS = 60_000L
    private const val PING_TIMEOUT_S = 2L
    private const val DEAD_THRESHOLD = 3
    // Recovery grace: after firing disconnect, the next ping cycle
    // would happen 60s later anyway, but we add an extra 30s so the
    // disconnect + reconnect lifecycle has time to settle before
    // we start counting failures again on the new tunnel.
    private const val RECOVERY_GRACE_MS = 30_000L

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
                    } else {
                        consecutiveFailures++
                        PrivycsLogger.w(
                            TAG,
                            "ping to $effectiveTarget failed ($consecutiveFailures/$DEAD_THRESHOLD)"
                        )
                        if (consecutiveFailures >= DEAD_THRESHOLD) {
                            PrivycsLogger.e(TAG, "tunnel dead - triggering recovery")
                            triggerRecovery()
                            consecutiveFailures = 0
                            // Pause counting failures during the
                            // disconnect-then-reconnect cycle so we
                            // do not stack false-positives while the
                            // new tunnel is coming up.
                            delay(RECOVERY_GRACE_MS)
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
        try {
            val context = PrivycsApp.instance.applicationContext
            // USER source so the disconnect respects the same
            // Coordinator gates as a manual tap, which keeps the
            // existing Always-On / pause / sinkhole semantics
            // intact even on automated recovery.
            ConnectCoordinator.requestDisconnect(
                context,
                ConnectCoordinator.IntentSource.USER,
            )
        } catch (e: Exception) {
            PrivycsLogger.w(TAG, "recovery requestDisconnect failed: ${e.message}")
        }
    }
}
