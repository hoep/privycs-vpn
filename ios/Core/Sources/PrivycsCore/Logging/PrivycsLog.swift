import Foundation
import os

/// Lightweight shared logger — writes to a capped file in the App
/// Group container so BOTH the main app and the PacketTunnelProvider
/// extension append to the same log, and the in-app Logs viewer can
/// read it (port of Android's file-backed privycs-vpn.log). Also tees
/// to os_log for Console.app.
public enum PrivycsLog {
    public static let appGroup = "group.com.privycs.vpn"
    private static let fileName = "privycs-vpn.log"
    private static let maxBytes = 256 * 1024   // 256 KB ring cap
    private static let logger = Logger(subsystem: "com.privycs.vpn", category: "app")
    private static let queue = DispatchQueue(label: "com.privycs.vpn.log", qos: .utility)
    /// Device-LOCAL timestamp formatter. A default ISO8601DateFormatter
    /// emits UTC/GMT; `.current` stamps log lines in the device's own zone
    /// (the offset is kept in the string so it stays unambiguous).
    private static let tsFormatter: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.timeZone = .current
        return f
    }()

    private static var fileURL: URL? {
        FileManager.default
            .containerURL(forSecurityApplicationGroupIdentifier: appGroup)?
            .appendingPathComponent(fileName)
    }

    /// Append a line (timestamped). Tees to os_log. Thread-safe.
    public static func log(_ message: String) {
        logger.log("\(message, privacy: .public)")
        queue.async {
            guard let url = fileURL else { return }
            let ts = tsFormatter.string(from: Date())
            let line = "\(ts)  \(message)\n"
            guard let data = line.data(using: .utf8) else { return }
            if let handle = try? FileHandle(forWritingTo: url) {
                defer { try? handle.close() }
                handle.seekToEndOfFile()
                handle.write(data)
            } else {
                try? data.write(to: url, options: .atomic)
            }
            trimIfNeeded(url)
        }
    }

    /// Returns the full log contents (most-recent at the bottom).
    public static func read() -> String {
        guard let url = fileURL,
              let data = try? Data(contentsOf: url),
              let str = String(data: data, encoding: .utf8) else { return "" }
        return str
    }

    public static func clear() {
        queue.async {
            guard let url = fileURL else { return }
            try? Data().write(to: url, options: .atomic)
        }
    }

    /// Keep the file under the ring cap by dropping the oldest half
    /// when it grows too large.
    private static func trimIfNeeded(_ url: URL) {
        guard let attrs = try? FileManager.default.attributesOfItem(atPath: url.path),
              let size = attrs[.size] as? Int, size > maxBytes else { return }
        guard let data = try? Data(contentsOf: url),
              let str = String(data: data, encoding: .utf8) else { return }
        let lines = str.split(separator: "\n", omittingEmptySubsequences: false)
        let kept = lines.suffix(lines.count / 2).joined(separator: "\n")
        try? kept.data(using: .utf8)?.write(to: url, options: .atomic)
    }
}
