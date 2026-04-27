package com.privycs.vpn.data.models

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Connection Pool data model — Android port of desktop's pool_registry.go
 * (post v0.9.11.39 state-separation refactor).
 *
 * Architecture mirror of desktop:
 *   - pools.json: definition state (immutable after import; rare writes)
 *   - pool_state.json: runtime state (active/pending member, slot,
 *     unreachable flags) — moved out for write-amplification reasons
 *
 * Persistence: filesDir/pools.json + filesDir/pool_state.json. Both
 * managed by PoolRepository / PoolStateRepository respectively.
 *
 * Differences from desktop's Pool struct:
 *   - No legacy fields needed (Android version is fresh, never had
 *     mixed runtime/definition data).
 *   - No memberByID index field on the data class — repository
 *     maintains a parallel cache.
 */

@Serializable
enum class PoolPolicy(val wireValue: String, val displayName: String) {
    @SerialName("geo-nearest")
    GEO_NEAREST("geo-nearest", "Geo-Nearest"),

    @SerialName("random")
    RANDOM("random", "Random"),

    @SerialName("round-robin-region")
    ROUND_ROBIN("round-robin-region", "Round-Robin");

    companion object {
        fun fromWireValue(value: String): PoolPolicy? =
            values().firstOrNull { it.wireValue == value }
    }
}

@Serializable
data class PoolRotation(
    @SerialName("interval_min")
    val intervalMin: Int = 30,
    @SerialName("idle_aware")
    val idleAware: Boolean = true,                      // default ON for Android (battery)
    @SerialName("force_after_min")
    val forceAfterMin: Int = 60
) {
    companion object {
        fun default() = PoolRotation()
    }
}

/**
 * One config inside a Pool. Country/Region resolved at import time
 * via the bundled MMDB (or empty if MMDB lookup failed).
 *
 * Runtime state (unreachable, last error, last unreachable timestamp)
 * lives in PoolStateRepository keyed by (poolID, memberID), not on
 * this struct. Reading it here would tempt direct mutation — see
 * desktop's v0.9.11.39 commit notes for why we burnt that bridge.
 */
@Serializable
data class PoolMember(
    val id: String,
    var name: String,
    val config: ProtocolConfig,
    val country: String = "",                           // ISO 3166-1 alpha-2
    val region: String = "",                            // continent-level
    @SerialName("active")
    val active: Boolean = true                          // future Pro-tier cap; default true
)

/**
 * A virtual connection wrapping multiple PoolMembers. The picker
 * runs the policy at connect-time / rotation-time to choose one.
 */
@Serializable
data class Pool(
    val id: String,
    var name: String,
    @SerialName("created_at")
    val createdAt: String = "",                         // RFC3339 timestamp
    var policy: PoolPolicy,
    var rotation: PoolRotation = PoolRotation.default(),
    val members: MutableList<PoolMember> = mutableListOf(),
    @SerialName("country_override")
    var countryOverride: String = "",                   // "" = auto-detect
    @SerialName("restrict_regions")
    var restrictRegions: List<String> = emptyList()     // [] = no restriction
) {
    /** O(n) member lookup. Repository caches per-pool index for O(1). */
    fun memberById(id: String): PoolMember? = members.firstOrNull { it.id == id }
}

/**
 * Wire-shape returned to the UI for the connection picker. We do
 * NOT send the full member list (could be 600 entries) on every
 * poll; the picker only needs ID, name, member-count, and the
 * active-member display fields.
 */
data class PoolListItem(
    val id: String,
    val name: String,
    val policy: PoolPolicy,
    val memberCount: Int,
    val activeMemberId: String = "",
    val activeMemberName: String = "",
    val activeMemberCountry: String = "",
    val pendingMemberId: String = "",
    val pendingMemberName: String = "",
    val pendingMemberCountry: String = "",
    val isActive: Boolean = false
)

/**
 * One row of the pool-detail-view's coverage breakdown.
 */
data class RegionCoverage(
    val region: String,
    val servers: Int,
    val countries: Int
)

/**
 * Wire-shape for AddPoolFlow's progress reporting.
 */
data class PoolImportProgress(
    val stage: Stage,
    val current: Int = 0,
    val total: Int = 0,
    val imported: Int = 0,
    val skipped: Int = 0,
    val message: String = ""
) {
    enum class Stage { EXTRACTING, PARSING, RESOLVING, DONE }
}

/**
 * Reason a config file was skipped during import. Surfaced in the
 * import-completion sheet as "488 imported, 12 skipped" with
 * grouped reasons.
 */
data class SkippedFile(
    val filename: String,
    val reason: String
)

data class PoolImportResult(
    val members: List<PoolMember>,
    val skipped: List<SkippedFile>
)
