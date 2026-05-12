package com.privycs.vpn.data

import android.util.Log
import com.privycs.vpn.data.models.VpnProtocol

/**
 * Patches a tunnel config blob with directives that capture all
 * IPv6 traffic into the VPN tun, even when the tunnel itself is
 * IPv4-only. Without this, a v4-only tunnel installs only IPv4
 * routes; the device's native IPv6 default route still applies
 * for AAAA-resolved destinations, and traffic the user expects
 * to be inside the VPN exits in the clear.
 *
 * Per-protocol semantics:
 *
 *   WireGuard:  rewrites the [Peer] section's `AllowedIPs = ...`
 *               line to include `::/0` if not already present.
 *               When `::/0` is in AllowedIPs, the WireGuard library
 *               adds an IPv6 default route that points at our tun.
 *               The peer doesn't have a v6 endpoint, so packets
 *               handed to the tun get dropped at write-time. Net:
 *               v6 traffic gets blackholed at our tun fd instead
 *               of leaking via the OS default v6 route.
 *
 *   OpenVPN:    appends `route-ipv6 ::/0` and `redirect-gateway
 *               ipv6` directives. ics-openvpn's parser installs a
 *               v6 default route to the tun based on these. Same
 *               drop semantics as WireGuard.
 *
 *   IPSec:      patches the .sswan JSON to add a `local_ts` /
 *               `remote_ts` entry that includes `::/0`. Whether
 *               the server accepts the v6 traffic-selector during
 *               IKE_AUTH determines whether v6 is actually
 *               protected — strongSwan negotiates selectors with
 *               the gateway, and a v4-only server may narrow the
 *               selector back to 0.0.0.0/0 only. Best-effort: the
 *               injection costs nothing on negotiation but
 *               provides full protection when the server agrees.
 *
 * Idempotent — each protocol's patch is a no-op if `::/0` is
 * already present. The [Result.applied] flag tells callers
 * whether anything was actually rewritten.
 */
object IpV6KillswitchInjector {

    private const val TAG = "IPv6KS"

    data class Result(
        val patched: String,
        val applied: Boolean,
        val skippedReason: String? = null,
    )

    /**
     * Apply IPv6-into-tunnel routing rules to the given config.
     * Caller should consult `enabled` (the user setting) AND
     * tunnel-is-v4-only detection BEFORE calling — this function
     * unconditionally patches without consulting either, so it
     * can also be used preemptively (e.g. before the first connect
     * when we don't yet know the tunnel's effective stack).
     */
    fun inject(configContent: String, protocol: VpnProtocol): Result {
        return when (protocol) {
            // AmneziaWG shares the WG .conf grammar — same kill-switch
            // patch (AllowedIPs = 0.0.0.0/0, ::/0) applies unchanged.
            VpnProtocol.WIREGUARD, VpnProtocol.AMNEZIAWG -> patchWireGuard(configContent)
            VpnProtocol.OPENVPN -> patchOpenVpn(configContent)
            VpnProtocol.IPSEC -> patchIpSec(configContent)
        }
    }

    // ------------------------------------------------------------
    // WireGuard
    // ------------------------------------------------------------

    private fun patchWireGuard(configContent: String): Result {
        // Walk lines, find AllowedIPs in the [Peer] section, add
        // ::/0 if missing. WireGuard config is line-based with
        // simple `Key = value` pairs; AllowedIPs is comma-separated.
        val lines = configContent.lines().toMutableList()
        var inPeer = false
        var patched = false
        for (i in lines.indices) {
            val line = lines[i].trim()
            if (line.startsWith("[")) {
                inPeer = line.equals("[Peer]", ignoreCase = true)
                continue
            }
            if (!inPeer) continue
            if (!line.startsWith("AllowedIPs", ignoreCase = true)) continue
            val eqIdx = line.indexOf('=')
            if (eqIdx < 0) continue
            val current = line.substring(eqIdx + 1)
                .split(',')
                .map { it.trim() }
                .filter { it.isNotEmpty() }
            // Idempotent: if any v6 catch-all entry is already
            // there, leave the line alone.
            val hasV6CatchAll = current.any {
                it == "::/0" || it == "::0/0"
            }
            if (hasV6CatchAll) {
                continue
            }
            val newAllowed = current + "::/0"
            val rebuilt = "AllowedIPs = " + newAllowed.joinToString(", ")
            lines[i] = rebuilt
            patched = true
            Log.i(TAG, "WireGuard: appended ::/0 to AllowedIPs (was: $current)")
        }
        return Result(
            patched = lines.joinToString("\n"),
            applied = patched,
            skippedReason = if (patched) null else "AllowedIPs already covers v6 or not found",
        )
    }

    // ------------------------------------------------------------
    // OpenVPN
    // ------------------------------------------------------------

    private fun patchOpenVpn(configContent: String): Result {
        // Skip if we already see route-ipv6 ::/0 OR
        // redirect-gateway ipv6 in the config.
        val lines = configContent.lines()
        val alreadyV6Route = lines.any {
            val t = it.trim().lowercase()
            t.startsWith("route-ipv6") && t.contains("::/0")
        }
        val alreadyV6Redirect = lines.any {
            val t = it.trim().lowercase()
            t.startsWith("redirect-gateway") && t.contains("ipv6")
        }
        if (alreadyV6Route || alreadyV6Redirect) {
            return Result(configContent, applied = false,
                skippedReason = "OpenVPN config already routes v6 to tun")
        }
        // Append v6 routing directives. ics-openvpn's parser
        // recognises both forms; we add both because they cover
        // slightly different cases (route-ipv6 is explicit; the
        // redirect-gateway flag handles server-pushed gateway
        // policies that would otherwise install v6 default routes).
        val patch = """

# v0.9.14.96 IPv6 leak killswitch — capture all v6 traffic into tun
# even when the tunnel itself is v4-only. The peer has no v6
# endpoint, so packets dropped at the tun fd are blackholed
# instead of leaking via the OS default v6 route.
route-ipv6 ::/0
redirect-gateway ipv6 def1 bypass-dhcp
""".trimIndent()
        return Result(
            patched = configContent + "\n" + patch + "\n",
            applied = true,
        )
    }

    // ------------------------------------------------------------
    // IPSec (.sswan strongSwan JSON profile)
    // ------------------------------------------------------------

    private fun patchIpSec(configContent: String): Result {
        // .sswan is a JSON object. Look for the `remote_ts` (or
        // `local_ts`) field and ensure it includes ::/0,0.0.0.0/0.
        // strongSwan negotiates traffic selectors with the server
        // during IKE_AUTH; setting them client-side declares OUR
        // intent. If the server has v6 selectors, the negotiation
        // succeeds and v6 is captured. If the server is v4-only,
        // the server narrows back and v6 still leaks — best-effort.
        if (!configContent.contains("\"remote_ts\"")) {
            return Result(configContent, applied = false,
                skippedReason = "no remote_ts in .sswan, IPSec ts injection inapplicable")
        }
        // String-level patch — full JSON-AST rewrite is overkill
        // for one field. Match the JSON array form:
        //   "remote_ts": ["0.0.0.0/0"]
        // and inject ::/0 if not present.
        val tsRegex = Regex(
            "\"remote_ts\"\\s*:\\s*\\[(?<inner>[^\\]]*)]",
            RegexOption.DOT_MATCHES_ALL,
        )
        val match = tsRegex.find(configContent)
            ?: return Result(configContent, applied = false,
                skippedReason = "remote_ts not in expected list form")
        val inner = match.groups["inner"]?.value ?: ""
        if (inner.contains("::/0") || inner.contains("::0/0")) {
            return Result(configContent, applied = false,
                skippedReason = "remote_ts already includes v6 catch-all")
        }
        // Append "::/0" to the array.
        val trimmed = inner.trim()
        val patchedInner = if (trimmed.isEmpty()) {
            "\"::/0\""
        } else {
            "$trimmed, \"::/0\""
        }
        val patched = configContent.replaceRange(
            match.range,
            "\"remote_ts\": [$patchedInner]"
        )
        Log.i(TAG, "IPSec: appended ::/0 to remote_ts")
        return Result(patched = patched, applied = true)
    }
}
