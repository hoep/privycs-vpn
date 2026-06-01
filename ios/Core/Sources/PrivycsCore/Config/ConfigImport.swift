import Foundation

/// Protocol detection + endpoint extraction shared by every import
/// path (file picker, QR scan, gateway pull). Port of the Android
/// `ConfigParser` heuristics.
public enum ConfigImport {

    /// Detect the protocol from filename extension first, then content.
    public static func detectProtocol(filename: String, content: String) -> VpnProtocol {
        let ext = (filename as NSString).pathExtension.lowercased()
        switch ext {
        case "ovpn": return .openvpn
        case "sswan", "mobileconfig", "p12": return .ipsec
        case "conf":
            return isAmneziaWG(content) ? .amneziawg : .wireguard
        default:
            break
        }
        // Content sniffing when the extension gives no clue.
        if content.contains("[Interface]") {
            return isAmneziaWG(content) ? .amneziawg : .wireguard
        }
        if content.range(of: #"(?m)^\s*remote\s"#, options: .regularExpression) != nil
            || content.contains("\nclient") {
            return .openvpn
        }
        if content.contains("\"remote\"") || content.contains("ikev2") {
            return .ipsec
        }
        return .wireguard
    }

    /// AmneziaWG is detected by its obfuscation keys in the [Interface]
    /// section (Jc/Jmin/Jmax/S1-4/H1-4/I1-5).
    public static func isAmneziaWG(_ content: String) -> Bool {
        return content.range(
            of: #"(?im)^\s*(Jc|Jmin|Jmax|S[1-4]|H[1-4]|I[1-5])\s*="#,
            options: .regularExpression
        ) != nil
    }

    /// Extract the server endpoint (host:port for WG, remote for OVPN,
    /// remote.addr for IPSec JSON).
    public static func extractServerAddress(_ content: String, _ proto: VpnProtocol) -> String {
        switch proto {
        case .wireguard, .amneziawg:
            for line in content.split(separator: "\n") {
                let l = line.trimmingCharacters(in: .whitespaces)
                if l.lowercased().hasPrefix("endpoint") {
                    return l.split(separator: "=").last
                        .map { $0.trimmingCharacters(in: .whitespaces) } ?? ""
                }
            }
        case .openvpn:
            for line in content.split(separator: "\n") {
                let l = line.trimmingCharacters(in: .whitespaces)
                if l.lowercased().hasPrefix("remote ") {
                    let parts = l.split(separator: " ").map(String.init)
                    if parts.count >= 2 {
                        return parts.count >= 3 ? "\(parts[1]):\(parts[2])" : parts[1]
                    }
                }
            }
        case .ipsec:
            // .sswan JSON — pull remote.addr without a full decode.
            if let p = try? SswanProfile.parse(content) {
                return p.remote.addr
            }
        }
        return ""
    }

    /// Build a SavedConnection from raw config content.
    public static func makeConnection(name: String, filename: String, content: String) -> SavedConnection {
        let proto = detectProtocol(filename: filename, content: content)
        let cfgID = UUID().uuidString
        let cfg = ProtocolConfig(
            id: cfgID,
            protocol: proto,
            filename: filename,
            configContent: content,
            serverAddress: extractServerAddress(content, proto)
        )
        return SavedConnection(
            id: UUID().uuidString,
            name: name,
            protocols: [cfg],
            activeConfigID: cfgID
        )
    }
}
