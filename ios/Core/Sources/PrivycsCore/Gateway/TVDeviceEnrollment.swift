import Foundation

/// Device-code ("link a TV") enrollment client — the living-room counterpart
/// to QR/manual gateway enrollment. A TV has no camera (no QR scan) and a
/// hostile on-screen keyboard, so the user enters a short code on their phone
/// (Plex/Netflix/Disney+ style). Adapts OAuth 2.0 RFC 8628 Device
/// Authorization Grant, trimmed to our needs.
///
/// Endpoint spec: `GATEWAY_TASK_tv-device-enrollment.md`. The gateway side is
/// NOT implemented yet — until it lands, the tvOS app uses its manual
/// gateway-URL + token fallback. This client is written to the spec so it
/// works the moment the endpoints ship.
///
/// Pure Foundation/URLSession + async-await, **no NetworkExtension import**, so
/// PrivycsCore keeps building + unit-testing on Linux (and so Android TV can
/// mirror the same wire format). The `(gatewayURL, token)` pair this produces
/// is exactly what `GatewayAPIClient` consumes — the TV reuses the existing
/// config-pull path unchanged.
public actor TVDeviceEnrollment {

    private let baseURL: String
    private let session: URLSession

    /// `baseURL` is the gateway/site base the `/api/v1/tv/device/*` endpoints
    /// live under (e.g. `https://www.privycs.com`). It is a build-time constant
    /// for the TV apps — the TV does NOT know a gateway URL yet (that is what
    /// enrollment yields), so `start`/`poll` hit the public site.
    public init(baseURL: URL, session: URLSession = .shared) {
        var s = baseURL.absoluteString
        while s.hasSuffix("/") { s.removeLast() }
        self.baseURL = s
        self.session = session
    }

    // MARK: — Public API

    /// POST /api/v1/tv/device/start — no auth. Begins a link session and
    /// returns the short `userCode` to display + the opaque `deviceCode` to
    /// poll with.
    /// - Parameters:
    ///   - client: `"appletv"` or `"androidtv"`.
    ///   - appVersion: the app's marketing version, for the gateway's device label.
    public func start(client: TVClient, appVersion: String) async throws -> TVDeviceStart {
        struct Req: Encodable { let client: String; let app_version: String }
        let body = try JSONEncoder().encode(Req(client: client.rawValue, app_version: appVersion))
        let (data, resp) = try await request(path: "/api/v1/tv/device/start", method: "POST", body: body)
        guard let http = resp as? HTTPURLResponse else { throw GatewayError.invalidResponse }
        guard http.statusCode == 200 else {
            throw GatewayError.httpStatus(http.statusCode, Self.bodyString(data))
        }
        return try JSONDecoder().decode(TVDeviceStartDTO.self, from: data).model
    }

    /// POST /api/v1/tv/device/poll — no auth. Called on the gateway-supplied
    /// `interval` until it returns `.approved` (or fails). Maps the spec's
    /// status codes to a typed result so the caller never inspects raw HTTP.
    public func poll(deviceCode: String) async throws -> TVDevicePollResult {
        struct Req: Encodable { let device_code: String }
        let body = try JSONEncoder().encode(Req(device_code: deviceCode))
        let (data, resp) = try await request(path: "/api/v1/tv/device/poll", method: "POST", body: body)
        guard let http = resp as? HTTPURLResponse else { throw GatewayError.invalidResponse }
        switch http.statusCode {
        case 200:
            let dto = try JSONDecoder().decode(TVDevicePollApprovedDTO.self, from: data)
            return .approved(token: dto.token, gatewayURL: dto.gateway_url, label: dto.label ?? "")
        case 428:
            return .pending
        case 429:
            return .slowDown
        case 400, 410:
            // 400 expired_token / 410 expired (consumed or past TTL).
            return .expired
        default:
            throw GatewayError.httpStatus(http.statusCode, Self.bodyString(data))
        }
    }

    // MARK: — Private HTTP

    private func request(path: String, method: String, body: Data?) async throws -> (Data, URLResponse) {
        guard let url = URL(string: baseURL + path) else { throw GatewayError.invalidResponse }
        var req = URLRequest(url: url)
        req.httpMethod = method
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        if body != nil { req.setValue("application/json", forHTTPHeaderField: "Content-Type") }
        req.httpBody = body
        return try await session.data(for: req)
    }

    private static func bodyString(_ data: Data) -> String {
        String(data: data, encoding: .utf8) ?? "<binary>"
    }
}

// MARK: — Public model + DTOs

/// Which TV platform is enrolling — selects the gateway device-label prefix.
public enum TVClient: String, Sendable {
    case appletv
    case androidtv
}

/// Result of `start` — the bits the TV UI displays + the poll handle.
public struct TVDeviceStart: Equatable, Sendable {
    /// Opaque server-side handle to poll with (never shown to the user).
    public let deviceCode: String
    /// Short human-enterable code, e.g. "WDJB-MJHT".
    public let userCode: String
    /// URL the user opens on their phone, e.g. "https://www.privycs.com/link".
    public let verificationURI: String
    /// `verificationURI` with the code embedded — render as a QR the phone scans.
    public let verificationURIComplete: String
    /// Minimum seconds between `poll` calls.
    public let interval: Int
    /// Seconds the `userCode` + `deviceCode` stay valid.
    public let expiresIn: Int

    public init(deviceCode: String, userCode: String, verificationURI: String,
                verificationURIComplete: String, interval: Int, expiresIn: Int) {
        self.deviceCode = deviceCode
        self.userCode = userCode
        self.verificationURI = verificationURI
        self.verificationURIComplete = verificationURIComplete
        self.interval = interval
        self.expiresIn = expiresIn
    }
}

/// Outcome of one `poll`. `.approved` carries the `(gatewayURL, token)` pair
/// the TV stores + feeds straight into `GatewayAPIClient`.
public enum TVDevicePollResult: Equatable, Sendable {
    case pending
    case slowDown
    case expired
    case approved(token: String, gatewayURL: String, label: String)
}

/// Wire DTO for `/start`. Tolerant — `verification_uri_complete`/`interval`/
/// `expires_in` fall back to sane defaults so a minor schema drift doesn't
/// abort enrollment.
private struct TVDeviceStartDTO: Decodable {
    let device_code: String
    let user_code: String
    let verification_uri: String
    let verification_uri_complete: String?
    let interval: Int?
    let expires_in: Int?

    var model: TVDeviceStart {
        TVDeviceStart(
            deviceCode: device_code,
            userCode: user_code,
            verificationURI: verification_uri,
            verificationURIComplete: verification_uri_complete ?? verification_uri,
            interval: interval ?? 5,
            expiresIn: expires_in ?? 600
        )
    }
}

/// Wire DTO for an approved `/poll` (HTTP 200).
private struct TVDevicePollApprovedDTO: Decodable {
    let token: String
    let gateway_url: String
    let label: String?
}
