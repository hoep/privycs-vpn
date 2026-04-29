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
         * Suspending: caller can await. Implementer is responsible
         * for dispatching to Dispatchers.IO if blocking work is
         * involved.
         */
        suspend fun bringUp(member: PoolMember): Boolean

        /**
         * Tears down the current tunnel synchronously.
         *
         * Returns true if the tunnel was successfully torn down
         * (or was already down), false if the teardown failed in
         * a way that leaves the OS-level state ambiguous - in
         * which case the caller must NOT proceed to a new bringUp
         * because that would corrupt routing state.
         */
        suspend fun bringDown(): Boolean

        /**
         * Reads received-bytes from the active WireGuard tunnel.
         * Returns 0 if no tunnel up or non-WireGuard. Implementer
         * MUST dispatch to a worker (Dispatchers.IO or its own
         * thread pool) - blocking work in this method on the
         * coroutine that called us would defeat the polling loop.
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
        // GUARD: Kill Switch sinkhole. If KS is engaged, every
        // bringUp will hit handleConnect's GUARD 0 and refuse.
        // Without this guard the retry loop would mark every member
        // unreachable in turn, leaving the pool in an "all members
        // dead" state that persists past the user disabling KS.
        // Bail without touching member-state so re-enabling pool
        // after KS disable starts from a clean slate.
        if (com.privycs.vpn.util.KillSwitchManager.isSinkholeActive()) {
            Log.w(tag, "Kill Switch sinkhole active - aborting pool connect (no member marks)")
            return null
        }

        val userCountry = tunnelOps.userCountry()

        // Tear down current tunnel ONCE before the retry loop.
        // Layer-A retries pick a different member each iteration;
        // they don't disconnect/reconnect more than necessary.
        //
        // CRITICAL: if teardown fails the OS-level state is
        // ambiguous - bringing up a new tunnel would corrupt
        // routing. Bail and let the user / next rotation tick
        // retry. Mirrors desktop's "refusing to overwrite config"
        // guard.
        if (!tunnelOps.bringDown()) {
            Log.e(tag, "bringDown failed - aborting pool rotation to avoid corrupt OS state")
            return null
        }

        // Soft de-prioritisation: members that failed within the
        // last RECENT_FAILURE_WINDOW_MS are excluded from the
        // initial pick so a freshly-flapping member doesn't get
        // re-tried immediately on every cycle. If exclusion
        // empties the candidate pool we relax it inside the loop
        // and pick from the full eligible set as a fallback.
        val recentlyFailedIds = pools.state
            .membersWithRecentFailure(pool.id, RECENT_FAILURE_WINDOW_MS)
        val attempted = mutableListOf<String>()
        if (recentlyFailedIds.isNotEmpty()) {
            attempted.addAll(recentlyFailedIds)
        }
        val lastActiveMember = pools.activeMemberId(pool.id)
        var lastErr: String? = null
        var relaxedRecentExclusion = false

        for (attempt in 0 until MAX_CONNECT_ATTEMPTS) {
            // First-attempt preference: honor the pre-warm pick if
            // valid. Subsequent attempts pick fresh excluding all
            // previously-tried members.
            //
            // Three guards on the pre-warmed pick:
            //   1. The candidate still exists in the pool (not
            //      deleted between pre-warm and rotate).
            //   2. The candidate is not currently flagged
            //      unreachable (some other path may have failed
            //      with it in the meantime).
            //   3. The candidate was not recently failed (within
            //      RECENT_FAILURE_WINDOW_MS). Pre-warm did a
            //      DNS-only probe at T-60 which can't catch a
            //      "DNS works but WG handshake broken" member.
            //      If such a member failed the actual rotation
            //      a few minutes ago, the pre-warm window may
            //      not have elapsed yet, and honoring pendingId
            //      would re-pick it. Falling through to the
            //      policy picker (which respects recently-failed
            //      exclusion) avoids that re-trial loop.
            var member: PoolMember? = null
            if (attempt == 0) {
                val pendingId = pools.pendingMemberId(pool.id)
                if (pendingId.isNotEmpty()) {
                    val candidate = pool.memberById(pendingId)
                    if (candidate != null &&
                        !pools.isMemberUnreachable(pool.id, candidate.id) &&
                        candidate.id !in recentlyFailedIds
                    ) {
                        member = candidate
                    } else if (candidate != null) {
                        Log.d(tag, "pre-warm pick ${candidate.name} stale " +
                                "(unreachable=${pools.isMemberUnreachable(pool.id, candidate.id)}, " +
                                "recentlyFailed=${candidate.id in recentlyFailedIds}) - falling through to policy pick")
                    }
                }
            }
            if (member == null) {
                member = PoolPicker.pickExcluding(pools, pool, userCountry, lastActiveMember, attempted)
            }
            if (member == null) {
                // No candidate left. If we still have the recent-
                // failure exclusion in effect, drop it once and
                // retry — better to retry a recently-failed member
                // than fail the whole rotation.
                if (!relaxedRecentExclusion && recentlyFailedIds.isNotEmpty()) {
                    Log.i(tag, "no fresh candidate - relaxing recent-failure exclusion")
                    attempted.removeAll(recentlyFailedIds.toSet())
                    relaxedRecentExclusion = true
                    member = PoolPicker.pickExcluding(pools, pool, userCountry, lastActiveMember, attempted)
                }
                if (member == null) {
                    Log.w(tag, "no candidate after $attempt attempt(s)")
                    break
                }
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
                    // Best-effort teardown before retry. If
                    // teardown fails here we bail too - the next
                    // bringUp would be on top of a half-up tunnel.
                    if (!tunnelOps.bringDown()) {
                        Log.e(tag, "bringDown after peer-silent failed - aborting rotation cycle")
                        return null
                    }
                    continue
                }
            }

            Log.i(tag, "connected member=${member.name}")
            return member
        }

        Log.e(tag, "all ${attempted.size} attempts failed (last: $lastErr)")
        // CRITICAL: clear the pre-warm cache. If a pre-warmed
        // member was the source of the failure, leaving it in
        // pendingMemberId means every subsequent rotation tick
        // tries that same dead member first (line above where
        // attempt==0 reads pendingId). The pool stays effectively
        // blackout-ed for 30 minutes (TTL) until the unreachable
        // flag clears. Wiping pending forces a fresh policy pick.
        pools.setPendingMember(pool.id, "")
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
        // Top-level timeout: each probeMember has a 2s DNS timeout
        // and we attempt up to 3 members, so worst case is 6s. Add
        // safety margin for slow geo-resolver lookups, then bound
        // the whole call so a stalled DNS server can't keep the
        // service scope busy after the user disconnected.
        try {
            kotlinx.coroutines.withTimeout(PRE_WARM_TOTAL_TIMEOUT_MS) {
                preWarmInternal(pool)
            }
        } catch (e: kotlinx.coroutines.TimeoutCancellationException) {
            Log.w(tag, "preWarm exceeded ${PRE_WARM_TOTAL_TIMEOUT_MS}ms - giving up this cycle")
        }
    }

    private suspend fun preWarmInternal(pool: Pool) {
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
        // 12s budget for the whole pre-warm: 3 attempts x 2s DNS
        // = 6s nominal, + slack for slow MMDB lookups and DNS
        // retries on flaky networks.
        private const val PRE_WARM_TOTAL_TIMEOUT_MS = 12_000L
        // Members that failed within the last 5 minutes are
        // soft-deprioritised. After this window they're treated
        // as fresh again.
        private const val RECENT_FAILURE_WINDOW_MS = 5 * 60 * 1000L
    }
}
