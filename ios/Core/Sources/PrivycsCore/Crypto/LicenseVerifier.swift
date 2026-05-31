import Foundation
import Crypto

/// ed25519-signed offline License-Key-Verifier. Cross-platform-
/// kompatibel mit Android- und Desktop-Implementation: gleicher
/// Public-Key, gleiches Payload-Format, gleicher Encoding-Tanz.
///
/// **Format des License-Key-Strings**:
/// ```
/// <base64(JSON_payload)>.<base64(ed25519_signature_64bytes)>
/// ```
/// JSON_payload Felder (snake_case):
/// - `tier`: "single_android" | "single_desktop" | "single_ios" | "cross_platform_bundle"
/// - `email`: User-Email des Käufers (für UI-Display + dispute-trace)
/// - `purchase_id`: opaker String vom Store/LemonSqueezy
/// - `issued_at`: UNIX-Timestamp Sekunden
/// - `not_after`: optional, 0 oder absent = lifetime
///
/// Eine valide License hat:
/// 1. Korrekte Signatur gegen den hardcoded Pubkey
/// 2. `tier` matched die aktuelle Platform-Erwartung (`single_ios`
///    oder `cross_platform_bundle`) — andere Tiers werden
///    abgewiesen
/// 3. `not_after` (wenn gesetzt) liegt in der Zukunft
public struct LicenseVerifier: Sendable {

    /// ed25519 Public Key (32 bytes raw). MUST match desktop's
    /// `licensePubkey` + Android's `LICENSE_PUBKEY`. Hardcoded
    /// damit ein offline-User keinen DNS/network-Trick spielen
    /// kann um einen anderen Pubkey unterzuschieben.
    ///
    /// PLATZHALTER — vor Production-Launch durch echten privycs
    /// Pubkey ersetzen (lebt in private docs / license-keypair.txt).
    /// Tests können diese Konstante via init-injection überschreiben.
    public static let productionPubkey: Data = Data(repeating: 0, count: 32)

    private let pubkey: Curve25519.Signing.PublicKey

    public init(rawPubkey: Data = LicenseVerifier.productionPubkey) throws {
        guard rawPubkey.count == 32 else {
            throw LicenseError.malformedPubkey
        }
        self.pubkey = try Curve25519.Signing.PublicKey(rawRepresentation: rawPubkey)
    }

    /// Verifiziert den Lizenz-String + parsed das Payload.
    public func verify(_ licenseString: String, now: Date = Date()) throws -> LicensePayload {
        // Format: <b64-payload>.<b64-sig>
        let parts = licenseString.split(separator: ".", maxSplits: 1)
        guard parts.count == 2 else {
            throw LicenseError.malformedLicense
        }
        guard let payloadData = Data(base64URLEncoded: String(parts[0])),
              let sigData = Data(base64URLEncoded: String(parts[1])) else {
            throw LicenseError.malformedLicense
        }
        guard pubkey.isValidSignature(sigData, for: payloadData) else {
            throw LicenseError.signatureMismatch
        }
        let payload = try JSONDecoder().decode(LicensePayload.self, from: payloadData)
        if payload.notAfter > 0 {
            let expiry = Date(timeIntervalSince1970: TimeInterval(payload.notAfter))
            if expiry <= now {
                throw LicenseError.expired
            }
        }
        return payload
    }

    /// Checkt ob das Payload das iOS Pro-Tier freischaltet. Akzeptiert
    /// `single_ios` und `cross_platform_bundle`. Die anderen Tiers
    /// gelten nicht für iOS.
    public static func unlocksIOS(_ payload: LicensePayload) -> Bool {
        switch payload.tier {
        case .singleIOS, .crossPlatformBundle:
            return true
        default:
            return false
        }
    }
}

/// Parsed Payload eines verifizierten Lizenz-Strings. Sicher in
/// UserDefaults persistierbar (kein Secret-Inhalt — bereits public-
/// signed). Der ORIGINAL-License-Key-String wird parallel im
/// Keychain unter `KeychainKey.proLicense` gehalten für Reverify-
/// on-startup.
public struct LicensePayload: Codable, Equatable, Sendable {
    public let tier: Tier
    public let email: String
    public let purchaseID: String
    public let issuedAt: Int64
    public let notAfter: Int64  // 0 = lifetime

    public enum Tier: String, Codable, Sendable {
        case singleAndroid = "single_android"
        case singleDesktop = "single_desktop"
        case singleIOS = "single_ios"
        case crossPlatformBundle = "cross_platform_bundle"
    }

    private enum CodingKeys: String, CodingKey {
        case tier
        case email
        case purchaseID = "purchase_id"
        case issuedAt = "issued_at"
        case notAfter = "not_after"
    }
}

public enum LicenseError: Error, Equatable {
    case malformedPubkey
    case malformedLicense
    case signatureMismatch
    case expired

    public var localizedDescription: String {
        switch self {
        case .malformedPubkey: return "License public key is malformed (must be 32 bytes ed25519)"
        case .malformedLicense: return "License string is malformed (expected <b64-payload>.<b64-sig>)"
        case .signatureMismatch: return "License signature does not validate"
        case .expired: return "License has expired"
        }
    }
}

// MARK: — Base64-URL helper

private extension Data {
    /// Decodes a URL-safe base64 string (with optional `-`/`_`
    /// substitution and missing padding). Matches the Android +
    /// Desktop `base64UrlDecode` helpers so license-strings can be
    /// embedded in URLs / QR codes without re-encoding.
    init?(base64URLEncoded: String) {
        var s = base64URLEncoded
            .replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        let pad = 4 - (s.count % 4)
        if pad < 4 {
            s.append(String(repeating: "=", count: pad))
        }
        guard let d = Data(base64Encoded: s) else { return nil }
        self = d
    }
}
