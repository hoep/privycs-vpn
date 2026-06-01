import Foundation

/// Field-for-field port of Android's `IpV6KillswitchInjector`
/// (`android/.../data/IpV6KillswitchInjector.kt`). Patches a tunnel
/// config blob with directives that capture all IPv6 traffic into the
/// VPN tun, even when the tunnel itself is IPv4-only. Without this a
/// v4-only tunnel installs only IPv4 routes; the device's native IPv6
/// default route still applies for AAAA-resolved destinations, and
/// traffic the user expects to be inside the VPN exits in the clear.
///
/// This is exactly why iOS AmneziaWG "had no IPv6": the gateway often
/// sends `AllowedIPs = 0.0.0.0/0` (v4-only), Android re-adds `::/0`
/// always-on, iOS did not — so v6 never entered the tunnel. Mirrors the
/// Android caller (`PrivycsVpnService`), which applies this
/// unconditionally (no user setting) and relies on idempotency.
///
/// Per-protocol semantics:
///   - WireGuard / AmneziaWG: rewrite the `[Peer]` `AllowedIPs` line to
///     include `::/0` when missing. WireGuard then installs a v6 default
///     route to our tun. If the interface carries a v6 address and the
///     server speaks v6, v6 works; otherwise v6 is blackholed at the tun
///     fd instead of leaking via the OS default v6 route.
///   - OpenVPN: append `route-ipv6 ::/0` + `redirect-gateway ipv6`.
///   - IPSec (.sswan JSON): ensure `remote_ts` includes `::/0`
///     (best-effort — the server may narrow the selector back).
///
/// Idempotent: each patch is a no-op when `::/0` is already present.
public enum IPv6KillswitchInjector {

    public struct Result {
        public let patched: String
        public let applied: Bool
        public let skippedReason: String?
    }

    public static func inject(_ configContent: String, protocol proto: VpnProtocol) -> Result {
        switch proto {
        // AmneziaWG shares the WG .conf grammar — same patch applies.
        case .wireguard, .amneziawg: return patchWireGuard(configContent)
        case .openvpn:               return patchOpenVPN(configContent)
        case .ipsec:                 return patchIPSec(configContent)
        }
    }

    // MARK: WireGuard / AmneziaWG

    private static func patchWireGuard(_ configContent: String) -> Result {
        var lines = configContent.components(separatedBy: "\n")
        var inPeer = false
        var patched = false
        for i in lines.indices {
            let line = lines[i].trimmingCharacters(in: .whitespaces)
            if line.hasPrefix("[") {
                inPeer = line.caseInsensitiveCompare("[Peer]") == .orderedSame
                continue
            }
            guard inPeer else { continue }
            guard line.lowercased().hasPrefix("allowedips") else { continue }
            guard let eqIdx = line.firstIndex(of: "=") else { continue }
            let current = line[line.index(after: eqIdx)...]
                .split(separator: ",")
                .map { $0.trimmingCharacters(in: .whitespaces) }
                .filter { !$0.isEmpty }
            // Idempotent: leave the line alone if a v6 catch-all is there.
            let hasV6CatchAll = current.contains { $0 == "::/0" || $0 == "::0/0" }
            if hasV6CatchAll { continue }
            let newAllowed = current + ["::/0"]
            lines[i] = "AllowedIPs = " + newAllowed.joined(separator: ", ")
            patched = true
        }
        return Result(
            patched: lines.joined(separator: "\n"),
            applied: patched,
            skippedReason: patched ? nil : "AllowedIPs already covers v6 or not found"
        )
    }

    // MARK: OpenVPN

    private static func patchOpenVPN(_ configContent: String) -> Result {
        let lines = configContent.components(separatedBy: "\n")
        let alreadyV6Route = lines.contains { l in
            let t = l.trimmingCharacters(in: .whitespaces).lowercased()
            return t.hasPrefix("route-ipv6") && t.contains("::/0")
        }
        let alreadyV6Redirect = lines.contains { l in
            let t = l.trimmingCharacters(in: .whitespaces).lowercased()
            return t.hasPrefix("redirect-gateway") && t.contains("ipv6")
        }
        if alreadyV6Route || alreadyV6Redirect {
            return Result(patched: configContent, applied: false,
                          skippedReason: "OpenVPN config already routes v6 to tun")
        }
        let patch = """

        # IPv6 leak killswitch — capture all v6 traffic into tun even when
        # the tunnel itself is v4-only. The peer has no v6 endpoint, so
        # packets dropped at the tun fd are blackholed instead of leaking
        # via the OS default v6 route.
        route-ipv6 ::/0
        redirect-gateway ipv6 def1 bypass-dhcp
        """
        return Result(patched: configContent + "\n" + patch + "\n", applied: true,
                      skippedReason: nil)
    }

    // MARK: IPSec (.sswan strongSwan JSON profile)

    private static func patchIPSec(_ configContent: String) -> Result {
        guard configContent.contains("\"remote_ts\"") else {
            return Result(patched: configContent, applied: false,
                          skippedReason: "no remote_ts in .sswan, IPSec ts injection inapplicable")
        }
        // Match the JSON array form:  "remote_ts": ["0.0.0.0/0"]
        let pattern = "\"remote_ts\"\\s*:\\s*\\[([^\\]]*)]"
        guard let regex = try? NSRegularExpression(pattern: pattern, options: [.dotMatchesLineSeparators]),
              let m = regex.firstMatch(in: configContent, range: NSRange(configContent.startIndex..., in: configContent)),
              let fullRange = Range(m.range, in: configContent),
              let innerRange = Range(m.range(at: 1), in: configContent) else {
            return Result(patched: configContent, applied: false,
                          skippedReason: "remote_ts not in expected list form")
        }
        let inner = String(configContent[innerRange])
        if inner.contains("::/0") || inner.contains("::0/0") {
            return Result(patched: configContent, applied: false,
                          skippedReason: "remote_ts already includes v6 catch-all")
        }
        let trimmed = inner.trimmingCharacters(in: .whitespaces)
        let patchedInner = trimmed.isEmpty ? "\"::/0\"" : "\(trimmed), \"::/0\""
        var patched = configContent
        patched.replaceSubrange(fullRange, with: "\"remote_ts\": [\(patchedInner)]")
        return Result(patched: patched, applied: true, skippedReason: nil)
    }
}
