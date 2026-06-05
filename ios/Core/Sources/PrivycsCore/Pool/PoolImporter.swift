import Foundation
import ZIPFoundation

/// Bulk pool import — extracts config files from a provider .zip archive
/// (or accepts loose files) and builds PoolMembers. Port of Android's
/// PoolImporter (which also supports ZIP). Geo country is parsed from the
/// "<cc>-<city3>-…" filename convention as a best-effort fallback.
public enum PoolImporter {

    public struct ExtractedConfig: Equatable, Sendable {
        public let filename: String
        public let content: String
        public init(filename: String, content: String) {
            self.filename = filename; self.content = content
        }
    }

    static let maxConfigBytes = 1_048_576   // 1 MB/file (ZIP-bomb guard, Android parity)

    /// True for a supported config file extension.
    public static func isConfigFile(_ name: String) -> Bool {
        let l = name.lowercased()
        return l.hasSuffix(".conf") || l.hasSuffix(".ovpn")
            || l.hasSuffix(".sswan") || l.hasSuffix(".mobileconfig")
    }

    /// Extract configs from ONE imported file's bytes. A ZIP — detected by its
    /// MAGIC BYTES, not the filename extension (the file importer can hand back
    /// a URL without a `.zip` suffix) — is expanded into all member configs;
    /// otherwise the bytes are a single config when the name has a supported
    /// extension. This is the entry point callers should use.
    public static func extractConfigs(fromFileData data: Data, filename: String) -> [ExtractedConfig] {
        if MiniZip.looksLikeZip(data) || filename.lowercased().hasSuffix(".zip") {
            return extractZip(data)
        }
        let name = (filename as NSString).lastPathComponent
        if isConfigFile(name), let s = String(data: data, encoding: .utf8), !s.isEmpty {
            return [ExtractedConfig(filename: name, content: s)]
        }
        return []
    }

    /// Extract every supported config file from a .zip archive's bytes.
    /// Uses the dependency-free `MiniZip` reader first (ZIPFoundation silently
    /// returned zero entries for some provider archives → "keine gültige
    /// config"); ZIPFoundation is only a fallback when the primary finds nothing.
    public static func extractZip(_ data: Data) -> [ExtractedConfig] {
        let primary = miniExtract(data)
        return primary.isEmpty ? zipFoundationExtract(data) : primary
    }

    private static func miniExtract(_ data: Data) -> [ExtractedConfig] {
        var out: [ExtractedConfig] = []
        for entry in MiniZip.entries(data) {
            let name = (entry.name as NSString).lastPathComponent
            guard isConfigFile(name), entry.data.count <= maxConfigBytes else { continue }
            if let s = String(data: entry.data, encoding: .utf8), !s.isEmpty {
                out.append(ExtractedConfig(filename: name, content: s))
            }
        }
        return out
    }

    private static func zipFoundationExtract(_ data: Data) -> [ExtractedConfig] {
        guard let archive = try? Archive(data: data, accessMode: .read) else { return [] }
        var out: [ExtractedConfig] = []
        for entry in archive where entry.type == .file {
            let name = (entry.path as NSString).lastPathComponent
            guard isConfigFile(name), entry.uncompressedSize <= UInt64(maxConfigBytes) else { continue }
            var buf = Data()
            _ = try? archive.extract(entry) { buf.append($0) }
            if let s = String(data: buf, encoding: .utf8), !s.isEmpty {
                out.append(ExtractedConfig(filename: name, content: s))
            }
        }
        return out
    }

    /// Build PoolMembers from extracted configs (protocol detected, geo
    /// country parsed from the filename, server address extracted).
    public static func makeMembers(_ configs: [ExtractedConfig], startIndex: Int = 0) -> [PoolMember] {
        configs.enumerated().map { (i, c) in
            let proto = ConfigImport.detectProtocol(filename: c.filename, content: c.content)
            return PoolMember(
                id: UUID().uuidString,
                name: (c.filename as NSString).deletingPathExtension,
                country: parseCountry(c.filename),
                region: "",
                index: startIndex + i,
                protocol: proto,
                configContent: c.content,
                serverAddress: ConfigImport.extractServerAddress(c.content, proto)
            )
        }
    }

    /// First "<cc>-…" segment, when it's a 2-letter code → uppercased.
    public static func parseCountry(_ filename: String) -> String {
        let base = (filename as NSString).deletingPathExtension
        let first = base.split(separator: "-", omittingEmptySubsequences: false)
            .first.map(String.init) ?? ""
        return first.count == 2 ? first.uppercased() : ""
    }

    /// Fill in each member's `country` (where the filename didn't already
    /// give one) by geolocating its server endpoint via the bundled
    /// `country.mmdb` — resolving a hostname to an IP first if needed.
    /// Mirrors Android's CombinedCountryResolver (IP-geo + filename fallback).
    public static func enrichCountries(_ members: [PoolMember]) async -> [PoolMember] {
        guard let mmdb = MmdbCountryResolver.shared else { return members }
        var out = members
        for i in out.indices where out[i].country.isEmpty {
            let host = endpointHost(out[i].serverAddress)
            guard !host.isEmpty else { continue }
            if let ip = await firstIP(host), let cc = mmdb.country(forIP: ip) {
                out[i].country = cc
            }
        }
        return out
    }

    /// Host part of a "host:port" / "[v6]:port" endpoint.
    public static func endpointHost(_ s: String) -> String {
        let t = s.trimmingCharacters(in: .whitespaces)
        if t.hasPrefix("[") {
            if let close = t.firstIndex(of: "]") { return String(t[t.index(after: t.startIndex)..<close]) }
            return t
        }
        let parts = t.split(separator: ":")
        return parts.count == 2 ? String(parts[0]) : t   // host:port → host (bare v6 untouched)
    }

    /// Resolve a host (an IP literal returns itself) to its first numeric IP.
    public static func firstIP(_ host: String) async -> String? {
        await withCheckedContinuation { (cont: CheckedContinuation<String?, Never>) in
            DispatchQueue.global().async {
                var res: UnsafeMutablePointer<addrinfo>?
                guard getaddrinfo(host, nil, nil, &res) == 0, let first = res else {
                    cont.resume(returning: nil); return
                }
                defer { freeaddrinfo(res) }
                var buf = [CChar](repeating: 0, count: Int(NI_MAXHOST))
                let ok = getnameinfo(first.pointee.ai_addr, first.pointee.ai_addrlen,
                                     &buf, socklen_t(buf.count), nil, 0, NI_NUMERICHOST) == 0
                cont.resume(returning: ok ? String(cString: buf) : nil)
            }
        }
    }
}
