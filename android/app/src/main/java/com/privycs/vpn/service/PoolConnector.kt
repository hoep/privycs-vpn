package com.privycs.vpn.service

import android.content.Context
import android.util.Log
import com.privycs.vpn.data.PoolHealthTrigger
import com.privycs.vpn.data.PoolPicker
import com.privycs.vpn.data.PoolProbe
import com.privycs.vpn.data.PoolRepository
import com.privycs.vpn.data.models.Pool
import com.privycs.vpn.data.models.PoolMember
import com.privycs.vpn.data.models.VpnProtocol
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext

/**
 * Pool-aware connection orchestrator. The companion to
 * PrivycsVpnService — handles "pick a member, connect, verify"
 * with the same Layer-A/B/C resilience as the desktop client.
 *
 * Lifecycle: lives inside PrivycsVpnService's coroutine scope.
 * Constructed once when the service starts, holds no per-call
 * state of its own.
 *
 * Layer mapping (mirrors desktop):
 *   - Layer A: connect-retry loop on Up() failure (3 attempts).
 *   - Layer B: post-Up packet-trigger + bytes_rx poll (5s timeout)
 *              for WireGuard members. Marks member unreachable if
 *              the peer doesn't respond.
 *   - Layer C: pre-warm DNS probe — picks the next member 60s
 *              ahead of rotation, runs DNS+probe, persists as
 *              pendingMemberId.
 */
class PoolConnector(
    private val context: Context,
    private val pools: PoolRepository,
    private val tunnelOps: PoolTunnelOps
) {

    /**
     * Bridge interface to whatever owns the actual VPN tunnel
     * (PrivycsVpnService). Keeps PoolConnector decoupled from
     * VpnService internals.
     */
    interface PoolTunnelOps {
        /**
         * Brings up a tunnel for the given member. Returns true
         * on success (kernel device installed), false on failure.
         * Suspending: caller can await.
         */
        suspend fun bringUp(member: PoolMember): Boolean

        /**
         * Tears down the current tunnel synchronously. Returns
         * after the OS device has been unwired.
         */
        suspend fun bringDown()

        /**
         * Reads received-bytes from the active WireGuard tunnel.
         * Returns 0 if no tunnel up or non-WireGuard.
         */
        suspend fun bytesReceived(): Long

        /** Detected user country for Geo-Nearest / home-region rotation. */
        suspend fun userCountry(): String
    }

    private val tag = "PoolConnector"

    /**
     * Picks a member via policy and connects, with full
     * Layer-A+B retry loop. Returns the connected member or null
     * if all attempts failed.
     */
    suspend fun pickAndConnect(pool: Pool): PoolMember? {
        val userCountry = tunnelOps.userCountry()

        // Tear down current tunnel ONCE before the retry loop.
        // Layer-A retries pick a different member each iteration;
        // they don't disconnect/reconnect more than necessary.
        tunnelOps.bringDown()

        val attempted = mutableListOf<String>()
        val lastActiveMember = pools.activeMemberId(pool.id)
        var lastErr: String? = null

        for (attempt in 0 until MAX_CONNECT_ATTEMPTS) {
            // First-attempt preference: honor the pre-warm pick if
            // valid. Subsequent attempts pick fresh excluding all
            // previously-tried members.
            var member: PoolMember? = null
            if (attempt == 0) {
                val pendingId = pools.pendingMemberId(pool.id)
                if (pendingId.isNotEmpty()) {
                    val candidate = pool.memberById(pendingId)
                    if (candidate != null && !pools.isMemberUnreachable(pool.id, candidate.id)) {
                        member = candidate
                    }
                }
            }
            if (member == null) {
                member = PoolPicker.pickExcluding(pools, pool, userCountry, lastActiveMember, attempted)
            }
            if (member == null) {
                Log.w(tag, "no candidate after $attempt attempt(s)")
                break
            }
            attempted.add(member.id)

            // Persist the pick (and clear pending) up-front so the
            // pre-warm path doesn't re-read stale ActiveMemberID.
            pools.setActiveMember(pool.id, member.id)
            pools.setPendingMember(pool.id, "")

            Log.i(tag, "attempt ${attempt + 1}/$MAX_CONNECT_ATTEMPTS member=${member.name}")

            val ok = tunnelOps.bringUp(member)
            if (!ok) {
                lastErr = "Up() failed"
                pools.markMemberUnreachable(pool.id, member.id, lastErr)
                continue
            }

            // Layer-B: WireGuard packet-trigger + bytes_rx poll.
            if (member.config.protocol == VpnProtocol.WIREGUARD) {
                if (!verifyWireGuardPeerHealth(member)) {
                    lastErr = "no rx within ${PEER_HEALTH_TIMEOUT_MS}ms"
                    pools.markMemberUnreachable(pool.id, member.id, lastErr)
                    tunnelOps.bringDown()
                    continue
                }
            }

            Log.i(tag, "connected member=${member.name}")
            return member
        }

        Log.e(tag, "all ${attempted.size} attempts failed (last: $lastErr)")
        return null
    }

    /**
     * Pre-warm step — fired by PoolAlarmReceiver 60s before rotation.
     * Picks the next member, runs DNS-only probe, persists as
     * pendingMemberId so the rotation tick picks it instantly.
     *
     * Multiple probe attempts: if the probe fails on the first
     * pick, mark unreachable and try another. Up to 3 attempts.
     */
    suspend fun preWarm(pool: Pool) {
        val userCountry = tunnelOps.userCountry()
        val attempted = mutableListOf<String>()
        val lastActiveMember = pools.activeMemberId(pool.id)
        var picked: PoolMember? = null

        for (i in 0 until MAX_PRE_WARM_ATTEMPTS) {
            val candidate = PoolPicker.pickExcluding(pools, pool, userCountry, lastActiveMember, attempted)
                ?: break
            attempted.add(candidate.id)
            val err = PoolProbe.probeMember(candidate)
            if (err != null) {
                Log.i(tag, "pre-warm probe ${candidate.name} failed: $err")
                pools.markMemberUnreachable(pool.id, candidate.id, err)
                continue
            }
            picked = candidate
            break
        }

        if (picked != null) {
            pools.setPendingMember(pool.id, picked.id)
            Log.i(tag, "pre-warmed pool=${pool.name} → ${picked.name}")
        } else {
            Log.w(tag, "pre-warm: no probe-passing member after ${attempted.size} tries")
        }
    }

    /**
     * Layer-B-V2: trigger a packet through the new tunnel and poll
     * bytes_received until non-zero. Returns true if the peer
     * responded within the timeout.
     */
    private suspend fun verifyWireGuardPeerHealth(member: PoolMember): Boolean = withContext(Dispatchers.IO) {
        val target = PoolHealthTrigger.parseAllowedIPsTarget(member.config.configContent)
        if (target == null) {
            Log.d(tag, "no IPv4 trigger target in AllowedIPs - trusting Up()")
            return@withContext true
        }
        PoolHealthTrigger.triggerTraffic(target)

        // Initial wait for the round-trip; healthy peers respond
        // within 50-300ms on a normal connection.
        delay(200)

        val deadline = System.currentTimeMillis() + PEER_HEALTH_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            val rx = tunnelOps.bytesReceived()
            if (rx > 0) return@withContext true
            delay(500)
        }
        false
    }

    companion object {
        private const val MAX_CONNECT_ATTEMPTS = 3
        private const val MAX_PRE_WARM_ATTEMPTS = 3
        private const val PEER_HEALTH_TIMEOUT_MS = 5_000L
    }
}
