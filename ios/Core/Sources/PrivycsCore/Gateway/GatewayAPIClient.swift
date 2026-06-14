import Foundation

/// HTTP client für die Privycs Gateway API (Pro tier "pull configs
/// from gateway"). Mirror der Android `GatewayApiClient` + Desktop
/// api_client.go. URLSession-based, async/await, Bearer-Token-Auth.
///
/// Wire format matches the gateway `cmd/gateway/connect_my_configs_api.go`:
///   GET /api/v1/connect/my-configs
///     → { success, user, configs: [MyConfigEntry], count }
///   GET /api/v1/connect/my-configs/<protocol>-<id>
///     → WireGuard/OpenVPN: { success, protocol, name, filename, config }
///       (config = the rendered .conf / .ovpn text)
///     → IPSec (with ?format=sswan): the raw .sswan JSON profile
public actor GatewayAPIClient {

    private let baseURL: String
    private let apiKey: String
    private let session: URLSession

    public init(gatewayURL: URL, apiKey: String, session: URLSession = .shared) {
        // Normalise: drop a trailing slash so path concatenation is clean.
        var s = gatewayURL.absoluteString
        while s.hasSuffix("/") { s.removeLast() }
        self.baseURL = s
        self.apiKey = apiKey
        self.session = session
    }

    /// GET /api/v1/connect/my-configs — the user's available configs.
    public func listMyConfigs() async throws -> [RemoteConfigEntry] {
        let (data, resp) = try await request(path: "/api/v1/connect/my-configs", method: "GET", body: nil)
        try ensureSuccess(response: resp, data: data)
        let decoded = try JSONDecoder().decode(MyConfigsResponse.self, from: data)
        return decoded.configs
    }

    /// Download + return the raw config text for one entry, rendered to
    /// the format the importer expects (.conf / .ovpn / .sswan). Ports
    /// the proven Android GatewayApiClient.fetchConfig + buildWireGuardConf
    /// / extractOpenVpnConfig logic 1:1.
    public func fetchConfig(entry: RemoteConfigEntry) async throws -> String {
        let proto = entry.protocolRaw
        // IPSec: request the Android .sswan JSON (default ios format is a
        // .mobileconfig signed plist our importer can't parse). Returned
        // verbatim — SswanProfile.parse consumes it.
        let path = proto == "ipsec"
            ? "/api/v1/connect/my-configs/ipsec-\(entry.id)?format=sswan"
            : "/api/v1/connect/my-configs/\(proto)-\(entry.id)"
        let (data, resp) = try await request(path: path, method: "GET", body: nil)
        try ensureSuccess(response: resp, data: data)

        switch proto {
        case "wireguard", "amneziawg":
            // `config` is a JSON OBJECT (peer_private_key, server_endpoint,
            // …) — render the wg-quick .conf client-side, like Android.
            return try Self.buildWireGuardConf(data)
        case "openvpn":
            // `config` is a STRING (the .ovpn text).
            return try Self.extractStringConfig(data)
        default: // ipsec + anything else: raw body
            return String(data: data, encoding: .utf8) ?? ""
        }
    }

    /// Render a wg-quick .conf from the gateway's WireGuard download JSON.
    /// 1:1 port of Android buildWireGuardConf — tolerant of numeric fields
    /// arriving as either JSON numbers or strings.
    static func buildWireGuardConf(_ data: Data) throws -> String {
        guard let root = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let config = root["config"] as? [String: Any] else {
            throw GatewayError.httpStatus(0, "Config not available")
        }
        func str(_ k: String) -> String {
            if let s = config[k] as? String { return s }
            if let n = config[k] as? NSNumber { return n.stringValue }
            return ""
        }
        func intVal(_ k: String, _ def: Int) -> Int {
            if let n = config[k] as? NSNumber { return n.intValue }
            if let s = config[k] as? String, let v = Int(s) { return v }
            return def
        }
        let privateKey = str("peer_private_key")
        guard !privateKey.isEmpty else {
            throw GatewayError.httpStatus(0, "Config not available (private key missing)")
        }
        let address = str("peer_address")
        let serverPublicKey = str("server_public_key")
        let presharedKey = str("preshared_key")
        let serverEndpoint = str("server_endpoint")
        let serverPort = intVal("server_port", 51820)
        let allowedIPs = { let a = str("allowed_ips"); return a.isEmpty ? "0.0.0.0/0" : a }()
        let dns = str("dns")
        let mtu = intVal("mtu", 0)
        let keepalive = intVal("persistent_keepalive", 25)
        let obfuscationLines = str("obfuscation_config_lines")

        var s = "[Interface]\n"
        s += "PrivateKey = \(privateKey)\n"
        s += "Address = \(address)\n"
        if !dns.isEmpty { s += "DNS = \(dns)\n" }
        if mtu > 0 { s += "MTU = \(mtu)\n" }
        if !obfuscationLines.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            s += obfuscationLines.trimmingCharacters(in: .whitespacesAndNewlines) + "\n"
        }
        s += "\n[Peer]\n"
        s += "PublicKey = \(serverPublicKey)\n"
        if !presharedKey.isEmpty { s += "PresharedKey = \(presharedKey)\n" }
        s += "Endpoint = \(serverEndpoint):\(serverPort)\n"
        s += "AllowedIPs = \(allowedIPs)\n"
        s += "PersistentKeepalive = \(keepalive)\n"
        return s
    }

    /// Extract the `config` string field (OpenVPN .ovpn text).
    static func extractStringConfig(_ data: Data) throws -> String {
        guard let root = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let cfg = root["config"] as? String, !cfg.isEmpty else {
            throw GatewayError.httpStatus(0, "Config not available")
        }
        return cfg
    }

    /// POST /api/v1/license/redeem-apple — exchange a StoreKit signed
    /// transaction for an ed25519 cross-platform license.
    public func redeemAppleReceipt(_ receiptBase64: String) async throws -> String {
        struct Body: Codable { let receipt: String }
        struct Resp: Codable { let license: String }
        let resp: Resp = try await post("/api/v1/license/redeem-apple", body: Body(receipt: receiptBase64))
        return resp.license
    }

    // MARK: — Private HTTP helpers

    private func post<B: Encodable, T: Decodable>(_ path: String, body: B) async throws -> T {
        let bodyData = try JSONEncoder().encode(body)
        let (data, resp) = try await request(path: path, method: "POST", body: bodyData)
        try ensureSuccess(response: resp, data: data)
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func request(path: String, method: String, body: Data?) async throws -> (Data, URLResponse) {
        // String-concat (not appendingPathComponent) so query strings like
        // "?format=sswan" survive un-escaped.
        guard let url = URL(string: baseURL + path) else {
            throw GatewayError.invalidResponse
        }
        var req = URLRequest(url: url)
        req.httpMethod = method
        req.setValue("Bearer \(apiKey)", forHTTPHeaderField: "Authorization")
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        if body != nil {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        req.httpBody = body
        return try await session.data(for: req)
    }

    private func ensureSuccess(response: URLResponse, data: Data) throws {
        guard let http = response as? HTTPURLResponse else {
            throw GatewayError.invalidResponse
        }
        guard (200..<300).contains(http.statusCode) else {
            let message = String(data: data, encoding: .utf8) ?? "<binary>"
            throw GatewayError.httpStatus(http.statusCode, message)
        }
    }
}

public enum GatewayError: Error, LocalizedError {
    case invalidResponse
    case httpStatus(Int, String)

    public var errorDescription: String? {
        switch self {
        case .invalidResponse: return "Invalid HTTP response"
        case .httpStatus(let code, let message): return "Gateway HTTP \(code): \(message)"
        }
    }
}

/// One entry from /api/v1/connect/my-configs. Matches the gateway's
/// `MyConfigEntry` JSON 1:1. Robust decode — only `id` is required so a
/// schema drift on a secondary field doesn't abort the whole list.
public struct RemoteConfigEntry: Codable, Identifiable, Hashable, Sendable {
    public let id: Int
    public let peerName: String
    /// Raw protocol string from the gateway ("wireguard"/"openvpn"/"ipsec").
    /// Used verbatim for the download path.
    public let protocolRaw: String
    public let interfaceName: String
    public let vpnIP: String
    public let obfuscationEnabled: Bool

    private enum CodingKeys: String, CodingKey {
        case id
        case peerName = "peer_name"
        case protocolRaw = "protocol"
        case interfaceName = "interface_name"
        case vpnIP = "vpn_ip"
        case obfuscationEnabled = "obfuscation_enabled"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(Int.self, forKey: .id)
        peerName = (try? c.decode(String.self, forKey: .peerName)) ?? ""
        protocolRaw = (try? c.decode(String.self, forKey: .protocolRaw)) ?? "wireguard"
        interfaceName = (try? c.decode(String.self, forKey: .interfaceName)) ?? ""
        vpnIP = (try? c.decode(String.self, forKey: .vpnIP)) ?? ""
        obfuscationEnabled = (try? c.decode(Bool.self, forKey: .obfuscationEnabled)) ?? false
    }

    /// Display name for the UI.
    public var name: String { peerName.isEmpty ? "config-\(id)" : peerName }

    /// Best-effort server display (the assigned VPN IP).
    public var serverAddress: String { vpnIP }

    /// Protocol for UI badges — an obfuscated WireGuard peer reads as
    /// AmneziaWG (matches how the importer will detect the rendered .conf).
    public var `protocol`: VpnProtocol {
        if protocolRaw == "wireguard" && obfuscationEnabled { return .amneziawg }
        return VpnProtocol(rawValue: protocolRaw) ?? .wireguard
    }
}

struct MyConfigsResponse: Decodable {
    let configs: [RemoteConfigEntry]
}
