package com.privycs.vpn.data

import com.privycs.vpn.data.models.Pool
import com.privycs.vpn.data.models.PoolMember
import com.privycs.vpn.data.models.PoolPolicy
import java.security.SecureRandom

/**
 * Pool picker — Android port of pool_policy.go.
 *
 * Pure logic: takes a Pool + state info, returns the next member.
 * No side effects beyond the per-region cursor write that
 * `pickRoundRobin` performs (delegated to PoolStateRepository).
 *
 * The picker is suspend because it consults PoolStateRepository
 * for the eligible-member filter and for the round-robin cursor.
 * Pure tests can pass a stub state-repo.
 */
object PoolPicker {

    private val rng = SecureRandom()

    /**
     * Convenience entry point — same signature shape as desktop's
     * PickMember. Threads the state-repo through for unreachable
     * filtering and round-robin cursor.
     */
    suspend fun pick(
        repo: PoolRepository,
        pool: Pool,
        userCountry: String,
        lastMemberId: String
    ): PoolMember? = pickExcluding(repo, pool, userCountry, lastMemberId, emptyList())

    /**
     * Retry-loop variant. Excludes already-attempted member IDs so
     * the rotator doesn't pick the same dead one twice in one cycle.
     */
    suspend fun pickExcluding(
        repo: PoolRepository,
        pool: Pool,
        userCountry: String,
        lastMemberId: String,
        excludeIds: List<String>
    ): PoolMember? {
        var eligible = repo.eligibleMembers(pool)
        if (excludeIds.isNotEmpty()) {
            eligible = eligible.filterNot { it.id in excludeIds }
        }
        if (eligible.isEmpty()) return null

        val effectiveCountry = pool.countryOverride.ifEmpty { userCountry }

        return when (pool.policy) {
            PoolPolicy.GEO_NEAREST -> pickGeoNearest(eligible, effectiveCountry)
            PoolPolicy.RANDOM -> pickRandom(eligible)
            PoolPolicy.ROUND_ROBIN -> pickRoundRobin(repo, pool, eligible, lastMemberId, effectiveCountry)
        }
    }

    /**
     * Tier 1: exact country match.
     * Tier 2: same continent (region).
     * Tier 3: any eligible.
     */
    private fun pickGeoNearest(eligible: List<PoolMember>, userCountry: String): PoolMember? {
        if (eligible.isEmpty()) return null

        if (userCountry.isNotEmpty()) {
            val countryMatches = eligible.filter { it.country == userCountry }
            if (countryMatches.isNotEmpty()) return pickRandom(countryMatches)

            val userRegion = regionForCountry(userCountry)
            val regionMatches = eligible.filter { it.region == userRegion }
            if (regionMatches.isNotEmpty()) return pickRandom(regionMatches)
        }

        return pickRandom(eligible)
    }

    /**
     * Uniformly random pick using SecureRandom. java.util.Random
     * with a fixed seed would produce the same first-pick across
     * cold app starts; SecureRandom seeds itself from /dev/urandom.
     */
    private fun pickRandom(members: List<PoolMember>): PoolMember? {
        if (members.isEmpty()) return null
        if (members.size == 1) return members[0]
        return members[rng.nextInt(members.size)]
    }

    /**
     * Cycles through regions alphabetically, then advances through
     * region members via a persisted per-region cursor. The cursor
     * gives true round-robin within region (not random-within),
     * closing the privacy hole where the same exit IP could re-pick
     * within a few rotations.
     *
     * Edge cases handled:
     *   - Single-region pool: cursor-only rotation within the region.
     *   - lastMemberID belongs to a deleted/unreachable member:
     *     restart from user's home region or the first region.
     */
    private suspend fun pickRoundRobin(
        repo: PoolRepository,
        pool: Pool,
        eligible: List<PoolMember>,
        lastMemberId: String,
        userCountry: String
    ): PoolMember? {
        if (eligible.isEmpty()) return null

        val byRegion = eligible.groupBy { it.region.ifEmpty { "Other" } }
        val regions = byRegion.keys.sorted()
        if (regions.isEmpty()) return null

        suspend fun pickFromRegion(region: String): PoolMember? {
            val members = byRegion[region]?.sortedBy { it.id } ?: return null
            if (members.isEmpty()) return null

            // Single-member region short-circuit. With one member the
            // cursor advancement (idx+1)%1 = 0 always re-picks the
            // same member; persisting the cursor is wasted I/O. Plus
            // the user's "rotation" is degenerate anyway — there is
            // nowhere else to rotate to. Return the member without
            // touching the cursor so subsequent rotation ticks don't
            // accumulate state-flush noise.
            if (members.size == 1) {
                return members[0]
            }

            // Cursor priority: state-registry's persisted cursor →
            // lastMemberID arg if it's in this region → empty (start
            // at index 0).
            var cursor = repo.state.regionCursor(pool.id, region)
            if (cursor.isEmpty() && lastMemberId.isNotEmpty()) {
                if (members.any { it.id == lastMemberId }) {
                    cursor = lastMemberId
                }
            }
            val startIdx = if (cursor.isNotEmpty()) {
                val idx = members.indexOfFirst { it.id == cursor }
                if (idx >= 0) {
                    (idx + 1) % members.size
                } else {
                    // Cursor ID is no longer in the members list
                    // (member deleted or marked unreachable mid-cycle).
                    // Falling back to index 0 would silently break
                    // the round-robin invariant: the next picks would
                    // re-visit the early IDs out of order, leaking
                    // the same exit IP within fewer-than-N rotations.
                    //
                    // Fix: find the position where the cursor ID
                    // would have lived in the sorted-by-id list and
                    // resume from the next stable ID after it. This
                    // preserves rotation order even across member
                    // deletions and unreachable-flag changes.
                    val insertion = -members.binarySearch { it.id.compareTo(cursor) } - 1
                    insertion.coerceAtLeast(0) % members.size
                }
            } else {
                0
            }
            val picked = members[startIdx]
            repo.state.setRegionCursor(pool.id, region, picked.id)
            return picked
        }

        // First-pass region pick. May return null if the chosen
        // region's member-set ended up empty (race with eligible
        // changing under us, or the region key shifted). Fallback
        // sequence walks the regions array.
        suspend fun pickWithFallback(preferredRegion: String?): PoolMember? {
            // 1. Try the preferred region.
            if (preferredRegion != null && preferredRegion in byRegion) {
                pickFromRegion(preferredRegion)?.let { return it }
            }
            // 2. Walk the alphabetically-sorted regions in order.
            for (r in regions) {
                pickFromRegion(r)?.let { return it }
            }
            return null
        }

        // Single-region pool: cursor-rotate within the only region.
        if (regions.size == 1) {
            return pickFromRegion(regions[0])
        }

        // First-time pick: home-region first when known.
        if (lastMemberId.isEmpty()) {
            val home = if (userCountry.isNotEmpty()) regionForCountry(userCountry) else null
            return pickWithFallback(home)
        }

        // Find lastMember's region and advance to next alphabetically.
        val lastRegion = eligible.firstOrNull { it.id == lastMemberId }?.region ?: ""
        if (lastRegion.isEmpty()) {
            val home = if (userCountry.isNotEmpty()) regionForCountry(userCountry) else null
            return pickWithFallback(home)
        }

        val idx = regions.indexOf(lastRegion)
        val nextRegion = regions[(idx + 1) % regions.size]
        return pickWithFallback(nextRegion)
    }

    /**
     * Returns the continent-region for a country code. Mirrors
     * desktop's geoip.Region(). Hardcoded mapping rather than a
     * runtime database — the list is short, slow-changing, and
     * embedded resources are heavier on Android than a const map.
     */
    fun regionForCountry(cc: String): String = when (cc.uppercase()) {
        // Europe
        "AT", "BE", "BG", "HR", "CZ", "DK", "EE", "FI", "FR", "DE",
        "GR", "HU", "IE", "IT", "LV", "LT", "LU", "MT", "NL", "PL",
        "PT", "RO", "SK", "SI", "ES", "SE", "GB", "UK", "CH", "NO",
        "IS", "AL", "BA", "MK", "ME", "RS", "MD", "UA", "BY", "RU",
        "TR", "XK", "LI", "MC", "SM", "VA", "AD", "FO", "GI", "IM",
        "JE", "GG" -> "Europe"

        // North America
        "US", "CA", "MX" -> "North America"

        // Asia-Pacific
        "JP", "KR", "KP", "CN", "TW", "HK", "MO", "MN",
        "TH", "VN", "PH", "ID", "MY", "SG", "BN", "KH", "LA", "MM", "TL",
        "IN", "PK", "BD", "LK", "NP", "BT", "MV", "AF",
        "AU", "NZ", "FJ", "PG", "SB", "VU", "NC", "PF", "WS", "TO",
        "KI", "TV", "NR", "MH", "FM", "PW", "CK", "NU", "AS", "GU",
        "MP", "UM" -> "Asia-Pacific"

        // South America
        "BR", "AR", "CL", "CO", "PE", "VE", "EC", "BO", "PY", "UY",
        "GY", "SR", "GF", "FK" -> "South America"

        // Africa
        "ZA", "EG", "NG", "KE", "MA", "DZ", "TN", "GH", "ET", "TZ", "UG",
        "AO", "ZW", "ZM", "MZ", "BW", "NA", "MU", "MG", "RW", "SN", "CI",
        "SS", "SD", "LY", "ER", "DJ", "SO", "BI", "MW", "LS", "SZ",
        "CD", "CG", "CM", "CF", "TD", "GA", "GQ", "GM", "GN", "GW",
        "LR", "ML", "MR", "NE", "BF", "BJ", "TG", "SL", "ST", "CV",
        "KM", "SC", "RE", "YT", "SH", "EH" -> "Africa"

        // Middle East
        "AE", "SA", "IL", "QA", "KW", "BH", "OM", "JO", "LB", "IR", "IQ",
        "PS", "SY", "YE" -> "Middle East"

        // Central America / Caribbean
        "CR", "PA", "GT", "HN", "NI", "SV", "BZ", "DO", "JM", "BS",
        "BB", "TT", "CU", "HT", "PR", "AG", "DM", "GD", "KN", "LC",
        "VC", "AI", "AW", "BM", "BQ", "CW", "GP", "KY", "MQ", "MS",
        "SX", "TC", "VG", "VI", "BL", "MF" -> "Central America"

        // Central Asia / Caucasus — folded into Asia-Pacific by
        // mass-market geo databases that don't break out a separate
        // region; group here for explicit coverage.
        "KZ", "UZ", "TM", "TJ", "KG", "AZ", "AM", "GE" -> "Asia-Pacific"

        else -> "Other"
    }
}
