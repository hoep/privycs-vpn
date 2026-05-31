import Foundation

/// HTTP client für die Privycs Gateway API (Pro tier "pull configs
/// from gateway" Feature). Mirror der Android `GatewayApiClient` +
/// Desktop api_client.go. URLSession-based, async/await, kein
/// 3rd-party HTTP-lib.
///
/// Auth: Bearer-Token (API-Key aus Settings/Keychain).
public actor GatewayAPIClient {

    private let gatewayURL: URL
    private let apiKey: String
    private let session: URLSession

    public init(gatewayURL: URL, apiKey: String, session: URLSession = .shared) {
        self.gatewayURL = gatewayURL
        self.apiKey = apiKey
        self.session = session
    }

    /// GET /api/v1/connect/my-configs
    /// Listet alle verfügbaren Configs, gruppiert nach Protokoll.
    public func listMyConfigs() async throws -> [RemoteConfigEntry] {
        let response: MyConfigsResponse = try await get("/api/v1/connect/my-configs")
        return response.configs
    }

    /// GET /api/v1/connect/my-configs/<protocol>-<id>
    /// Lädt den raw Config-Content für eine einzelne Entry.
    public func fetchConfig(entry: RemoteConfigEntry) async throws -> String {
        let path = "/api/v1/connect/my-configs/\(entry.protocol.rawValue)-\(entry.id)"
        let (data, response) = try await request(path: path, method: "GET", body: nil)
        try ensureSuccess(response: response, data: data)
        return String(data: data, encoding: .utf8) ?? ""
    }

    /// POST /api/v1/license/redeem-apple
    /// Tauscht ein StoreKit-Receipt gegen eine signed ed25519
    /// License-Key (Cross-platform-Redeem auf andere Devices).
    public func redeemAppleReceipt(_ receiptBase64: String) async throws -> String {
        struct Body: Codable { let receipt: String }
        struct Resp: Codable { let license: String }
        let resp: Resp = try await post("/api/v1/license/redeem-apple", body: Body(receipt: receiptBase64))
        return resp.license
    }

    // MARK: — Private HTTP helpers

    private func get<T: Decodable>(_ path: String) async throws -> T {
        let (data, resp) = try await request(path: path, method: "GET", body: nil)
        try ensureSuccess(response: resp, data: data)
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func post<B: Encodable, T: Decodable>(_ path: String, body: B) async throws -> T {
        let bodyData = try JSONEncoder().encode(body)
        let (data, resp) = try await request(path: path, method: "POST", body: bodyData)
        try ensureSuccess(response: resp, data: data)
        return try JSONDecoder().decode(T.self, from: data)
    }

    private func request(path: String, method: String, body: Data?) async throws -> (Data, URLResponse) {
        let url = gatewayURL.appendingPathComponent(path)
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

/// Eintrag der Gateway-Configs-Liste. Mirror der Android
/// `RemoteConfigEntry`.
public struct RemoteConfigEntry: Codable, Identifiable, Hashable, Sendable {
    public let id: Int
    public let `protocol`: VpnProtocol
    public let name: String
    public let serverAddress: String

    private enum CodingKeys: String, CodingKey {
        case id, `protocol`, name
        case serverAddress = "server_address"
    }
}

struct MyConfigsResponse: Codable {
    let configs: [RemoteConfigEntry]
}
