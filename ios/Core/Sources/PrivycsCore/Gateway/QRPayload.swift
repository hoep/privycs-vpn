import Foundation

/// Privycs QR-code payload formats. Mirror der Android `QRPayload`
/// parsing-logic. Drei mögliche Payload-typen:
///
///   1. WireGuard / AmneziaWG: roh wg-quick INI text.
///   2. OpenVPN: roh .ovpn text.
///   3. Privycs Enrollment: JSON
///      `{"kind":"privycs","gateway_url":"...","api_key":"..."}`
///      — automatisch konfiguriert Gateway+API-Key.
public enum QRPayload: Sendable {
    case wireguard(String)
    case amneziawg(String)
    case openvpn(String)
    case privycsEnrollment(gatewayURL: URL, apiKey: String)

    public static func parse(_ raw: String) -> QRPayload? {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)

        // 1. Privycs enrollment JSON?
        if let data = trimmed.data(using: .utf8),
           let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
           let kind = json["kind"] as? String, kind == "privycs",
           let urlStr = json["gateway_url"] as? String,
           let url = URL(string: urlStr),
           let apiKey = json["api_key"] as? String {
            return .privycsEnrollment(gatewayURL: url, apiKey: apiKey)
        }

        // 2. wg-quick INI? Look for [Interface] + [Peer]
        if trimmed.contains("[Interface]") && trimmed.contains("[Peer]") {
            if trimmed.contains("Jc =") || trimmed.contains("S1 =") {
                return .amneziawg(trimmed)
            }
            return .wireguard(trimmed)
        }

        // 3. OpenVPN? Look for "client" + "remote ..." lines
        if trimmed.contains("client") && trimmed.range(of: #"^remote\s"#, options: [.regularExpression, .anchored]) != nil {
            return .openvpn(trimmed)
        }
        if trimmed.contains("\nremote ") || trimmed.hasPrefix("remote ") {
            return .openvpn(trimmed)
        }

        return nil
    }
}
