import Foundation
import Crypto

/// ed25519-signed offline license-key verifier. **Byte-identical** to
/// the Android (`license/License.kt`) and Desktop
/// (`internal/license/license.go`) verifiers — same public key, same
/// format, same payload schema — so one `.privycs-license` /
/// cross-platform-bundle key activates on all three platforms.
///
/// Format:
///
///     PRVC-{base32(canonicalJSON(payload))}-{base32(ed25519-sig)}
///
/// base32 = RFC 4648 standard alphabet, no padding, uppercase. The
/// signature is verified over the EXACT decoded payload bytes (no
/// re-serialisation). Verification is offline; the build-injected
/// public key hex is the trust anchor.
public struct LicenseVerifier: Sendable {

    public static let prefix = "PRVC"
    public static let currentSchemaVersion = 1

    public static let platformIOS = "ios"

    /// 64-char hex of the 32-byte ed25519 public key, injected at build
    /// time (Info.plist `LicensePublicKeyHex` ← `$(LICENSE_PUBLIC_KEY_HEX)`
    /// CI secret, the SAME key Android/Desktop embed). Empty when the
    /// secret isn't set → verify fails closed with `.noPublicKey`.
    private let pubKeyHex: String

    public init(pubKeyHex: String = PrivycsCoreInfo.licensePublicKeyHex) {
        self.pubKeyHex = pubKeyHex
    }

    public enum ErrorKind: Equatable, Sendable {
        case malformed, badSignature, unsupportedVersion, wrongPlatform, unknownTier, noPublicKey
    }

    /// Verify a raw `PRVC-…-…` key. Throws `LicenseError` with a kind on
    /// failure; returns the parsed payload on success. `expectedPlatform`
    /// defaults to "ios" — a key whose `platforms` array lacks it is
    /// rejected (single-Desktop keys fail here, the bundle passes).
    public func verify(_ rawKey: String, expectedPlatform: String = LicenseVerifier.platformIOS) throws -> LicensePayload {
        guard !pubKeyHex.isEmpty else { throw LicenseError(.noPublicKey, "no pubkey configured") }
        guard let pub = Self.hexToBytes(pubKeyHex), pub.count == 32 else {
            throw LicenseError(.noPublicKey, "invalid pubkey hex")
        }
        let parts = rawKey.trimmingCharacters(in: .whitespacesAndNewlines).split(separator: "-").map(String.init)
        guard parts.count == 3, parts[0] == Self.prefix else {
            throw LicenseError(.malformed, "expected PRVC-<payload>-<sig>")
        }
        guard let payloadBytes = Self.decodeBase32(parts[1]) else {
            throw LicenseError(.malformed, "payload not base32")
        }
        guard let sigBytes = Self.decodeBase32(parts[2]), sigBytes.count == 64 else {
            throw LicenseError(.malformed, "sig not base32/len")
        }
        guard let key = try? Curve25519.Signing.PublicKey(rawRepresentation: pub),
              key.isValidSignature(sigBytes, for: payloadBytes) else {
            throw LicenseError(.badSignature, "signature does not match")
        }
        guard let payload = Self.parsePayload(payloadBytes) else {
            throw LicenseError(.malformed, "payload not JSON")
        }
        guard payload.v == Self.currentSchemaVersion else {
            throw LicenseError(.unsupportedVersion, "v=\(payload.v)")
        }
        guard payload.tier == "pro" else {
            throw LicenseError(.unknownTier, payload.tier)
        }
        if !expectedPlatform.isEmpty && !payload.platforms.contains(expectedPlatform) {
            throw LicenseError(.wrongPlatform, "platforms=\(payload.platforms) excludes \(expectedPlatform)")
        }
        return payload
    }

    /// True when this payload unlocks iOS Pro (its platforms list
    /// includes "ios" — single-iOS or the cross-platform bundle).
    public static func unlocksIOS(_ payload: LicensePayload) -> Bool {
        payload.platforms.contains(platformIOS)
    }

    // MARK: — helpers (ports of License.kt)

    private static func parsePayload(_ bytes: Data) -> LicensePayload? {
        guard let obj = try? JSONSerialization.jsonObject(with: bytes) as? [String: Any] else { return nil }
        let platforms = (obj["platforms"] as? [Any])?.compactMap { $0 as? String } ?? []
        let hash = obj["buyer_email_hash"] as? String
        return LicensePayload(
            v: (obj["v"] as? NSNumber)?.intValue ?? 0,
            tier: obj["tier"] as? String ?? "",
            sku: obj["sku"] as? String ?? "",
            platforms: platforms,
            issued: obj["issued"] as? String ?? "",
            buyerEmailHash: (hash?.isEmpty == false) ? hash : nil
        )
    }

    /// RFC 4648 base32 decode — standard alphabet, no padding, uppercase.
    /// Matches Go `base32.StdEncoding.WithPadding(NoPadding)` + Android.
    static func decodeBase32(_ s: String) -> Data? {
        let alphabet = Array("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567")
        let src = Array(s.uppercased())
        if src.isEmpty { return Data() }
        var out = [UInt8]()
        out.reserveCapacity(src.count * 5 / 8)
        var buffer: UInt64 = 0
        var bitsLeft = 0
        for c in src {
            guard let v = alphabet.firstIndex(of: c) else { return nil }
            buffer = (buffer << 5) | UInt64(v)
            bitsLeft += 5
            if bitsLeft >= 8 {
                bitsLeft -= 8
                out.append(UInt8((buffer >> UInt64(bitsLeft)) & 0xFF))
            }
        }
        return Data(out)
    }

    static func hexToBytes(_ s: String) -> Data? {
        guard s.count % 2 == 0 else { return nil }
        var out = [UInt8](); out.reserveCapacity(s.count / 2)
        var idx = s.startIndex
        while idx < s.endIndex {
            let next = s.index(idx, offsetBy: 2)
            guard let b = UInt8(s[idx..<next], radix: 16) else { return nil }
            out.append(b); idx = next
        }
        return Data(out)
    }
}

/// Parsed license payload — mirrors the signer schema (Android Payload /
/// Go payload). Safe to persist (already public-signed, no secret).
public struct LicensePayload: Codable, Equatable, Sendable {
    public let v: Int
    public let tier: String          // always "pro" for a valid key
    public let sku: String           // privycs_pro_desktop | privycs_pro_bundle_all
    public let platforms: [String]   // e.g. ["android","desktop","ios"]
    public let issued: String        // RFC3339
    public let buyerEmailHash: String?

    private enum CodingKeys: String, CodingKey {
        case v, tier, sku, platforms, issued
        case buyerEmailHash = "buyer_email_hash"
    }
}

public struct LicenseError: Error, Equatable {
    public let kind: LicenseVerifier.ErrorKind
    public let detail: String
    public init(_ kind: LicenseVerifier.ErrorKind, _ detail: String = "") {
        self.kind = kind
        self.detail = detail
    }
    public var localizedDescription: String {
        switch kind {
        case .malformed: return "License key is malformed (expected PRVC-…-…)"
        case .badSignature: return "License signature does not validate"
        case .unsupportedVersion: return "License schema version is not supported"
        case .wrongPlatform: return "This license does not include iOS"
        case .unknownTier: return "License tier is not recognized"
        case .noPublicKey: return "This build cannot validate license keys"
        }
    }
}
