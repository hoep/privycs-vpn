import Foundation

/// Privycs QR-code payload formats. 1:1 mirror of the Android
/// `QrCodePayload` / `parseQrPayload` logic:
///
///   1. Privycs enrollment URL — the `privycs://enroll?...` scheme with
///      query params (url/gateway, apikey/token, …). The QR points the
///      client at a gateway to pull configs from; required for
///      OpenVPN/IPSec (too large for a QR) and an alternative for WG.
///   2. Raw wg-quick .conf — the standard WireGuard QR (`[Interface]`
///      first non-comment line). AmneziaWG if it carries AWG keys.
public enum QRPayload: Sendable {
    case wireguard(String)
    case amneziawg(String)
    case openvpn(String)
    case privycsEnrollment(gatewayURL: URL, apiKey: String)

    public static func parse(_ raw: String) -> QRPayload? {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)

        // 1. Privycs custom URL scheme: privycs://enroll?url=…&apikey=…
        //    (accepts the gateway/token aliases too, like Android).
        if trimmed.lowercased().hasPrefix("privycs://"),
           let comps = URLComponents(string: trimmed),
           comps.host?.lowercased() == "enroll" {
            let q = comps.queryItems ?? []
            func param(_ keys: [String]) -> String? {
                for k in keys {
                    if let v = q.first(where: { $0.name.lowercased() == k })?.value, !v.isEmpty {
                        return v
                    }
                }
                return nil
            }
            if let urlStr = param(["url", "gateway"]),
               let url = URL(string: urlStr),
               let apiKey = param(["apikey", "token"]) {
                return .privycsEnrollment(gatewayURL: url, apiKey: apiKey)
            }
            return nil
        }

        // 2. Raw wg-quick INI — [Interface] as the first non-comment line.
        let firstLine = trimmed
            .split(separator: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .first { !$0.isEmpty && !$0.hasPrefix("#") }
        if firstLine?.caseInsensitiveCompare("[Interface]") == .orderedSame {
            return ConfigImport.isAmneziaWG(trimmed) ? .amneziawg(trimmed) : .wireguard(trimmed)
        }

        // 3. Raw OpenVPN (uncommon in a QR, but accept it).
        if trimmed.range(of: #"(?m)^\s*remote\s"#, options: .regularExpression) != nil {
            return .openvpn(trimmed)
        }

        return nil
    }
}
