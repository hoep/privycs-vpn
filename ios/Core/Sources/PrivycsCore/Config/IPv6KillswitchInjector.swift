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
        // If we just added the v6 catch-all ROUTE but the [Interface] Address
        // is v4-only, iOS builds NEIPv6Settings with NO source address and
        // refuses to originate IPv6 (Android's GoBackend tun does not need a
        // v6 source — that is the iOS-vs-Android divergence behind "AmneziaWG
        // has no IPv6"). Add a deterministic ULA v6 source so iOS routes v6
        // into the tunnel. No-op when the config already carries a v6 address.
        var v6AddrInjected = false
        if patched {
            var inInterface = false
            for i in lines.indices {
                let t = lines[i].trimmingCharacters(in: .whitespaces)
                if t.hasPrefix("[") {
                    inInterface = t.caseInsensitiveCompare("[Interface]") == .orderedSame
                    continue
                }
                guard inInterface, t.lowercased().hasPrefix("address"),
                      let eq = t.firstIndex(of: "=") else { continue }
                let addrs = t[t.index(after: eq)...]
                    .split(separator: ",")
                    .map { $0.trimmingCharacters(in: .whitespaces) }
                    .filter { !$0.isEmpty }
                if addrs.contains(where: { $0.contains(":") }) { break }   // already has v6
                if let v4 = addrs.first(where: { $0.contains(".") }) {
                    lines[i] = "Address = " + (addrs + [Self.deriveULA(fromV4: v4)]).joined(separator: ", ")
                    v6AddrInjected = true
                }
                break
            }
        }
        return Result(
            patched: lines.joined(separator: "\n"),
            applied: patched,
            skippedReason: patched
                ? (v6AddrInjected ? "added ::/0 route + ULA v6 interface address" : "added ::/0 route (config already has a v6 interface address)")
                : "AllowedIPs already covers v6 or not found"
        )
    }

    /// Deterministic ULA /128 derived from a v4 host (e.g. 10.100.110.5/32 →
    /// fd00::a64:6e05/128). Stable per peer so the same source is used each
    /// connect; works only if the server accepts v6 from this peer.
    private static func deriveULA(fromV4 v4: String) -> String {
        let host = v4.split(separator: "/").first.map(String.init) ?? v4
        let octets = host.split(separator: ".").compactMap { UInt16($0) }
        guard octets.count == 4 else { return "fd00::1/128" }
        let hi = (octets[0] << 8) | octets[1]
        let lo = (octets[2] << 8) | octets[3]
        return String(format: "fd00::%x:%x/128", hi, lo)
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
