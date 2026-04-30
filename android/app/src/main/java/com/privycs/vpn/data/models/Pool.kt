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
/**
 * One config in a Pool. All fields are immutable - use
 * PoolRepository.renameMember() to change the display name; the
 * repository handles persistence + StateFlow notification.
 */
@Serializable
data class PoolMember(
    val id: String,
    val name: String,
    val config: ProtocolConfig,
    val country: String = "",                           // ISO 3166-1 alpha-2
    val region: String = "",                            // continent-level
    @SerialName("active")
    val active: Boolean = true                          // future Pro-tier cap
)

/**
 * A virtual connection wrapping multiple PoolMembers. All fields
 * immutable - mutations go through PoolRepository.update() which
 * receives a copy() with the modified fields. This makes Compose's
 * structural-equality recomposition reliable; in-place mutation
 * was the source of UI staleness in earlier drafts.
 */
@Serializable
data class Pool(
    val id: String,
    val name: String,
    @SerialName("created_at")
    val createdAt: String = "",                         // RFC3339 timestamp
    val policy: PoolPolicy,
    val rotation: PoolRotation = PoolRotation.default(),
    val members: MutableList<PoolMember> = mutableListOf(),
    @SerialName("country_override")
    val countryOverride: String = "",
    @SerialName("restrict_regions")
    val restrictRegions: List<String> = emptyList(),
    /**
     * Per-pool client-side split-tunnel configuration. When set,
     * the listed CIDRs (plus RFC1918+IPv6-ULA if the toggle is on)
     * are excluded from the pool member's tunnel. Empty bypass
     * list + toggle off = feature disabled, member config passes
     * through unchanged.
     *
     * Default-on field: existing pools persisted before v0.9.11.55
     * deserialise with the default (disabled), so behaviour stays
     * identical for any pool that hasn't opted in.
     */
    @SerialName("split_tunnel")
    val splitTunnel: PoolSplitTunnel = PoolSplitTunnel(),
    /**
     * Per-pool DNS override. Comma- or whitespace-separated
     * IPv4/IPv6 list. When non-empty, overrides the global
     * Settings.dnsOverride for the duration of this pool's
     * tunnel. Empty falls back to global. Mirrors desktop
     * Pool.DnsOverride for cross-platform parity.
     */
    @SerialName("dns_override")
    val dnsOverride: String = ""
) {
    /** O(n) member lookup. Repository caches per-pool index for O(1). */
    fun memberById(id: String): PoolMember? = members.firstOrNull { it.id == id }
}

/**
 * Client-side split-tunnel config attached to each pool. Bypass
 * means "this traffic goes around the VPN" - the CIDRs land on
 * the local network's default route instead of the tunnel.
 *
 * Algorithm (in CidrMath + SplitTunnelInjector):
 *   AllowedIPs becomes (0.0.0.0/0 + ::/0) MINUS bypass set.
 *   ~30-50 output CIDRs for typical 5-10 bypass entries.
 *
 * IPSec is unsupported because traffic selectors require server
 * cooperation; the injector logs a warning and leaves IPSec pool
 * members untouched.
 */
@Serializable
data class PoolSplitTunnel(
    /**
     * User-typed CIDRs to bypass the VPN. Each entry is parsed by
     * CidrMath.parse() at injection time; invalid entries are
     * dropped silently (the UI surfaces a per-line validation hint
     * before saving so this should be rare).
     */
    @SerialName("bypass_cidrs")
    val bypassCidrs: List<String> = emptyList(),
    /**
     * Convenience toggle: include the standard private-network
     * CIDRs (RFC1918 IPv4 + IPv6 ULA + link-local on both
     * families) in the bypass set without making the user type
     * them. Default off so existing pools are unaffected.
     */
    @SerialName("exclude_private_networks")
    val excludePrivateNetworks: Boolean = false
) {
    /** True if this config has any effect at injection time. */
    fun isActive(): Boolean = bypassCidrs.isNotEmpty() || excludePrivateNetworks
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
