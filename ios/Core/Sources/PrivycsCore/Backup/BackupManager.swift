import Foundation
import Crypto

/// Encrypted backup export/import — byte-compatible with the Android
/// `CloudBackupManager` so a backup made on one platform restores on the
/// other. AES-256-GCM with PBKDF2-HMAC-SHA256 (100k iterations) key
/// derivation; the on-disk JSON envelope is `{salt, iv, data, version}`
/// (all base64), and `data` = ciphertext‖tag exactly as Java's
/// `Cipher("AES/GCM/NoPadding")` emits it (the 16-byte GCM tag appended).
public enum BackupManager {
    public static let version = 4
    static let iterations = 100_000
    static let saltLen = 16
    static let ivLen = 12          // GCM standard nonce
    static let tagLen = 16         // 128-bit GCM tag

    public enum BackupError: Error, LocalizedError {
        case unsupportedVersion(Int)
        case decryptFailed          // wrong passphrase or corrupted file
        case malformed
        public var errorDescription: String? {
            switch self {
            case .unsupportedVersion(let v): return "Backup version \(v) is not supported by this app."
            case .decryptFailed: return "Wrong passphrase or corrupted backup file."
            case .malformed: return "The backup file is not a valid Privycs backup."
            }
        }
    }

    // MARK: Envelope + payload (Android wire shapes)

    struct Envelope: Codable {
        var salt: String
        var iv: String
        var data: String
        var version: Int
    }

    /// `{connections:[...], active_id}` — Android ConnectionRegistry.
    public struct ConnectionRegistry: Codable {
        public var connections: [SavedConnection]
        public var activeId: String
        public init(connections: [SavedConnection] = [], activeId: String = "") {
            self.connections = connections; self.activeId = activeId
        }
        enum CodingKeys: String, CodingKey { case connections; case activeId = "active_id" }
        public init(from d: Decoder) throws {
            let c = try d.container(keyedBy: CodingKeys.self)
            connections = try c.decodeIfPresent([SavedConnection].self, forKey: .connections) ?? []
            activeId = try c.decodeIfPresent(String.self, forKey: .activeId) ?? ""
        }
    }

    /// `{pools:[...], active_id}` — Android PoolFile.
    public struct PoolFile: Codable {
        public var pools: [Pool]
        public var activeId: String
        public init(pools: [Pool] = [], activeId: String = "") {
            self.pools = pools; self.activeId = activeId
        }
        enum CodingKeys: String, CodingKey { case pools; case activeId = "active_id" }
        public init(from d: Decoder) throws {
            let c = try d.container(keyedBy: CodingKeys.self)
            pools = try c.decodeIfPresent([Pool].self, forKey: .pools) ?? []
            activeId = try c.decodeIfPresent(String.self, forKey: .activeId) ?? ""
        }
    }

    /// v4 payload `{connections, settings, pools, networkRules}` (camelCase
    /// keys, matching kotlinx default property-name serialization).
    public struct Payload: Codable {
        public var connections: ConnectionRegistry
        public var settings: AppSettings
        public var pools: PoolFile?
        public var networkRules: [NetworkRule]
        public init(connections: ConnectionRegistry, settings: AppSettings,
                    pools: PoolFile? = nil, networkRules: [NetworkRule] = []) {
            self.connections = connections; self.settings = settings
            self.pools = pools; self.networkRules = networkRules
        }
        enum CodingKeys: String, CodingKey { case connections, settings, pools, networkRules }
        public init(from d: Decoder) throws {
            let c = try d.container(keyedBy: CodingKeys.self)
            connections = try c.decode(ConnectionRegistry.self, forKey: .connections)
            settings = (try? c.decodeIfPresent(AppSettings.self, forKey: .settings)) ?? .default
            pools = try? c.decodeIfPresent(PoolFile.self, forKey: .pools)
            networkRules = (try? c.decodeIfPresent([NetworkRule].self, forKey: .networkRules)) ?? []
        }
    }

    // MARK: Export / import

    public static func export(_ payload: Payload, password: String) throws -> Data {
        let plaintext = try JSONEncoder().encode(payload)
        let salt = randomBytes(saltLen)
        let iv = randomBytes(ivLen)
        let keyBytes = PBKDF2.deriveSHA256(password: password, salt: salt,
                                           iterations: iterations, keyLength: 32)
        let key = SymmetricKey(data: keyBytes)
        let sealed = try AES.GCM.seal(plaintext, using: key, nonce: try AES.GCM.Nonce(data: iv))
        // Java GCM output = ciphertext || tag.
        let blob = sealed.ciphertext + sealed.tag
        let env = Envelope(
            salt: Data(salt).base64EncodedString(),
            iv: Data(iv).base64EncodedString(),
            data: blob.base64EncodedString(),
            version: version
        )
        return try JSONEncoder().encode(env)
    }

    public static func decrypt(_ fileData: Data, password: String) throws -> Payload {
        guard let env = try? JSONDecoder().decode(Envelope.self, from: fileData) else {
            throw BackupError.malformed
        }
        if env.version > version { throw BackupError.unsupportedVersion(env.version) }
        guard let salt = Data(base64Encoded: env.salt),
              let iv = Data(base64Encoded: env.iv),
              let blob = Data(base64Encoded: env.data), blob.count > tagLen else {
            throw BackupError.malformed
        }
        let keyBytes = PBKDF2.deriveSHA256(password: password, salt: [UInt8](salt),
                                           iterations: iterations, keyLength: 32)
        let key = SymmetricKey(data: keyBytes)
        let ct = blob.prefix(blob.count - tagLen)
        let tag = blob.suffix(tagLen)
        let plaintext: Data
        do {
            let box = try AES.GCM.SealedBox(nonce: try AES.GCM.Nonce(data: iv),
                                            ciphertext: ct, tag: tag)
            plaintext = try AES.GCM.open(box, using: key)
        } catch {
            throw BackupError.decryptFailed
        }
        // v2+ = Payload object; v1 = bare ConnectionRegistry.
        if let payload = try? JSONDecoder().decode(Payload.self, from: plaintext) {
            return payload
        }
        if let reg = try? JSONDecoder().decode(ConnectionRegistry.self, from: plaintext) {
            return Payload(connections: reg, settings: .default)
        }
        throw BackupError.decryptFailed
    }

    // MARK: Helpers

    static func randomBytes(_ n: Int) -> [UInt8] {
        var rng = SystemRandomNumberGenerator()   // CSPRNG on Apple + Linux
        return (0..<n).map { _ in UInt8.random(in: UInt8.min...UInt8.max, using: &rng) }
    }
}

/// PBKDF2-HMAC-SHA256, pure Swift on top of swift-crypto's HMAC (neither
/// CryptoKit nor swift-crypto ship PBKDF2, and CommonCrypto would break
/// the Linux test build). Produces the same key as Java's
/// `PBKDF2WithHmacSHA256` for UTF-8 passwords.
enum PBKDF2 {
    static func deriveSHA256(password: String, salt: [UInt8], iterations: Int, keyLength: Int) -> [UInt8] {
        let hLen = 32
        let pwKey = SymmetricKey(data: Array(password.utf8))
        let blocks = Int((Double(keyLength) / Double(hLen)).rounded(.up))
        var derived = [UInt8]()
        derived.reserveCapacity(blocks * hLen)
        for block in 1...max(1, blocks) {
            let be: [UInt8] = [
                UInt8((block >> 24) & 0xff), UInt8((block >> 16) & 0xff),
                UInt8((block >> 8) & 0xff), UInt8(block & 0xff),
            ]
            var u = [UInt8](HMAC<SHA256>.authenticationCode(for: Data(salt + be), using: pwKey))
            var t = u
            if iterations > 1 {
                for _ in 1..<iterations {
                    u = [UInt8](HMAC<SHA256>.authenticationCode(for: Data(u), using: pwKey))
                    for i in 0..<hLen { t[i] ^= u[i] }
                }
            }
            derived.append(contentsOf: t)
        }
        return Array(derived.prefix(keyLength))
    }
}
