package com.privycs.vpn.data.models

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Per-network auto-tunnel routing rule. The rules engine walks
 * the user's rule list in priority order on every network event;
 * the first rule that matches the current network state determines
 * the active VPN target (or "no VPN" if the rule says so).
 *
 * Rule semantics:
 *   - If at least ONE rule exists in the user's list, the rules
 *     engine becomes authoritative for the connect lifecycle.
 *     The legacy COD trigger / SSID-mode logic is bypassed.
 *   - If the user has zero rules, the legacy COD logic runs as
 *     before. This is the default for upgrade users so behaviour
 *     stays identical until they create a rule.
 *
 * Match types:
 *   - SSID_EXACT: matches a Wi-Fi network by its exact SSID.
 *     Case-insensitive on the SSID name.
 *   - SSID_PATTERN: matches a Wi-Fi network by glob pattern,
 *     e.g. "Cafe*" matches "Cafe-Wifi" and "Cafe-Guest".
 *   - NETWORK_TYPE: matches by transport - "wifi", "mobile",
 *     "ethernet", "any". Doesn't care about SSID.
 *   - BSSID: matches a Wi-Fi access point by hardware MAC.
 *     Defends against SSID spoofing (someone naming their
 *     hotspot "HomeWifi" at the airport). Requires Location
 *     permission to read.
 *   - ANY: catch-all. Useful as the last "default" rule.
 *
 * Actions:
 *   - NO_VPN: if currently connected to anything, disconnect.
 *     Implements the "trusted network" pattern.
 *   - POOL: switch to the pool with id = targetId.
 *   - CONNECTION: switch to the single connection with id = targetId.
 */
@Serializable
enum class RuleMatchType {
    @SerialName("ssid_exact") SSID_EXACT,
    @SerialName("ssid_pattern") SSID_PATTERN,
    @SerialName("network_type") NETWORK_TYPE,
    @SerialName("bssid") BSSID,
    @SerialName("any") ANY,
}

@Serializable
enum class RuleAction {
    @SerialName("no_vpn") NO_VPN,
    @SerialName("pool") POOL,
    @SerialName("connection") CONNECTION,
}

@Serializable
data class NetworkRule(
    val id: String,
    val priority: Int,
    @SerialName("match_type")
    val matchType: RuleMatchType,
    @SerialName("match_value")
    val matchValue: String,
    val action: RuleAction,
    @SerialName("target_id")
    val targetId: String = "",
    val enabled: Boolean = true,
    val name: String = "",
) {
    /**
     * Returns true if this rule matches the supplied network state.
     * State fields:
     *   - networkType: "wifi" / "mobile" / "ethernet" / "none"
     *   - ssid: current Wi-Fi SSID (empty when not on Wi-Fi)
     *   - bssid: current Wi-Fi access-point MAC (empty when not
     *     on Wi-Fi or Location permission missing)
     */
    fun matches(networkType: String, ssid: String, bssid: String): Boolean {
        if (!enabled) return false
        return when (matchType) {
            RuleMatchType.SSID_EXACT ->
                networkType == "wifi" && ssid.equals(matchValue, ignoreCase = true)
            RuleMatchType.SSID_PATTERN ->
                networkType == "wifi" && ssid.isNotEmpty() &&
                        globMatches(matchValue, ssid)
            RuleMatchType.NETWORK_TYPE ->
                when (matchValue.lowercase()) {
                    "any" -> networkType != "none"
                    "wifi" -> networkType == "wifi"
                    "mobile" -> networkType == "mobile"
                    "ethernet" -> networkType == "ethernet"
                    "wifi_mobile" -> networkType == "wifi" || networkType == "mobile"
                    else -> false
                }
            RuleMatchType.BSSID ->
                networkType == "wifi" && bssid.isNotEmpty() &&
                        bssid.equals(matchValue, ignoreCase = true)
            RuleMatchType.ANY -> networkType != "none"
        }
    }

    /**
     * Glob match: '*' matches any substring (including empty),
     * '?' matches a single character. Case-insensitive. No
     * regex-special characters in matchValue need escaping
     * because we translate to a literal-with-wildcards Regex
     * directly. Used for SSID_PATTERN.
     */
    private fun globMatches(pattern: String, input: String): Boolean {
        val regex = buildString {
            for (c in pattern) {
                when (c) {
                    '*' -> append(".*")
                    '?' -> append('.')
                    in "\\.[](){}+|^$" -> append('\\').append(c)
                    else -> append(c)
                }
            }
        }
        return Regex("^$regex$", RegexOption.IGNORE_CASE).matches(input)
    }
}

/**
 * Resolution result returned by the rules engine to the auto-
 * tunnel evaluator. NoMatch means "fall through to legacy COD
 * logic"; any specific result short-circuits the legacy path.
 */
sealed class RuleResolution {
    object NoMatch : RuleResolution()
    object NoVpn : RuleResolution()
    data class Pool(val poolId: String) : RuleResolution()
    data class Connection(val connectionId: String) : RuleResolution()
}
