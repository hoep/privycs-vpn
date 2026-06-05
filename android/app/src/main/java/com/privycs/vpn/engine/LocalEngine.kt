package com.privycs.vpn.engine

import com.privycs.vpn.data.models.VpnProtocol

/**
 * Pure-Kotlin port of the Smart Decision Engine's deterministic selection +
 * reasoning (engine/select.go + engine/reason.go). Identical ranking/reason
 * logic to iOS/desktop — but with NO gomobile binding, so the engine's Go
 * runtime (libgojni.so) never loads into the app process. That removes the
 * fatal two-Go-runtime conflict with the WireGuard/AmneziaWG runtime
 * (libwg-go-awg.so) that segfaulted on connect; on iOS the tunnel runs in a
 * separate NE process so the conflict never arose, on Android there is only one
 * process, so the engine is reimplemented natively instead.
 *
 * PURE + deterministic (no time/IO/randomness): same inputs → same output, so
 * it replays identically to the Go original and is fully unit-testable.
 */
object LocalEngine {

    /** A protocol's learned performance on the current network (P4 adaptive). */
    data class Stat(val successEwma: Int, val lastFailSec: Long)

    /** A protocol that failed within this window on this network is heavily
     *  demoted (give the network time to recover / prefer alternatives). */
    private const val FAIL_COOLDOWN_SEC = 600L

    // ISO-3166-1 alpha-2 codes with systemic VPN/DPI blocking where AmneziaWG's
    // obfuscation materially helps. Matches engine/reason.go exactly.
    private val RESTRICTIVE = setOf(
        "CN", "IR", "RU", "BY", "TM", "KP", "SY", "CU", "MM", "AE", "OM", "EG",
    )

    fun isRestrictiveCountry(cc: String): Boolean =
        RESTRICTIVE.contains(cc.trim().uppercase())

    /**
     * Context-ranked order (censorship/speed + roaming), most-preferred first,
     * BEFORE adaptive stats. `iface` is the platform interface token
     * ("wifi"/"cellular"/"ethernet"/"").
     */
    private fun baseContextOrder(country: String, iface: String): List<VpnProtocol> = when {
        isRestrictiveCountry(country) ->
            // Evasion first; roaming is secondary to beating DPI.
            listOf(VpnProtocol.AMNEZIAWG, VpnProtocol.OPENVPN, VpnProtocol.WIREGUARD, VpnProtocol.IPSEC)
        iface == "cellular" ->
            // MOBIKE: IPSec rides through Wi-Fi↔cellular handoffs — bump to second.
            listOf(VpnProtocol.WIREGUARD, VpnProtocol.IPSEC, VpnProtocol.AMNEZIAWG, VpnProtocol.OPENVPN)
        else ->
            listOf(VpnProtocol.WIREGUARD, VpnProtocol.AMNEZIAWG, VpnProtocol.OPENVPN, VpnProtocol.IPSEC)
    }

    /** The context-ranked order for a country + interface (no stats). */
    fun protocolOrder(country: String, iface: String): List<VpnProtocol> =
        baseContextOrder(country, iface)

    /**
     * Rank the usable protocols best-first by context, roaming and adaptive
     * stats. Excludes are dropped. Deterministic — a faithful port of
     * engine/select.go SelectOrder (integer scoring, stable tie-break).
     */
    fun selectOrder(
        available: List<VpnProtocol>,
        country: String,
        iface: String,
        nowSec: Long,
        exclude: Set<VpnProtocol> = emptySet(),
        stats: Map<VpnProtocol, Stat> = emptyMap(),
    ): List<VpnProtocol> {
        val base = baseContextOrder(country, iface)
        val pos = base.withIndex().associate { (i, p) -> p to i }
        val seen = HashSet<VpnProtocol>()
        // (protocol, score, base-position) — lowest score wins, ties by base.
        data class Scored(val p: VpnProtocol, val score: Int, val base: Int)
        val list = ArrayList<Scored>()
        for (p in available) {
            if (p in exclude || p in seen) continue
            seen.add(p)
            val bpos = pos[p] ?: base.size // unknown protocol sorts last
            var score = bpos * 100
            stats[p]?.let { st ->
                if (st.lastFailSec != 0L && nowSec - st.lastFailSec < FAIL_COOLDOWN_SEC) score += 500
                score -= st.successEwma / 10
            }
            list.add(Scored(p, score, bpos))
        }
        // Kotlin sortedWith is stable → equal-key items keep input (= context)
        // order, matching Go's sort.SliceStable.
        return list.sortedWith(compareBy({ it.score }, { it.base })).map { it.p }
    }

    /**
     * Network-aware reason key (+ args) for the active protocol given the user's
     * country and AmneziaWG availability. Port of engine/reason.go CountryReason.
     * Empty key = no reason (country unknown). args[0] is the upper-cased CC; the
     * UI resolves it to a localized country name.
     */
    fun countryReason(country: String, active: VpnProtocol?, awgAvailable: Boolean): Pair<String, List<String>> {
        val cc = country.trim().uppercase()
        if (cc.isEmpty()) return "" to emptyList()
        if (!isRestrictiveCountry(cc)) return "reason.country_open" to listOf(cc)
        return when {
            active == VpnProtocol.AMNEZIAWG -> "reason.country_restrictive_awg" to listOf(cc)
            awgAvailable -> "reason.country_restrictive_use_awg" to listOf(cc)
            else -> "reason.country_restrictive_no_awg" to listOf(cc)
        }
    }
}
