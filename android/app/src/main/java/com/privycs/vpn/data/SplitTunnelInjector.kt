package com.privycs.vpn.data

import android.util.Log
import com.privycs.vpn.data.models.PoolSplitTunnel
import com.privycs.vpn.data.models.VpnProtocol

/**
 * Patches a pool member's config blob with split-tunnel bypass
 * directives derived from the pool's [PoolSplitTunnel] settings.
 *
 * Per-protocol semantics:
 *
 *   WireGuard:  rewrite the [Peer] section's `AllowedIPs = ...`
 *               line as the complement of the bypass set against
 *               the original AllowedIPs. If the original is not
 *               full-tunnel (0.0.0.0/0 OR ::/0), the bypass is
 *               disabled with a log warning - per the user's
 *               decision in v0.9.11.55 design discussion.
 *
 *   OpenVPN:    append `route X mask Y net_gateway` (IPv4) and
 *               `route-ipv6 X::/n net_gateway_ipv6` (IPv6) per
 *               bypass CIDR. Plus `pull-filter ignore "route X"`
 *               directives for any matching server-pushed routes
 *               that would otherwise compete.
 *
 *   IPSec:      no-op + warning. IPSec traffic selectors are
 *               negotiated with the server; client-side narrowing
 *               isn't reliable across providers.
 *
 * Empty / disabled config returns the input unchanged.
 */
object SplitTunnelInjector {

    private const val TAG = "SplitTunnel"

    /**
     * Result type so callers can surface a "split tunnel disabled
     * because the member config isn't full-tunnel" message in the
     * UI / logs without needing to re-parse the config.
     */
    data class InjectResult(
        val patched: String,
        val applied: Boolean,
        val skippedReason: String? = null
    )

    /**
     * Apply the split-tunnel config to the given member config text.
     * No-op when the split-tunnel is disabled (toggle off + no
     * bypass CIDRs).
     */
    fun inject(
        configContent: String,
        protocol: VpnProtocol,
        splitTunnel: PoolSplitTunnel
    ): InjectResult {
        if (!splitTunnel.isActive()) {
            return InjectResult(configContent, applied = false)
        }
        // Build the effective bypass CIDR set: user-typed entries
        // (skipping malformed strings - the UI validates but we
        // double-check here) + RFC1918/IPv6-ULA if enabled.
        val parsed = mutableListOf<CidrMath.Cidr>()
        for (s in splitTunnel.bypassCidrs) {
            CidrMath.parse(s)?.let { parsed.add(it) }
        }
        if (splitTunnel.excludePrivateNetworks) {
            parsed.addAll(CidrMath.PRIVATE_NETWORKS)
        }
        if (parsed.isEmpty()) {
            // Nothing valid to apply. Treat as disabled so we don't
            // accidentally rewrite AllowedIPs to 0.0.0.0/0+::/0
            // (no-change) and trigger a needless tunnel rebuild.
            return InjectResult(configContent, applied = false,
                skippedReason = "no valid CIDRs after parse")
        }

        return when (protocol) {
            // AmneziaWG shares the WG .conf grammar — same AllowedIPs
            // narrowing applies unchanged.
            VpnProtocol.WIREGUARD, VpnProtocol.AMNEZIAWG -> patchWireGuard(configContent, parsed)
            VpnProtocol.OPENVPN -> patchOpenVpn(configContent, parsed)
            VpnProtocol.IPSEC -> {
                Log.w(TAG, "split tunnel skipped: IPSec doesn't support client-side traffic-selector narrowing")
                InjectResult(configContent, applied = false,
                    skippedReason = "IPSec member - client-side split tunnel not supported")
            }
        }
    }

    /**
     * WireGuard patch: parse AllowedIPs from [Peer], compute
     * complement, replace. Disable if AllowedIPs doesn't include
     * a full-tunnel route.
     */
    private fun patchWireGuard(
        content: String,
        bypass: List<CidrMath.Cidr>
    ): InjectResult {
        val lines = content.lines().toMutableList()

        // Find AllowedIPs lines (peer section may have one or
        // multiple AllowedIPs lines, each comma-separated).
        val allowedIPLineIdxs = lines.indices.filter {
            lines[it].trim().startsWith("AllowedIPs", ignoreCase = true) &&
                    lines[it].contains('=')
        }
        if (allowedIPLineIdxs.isEmpty()) {
            return InjectResult(content, applied = false,
                skippedReason = "no AllowedIPs line in config")
        }

        // Aggregate all existing AllowedIPs CIDRs across the lines.
        val existingCidrs = mutableListOf<CidrMath.Cidr>()
        for (idx in allowedIPLineIdxs) {
            val rhs = lines[idx].substringAfter('=').trim()
            for (raw in rhs.split(',')) {
                val c = CidrMath.parse(raw.trim()) ?: continue
                existingCidrs.add(c)
            }
        }

        // v4 universe present? (0.0.0.0/0)
        val hasV4Universe = existingCidrs.any { it.isV4 && it.prefix == 0 }
        // v6 universe present? (::/0)
        val hasV6Universe = existingCidrs.any { !it.isV4 && it.prefix == 0 }

        if (!hasV4Universe && !hasV6Universe) {
            // Member config is already a custom split-tunnel
            // (provider-specific routes). User decision: disable
            // bypass with warning rather than risk unpredictable
            // route layering.
            Log.w(TAG, "split tunnel skipped: AllowedIPs is not full-tunnel " +
                    "(no 0.0.0.0/0 or ::/0) - existing routes would conflict")
            return InjectResult(content, applied = false,
                skippedReason = "member config is not full-tunnel; pool-level bypass cannot apply safely")
        }

        // Compute complement only for the families present in the
        // existing AllowedIPs. If existing is v4-only, leave v6
        // out of the result (we don't want to add ::/0 silently).
        val effectiveBypass = bypass.filter {
            (it.isV4 && hasV4Universe) || (!it.isV4 && hasV6Universe)
        }
        val complement = CidrMath.subtractFromUniverse(effectiveBypass)
            .filter {
                (it.isV4 && hasV4Universe) || (!it.isV4 && hasV6Universe)
            }

        if (complement.isEmpty()) {
            // Pathological: bypass covers the entire universe(s).
            // Skip rather than emit "AllowedIPs = " which the WG
            // parser may reject or treat as "block all".
            Log.w(TAG, "split tunnel skipped: bypass covers full universe, would result in empty AllowedIPs")
            return InjectResult(content, applied = false,
                skippedReason = "bypass set covers entire address space")
        }

        // Replace the FIRST AllowedIPs line with the complement,
        // delete any subsequent AllowedIPs lines (we collapse
        // multi-line entries into one for readability).
        val replacement = "AllowedIPs = " + complement.joinToString(", ") { it.toCidrString() }
        // Remove from highest idx down so indices stay valid.
        for (idx in allowedIPLineIdxs.drop(1).reversed()) {
            lines.removeAt(idx)
        }
        lines[allowedIPLineIdxs[0]] = replacement

        Log.i(TAG, "WG split tunnel applied: ${effectiveBypass.size} bypass " +
                "CIDRs, ${complement.size} resulting AllowedIPs entries")
        return InjectResult(lines.joinToString("\n"), applied = true)
    }

    /**
     * OpenVPN patch: prepend route directives so the bypass CIDRs
     * land on the local default gateway instead of the tunnel.
     * Append + pull-filter ignore for any server-pushed routes
     * that match (so the server can't undo our bypass).
     */
    private fun patchOpenVpn(
        content: String,
        bypass: List<CidrMath.Cidr>
    ): InjectResult {
        val sb = StringBuilder()
        // pull-filter must come before any other route directives
        // for OpenVPN's parser to apply it. Ignore any server
        // pushed route for an IP within our bypass list.
        sb.appendLine("# Privycs split tunnel: client-side bypass routes")
        for (c in bypass) {
            // pull-filter pattern matches the start of the route
            // directive. Server typically pushes "route a.b.c.d
            // m.m.m.m" - prefix-match catches it.
            if (c.isV4) {
                sb.appendLine("pull-filter ignore \"route ${c.toCidrString().substringBefore('/')}\"")
            } else {
                sb.appendLine("pull-filter ignore \"route-ipv6 ${c.toCidrString().substringBefore('/')}\"")
            }
        }
        for (c in bypass) {
            if (c.isV4) {
                val ip = c.toCidrString().substringBefore('/')
                val mask = prefixToIPv4Mask(c.prefix)
                sb.appendLine("route $ip $mask net_gateway")
            } else {
                sb.appendLine("route-ipv6 ${c.toCidrString()} net_gateway_ipv6")
            }
        }
        sb.append(content)
        Log.i(TAG, "OpenVPN split tunnel applied: ${bypass.size} bypass CIDRs")
        return InjectResult(sb.toString(), applied = true)
    }

    /** Convert "/24" prefix to "255.255.255.0" netmask form. */
    private fun prefixToIPv4Mask(prefix: Int): String {
        val mask = if (prefix == 0) 0L else (0xFFFFFFFFL shl (32 - prefix)) and 0xFFFFFFFFL
        return "${(mask shr 24) and 0xFF}.${(mask shr 16) and 0xFF}.${(mask shr 8) and 0xFF}.${mask and 0xFF}"
    }
}
