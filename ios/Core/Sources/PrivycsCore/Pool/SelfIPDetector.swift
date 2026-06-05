import Foundation

/// Detects the user's pre-VPN public IP → country. Cross-platform mirror of the
/// desktop `selfip` package and Android `SelfIpDetector`: probes a few plain-
/// HTTPS IP-echo endpoints (first parseable IP wins), resolves it through the
/// bundled MMDB (`MmdbCountryResolver`), and caches the country for 1 hour.
///
/// Used by the Smart Decision Engine to recommend AmneziaWG on restrictive
/// networks. Returns "" on any failure (no internet / captive portal / all
/// probes timed out) — the caller then makes no censorship claim.
public actor SelfIPDetector {
    public static let shared = SelfIPDetector()

    private var cachedCountry = ""
    private var cachedAt: Date?
    private let ttl: TimeInterval = 3600

    private let session: URLSession = {
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 3
        cfg.waitsForConnectivity = false
        return URLSession(configuration: cfg)
    }()

    public init() {}

    /// Cached country if fresh, else probes. ISO-3166-1 alpha-2 (e.g. "CN"), "" if unknown.
    public func country() async -> String {
        if let at = cachedAt, Date().timeIntervalSince(at) < ttl, !cachedCountry.isEmpty {
            return cachedCountry
        }
        let ip = await probeIP()
        let cc = ip.flatMap { MmdbCountryResolver.shared?.country(forIP: $0) } ?? ""
        cachedCountry = cc
        cachedAt = Date()
        return cc
    }

    /// Drops the cache (e.g. on network change) so the next country() reprobes.
    public func invalidate() {
        cachedAt = nil
        cachedCountry = ""
    }

    private func probeIP() async -> String? {
        let probes: [(String, (String) -> String?)] = [
            // Cloudflare trace: multi-line key=value with an "ip=" line.
            ("https://1.1.1.1/cdn-cgi/trace", { body in
                body.split(separator: "\n").first { $0.hasPrefix("ip=") }
                    .map { String($0.dropFirst(3)).trimmingCharacters(in: .whitespaces) }
            }),
            // ipify / ifconfig return the bare IP literal.
            ("https://api.ipify.org", { $0.trimmingCharacters(in: .whitespacesAndNewlines) }),
            ("https://ifconfig.me/ip", { $0.trimmingCharacters(in: .whitespacesAndNewlines) }),
        ]
        for (urlStr, parse) in probes {
            guard let url = URL(string: urlStr) else { continue }
            do {
                let (data, resp) = try await session.data(from: url)
                guard (resp as? HTTPURLResponse)?.statusCode == 200,
                      let body = String(data: data, encoding: .utf8),
                      let ip = parse(body), Self.isIPLiteral(ip) else { continue }
                return ip
            } catch {
                continue
            }
        }
        return nil
    }

    private static func isIPLiteral(_ s: String) -> Bool {
        if s.contains(":") { return true } // IPv6
        let parts = s.split(separator: ".")
        return parts.count == 4 && parts.allSatisfy { (Int($0).map { $0 >= 0 && $0 <= 255 }) ?? false }
    }
}
