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

    /// Extract every supported config file from a .zip archive's bytes.
    /// Directories, oversized entries and non-UTF8 files are skipped.
    public static func extractZip(_ data: Data) -> [ExtractedConfig] {
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
}
