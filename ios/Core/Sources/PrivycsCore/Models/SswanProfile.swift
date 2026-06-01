import Foundation

/// strongSwan Android-style `.sswan` JSON profile — the format the
/// Privycs gateway emits for IPSec/IKEv2 (see gateway
/// `cmd/gateway/ipsec_mobile_profiles.go` SSWANProfile). On iOS we do
/// NOT run strongSwan; instead we map these fields onto the built-in
/// `NEVPNProtocolIKEv2` (certificate auth with inline PKCS#12). The
/// embedded `local.p12` is a base64 PKCS#12 bundle (client cert +
/// private key + CA chain), unlocked with `local.p12-password`.
public struct SswanProfile: Codable, Equatable, Sendable {
    public let uuid: String
    public let name: String
    /// e.g. "ikev2-cert". Drives the auth method.
    public let type: String
    public let remote: Remote
    public let local: Local
    public let mtu: Int?
    public let splitTunneling: [String]?
    public let dnsServers: [String]?
    /// RFC 8784 post-quantum PPK material (optional). NEVPNProtocolIKEv2
    /// has no public PPK API, so these are parsed-but-ignored on iOS.
    public let ppkID: String?
    public let ppkPSK: String?
    /// Optional pre-signed .mobileconfig (gateway-signed) — when present
    /// the app can install it directly instead of building the IKEv2
    /// config field-by-field. Base64.
    public let macosSignedProfile: String?

    public struct Remote: Codable, Equatable, Sendable {
        public let addr: String
        public let id: String?
    }

    public struct Local: Codable, Equatable, Sendable {
        public let id: String?
        /// Base64-encoded PKCS#12 (client cert + key + CA chain).
        public let p12: String?
        public let p12Password: String?

        private enum CodingKeys: String, CodingKey {
            case id
            case p12
            case p12Password = "p12-password"
        }
    }

    private enum CodingKeys: String, CodingKey {
        case uuid, name, type, remote, local, mtu
        case splitTunneling = "split-tunneling"
        case dnsServers = "dns-servers"
        case ppkID = "ppk_id"
        case ppkPSK = "ppk_psk"
        case macosSignedProfile = "macos_signed_profile"
    }

    /// Parse a raw `.sswan` JSON string.
    public static func parse(_ raw: String) throws -> SswanProfile {
        guard let data = raw.data(using: .utf8) else {
            throw SswanError.notUTF8
        }
        let decoder = JSONDecoder()
        do {
            return try decoder.decode(SswanProfile.self, from: data)
        } catch {
            throw SswanError.malformed(String(describing: error))
        }
    }

    /// Decoded PKCS#12 bytes for `NEVPNProtocolIKEv2.identityData`.
    /// Nil when no client cert is embedded (pure-EAP profiles).
    public var pkcs12Data: Data? {
        guard let p12 = local.p12, !p12.isEmpty else { return nil }
        // Tolerate whitespace/newlines in the base64 blob.
        let cleaned = p12.filter { !$0.isWhitespace }
        return Data(base64Encoded: cleaned)
    }

    /// Server identity for `remoteIdentifier` — explicit id, else the
    /// server address (matches the TLS/cert SAN the gateway issues).
    public var resolvedRemoteIdentifier: String {
        if let id = remote.id, !id.isEmpty { return id }
        return remote.addr
    }
}

public enum SswanError: LocalizedError {
    case notUTF8
    case malformed(String)
    case missingCertificate

    public var errorDescription: String? {
        switch self {
        case .notUTF8: return "IPSec profile is not valid UTF-8"
        case .malformed(let detail): return "IPSec profile is malformed: \(detail)"
        case .missingCertificate: return "IPSec profile has no client certificate (p12)"
        }
    }
}
