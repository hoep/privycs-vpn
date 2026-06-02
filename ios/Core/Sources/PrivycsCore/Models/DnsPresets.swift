import Foundation

/// Canonical DNS-provider presets — 1:1 port of the Android
/// `DnsValidator.providers` list (itself kept in sync with the desktop
/// `GetDnsProviders`). Drives the reusable DNS-override field on iOS
/// (global Settings, per-connection, per-pool) so the option set,
/// ordering and dual-stack server values stay identical across platforms.
public struct DnsProvider: Identifiable, Equatable, Hashable, Sendable {
    public let id: String
    public let label: String
    public let servers: [String]
    public let dotHost: String
    public let note: String

    /// Comma-separated dual-stack server list, ready to drop into an
    /// override field (matches Android's `servers.joinToString(", ")`).
    public var serversJoined: String { servers.joined(separator: ", ") }

    public init(id: String, label: String, servers: [String], dotHost: String, note: String) {
        self.id = id
        self.label = label
        self.servers = servers
        self.dotHost = dotHost
        self.note = note
    }
}

public enum DnsPresets {
    public static let providers: [DnsProvider] = [
        DnsProvider(id: "cloudflare", label: "Cloudflare",
            servers: ["1.1.1.1", "1.0.0.1", "2606:4700:4700::1111", "2606:4700:4700::1001"],
            dotHost: "cloudflare-dns.com", note: "Fast, no logging beyond 24h"),
        DnsProvider(id: "cloudflare-malware", label: "Cloudflare (block malware)",
            servers: ["1.1.1.2", "1.0.0.2", "2606:4700:4700::1112", "2606:4700:4700::1002"],
            dotHost: "security.cloudflare-dns.com", note: "Blocks known malware domains"),
        DnsProvider(id: "cloudflare-family", label: "Cloudflare (block malware + adult)",
            servers: ["1.1.1.3", "1.0.0.3", "2606:4700:4700::1113", "2606:4700:4700::1003"],
            dotHost: "family.cloudflare-dns.com", note: "Family-safe filtering"),
        DnsProvider(id: "google", label: "Google",
            servers: ["8.8.8.8", "8.8.4.4", "2001:4860:4860::8888", "2001:4860:4860::8844"],
            dotHost: "dns.google", note: "Reliable, logs queries"),
        DnsProvider(id: "quad9", label: "Quad9 (block malware)",
            servers: ["9.9.9.9", "149.112.112.112", "2620:fe::fe", "2620:fe::9"],
            dotHost: "dns.quad9.net", note: "Swiss, malware blocking, no logging"),
        DnsProvider(id: "adguard", label: "AdGuard (block ads + trackers)",
            servers: ["94.140.14.14", "94.140.15.15", "2a10:50c0::ad1:ff", "2a10:50c0::ad2:ff"],
            dotHost: "dns.adguard-dns.com", note: "Default - blocks ads and trackers"),
        DnsProvider(id: "adguard-family", label: "AdGuard Family (block ads + trackers + adult)",
            servers: ["94.140.14.15", "94.140.15.16", "2a10:50c0::bad1:ff", "2a10:50c0::bad2:ff"],
            dotHost: "family.adguard-dns.com", note: "Family-safe content filtering on top of ad blocking"),
        DnsProvider(id: "adguard-unfiltered", label: "AdGuard (no filtering)",
            servers: ["94.140.14.140", "94.140.14.141", "2a10:50c0::1:ff", "2a10:50c0::2:ff"],
            dotHost: "unfiltered.adguard-dns.com", note: "Pass-through, no blocking"),
        DnsProvider(id: "mullvad", label: "Mullvad",
            servers: ["194.242.2.2", "2a07:e340::2"],
            dotHost: "dns.mullvad.net", note: "Logging-free, run by Mullvad VPN"),
        DnsProvider(id: "mullvad-adblock", label: "Mullvad (block ads + trackers)",
            servers: ["194.242.2.3", "2a07:e340::3"],
            dotHost: "adblock.dns.mullvad.net", note: "Mullvad with content blocking"),
    ]

    /// Best-matching provider for an override string (every parsed server
    /// belongs to the provider's canonical set). Mirrors Android
    /// `DnsValidator.detectProvider`. Used to badge a known resolver.
    public static func detect(_ input: String) -> DnsProvider? {
        let parsed = Set(input
            .split(whereSeparator: { $0 == "," || $0 == " " })
            .map { $0.trimmingCharacters(in: .whitespaces).lowercased() }
            .filter { !$0.isEmpty })
        guard !parsed.isEmpty else { return nil }
        return providers.first { p in
            let canon = Set(p.servers.map { $0.lowercased() })
            return parsed.isSubset(of: canon)
        }
    }
}
