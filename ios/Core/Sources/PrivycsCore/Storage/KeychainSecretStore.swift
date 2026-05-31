import Foundation
import Security

/// Keychain-backed Secret-Storage. Persistiert Strings (API-keys,
/// VPN-config-Content, ed25519-License-Keys) im iOS-Keychain mit
/// strenger Access-Class. Shared via App Group damit der
/// PacketTunnelProvider-Extension dieselben Secrets lesen kann.
///
/// **Privacy-Annahmen**:
/// - `kSecAttrAccessibleWhenUnlockedThisDeviceOnly`: Secrets sind
///   nur lesbar während Device unlocked ist, NICHT vom Backup
///   wiederherstellbar (TIDeviceOnly).
/// - `kSecAttrAccessGroup`: alle Privycs-targets im App Group
///   teilen sich Keychain-Items (App + Tunnel-Extension + ggf.
///   Widget).
/// - `kSecAttrService`: stable identifier "com.privycs.vpn.secrets"
///   plus per-secret `kSecAttrAccount` als Lookup-Key.
///
/// Mirror der Android `SecretCrypto` Semantik, aber statt
/// AES-GCM-im-DataStore wird hier direkt Apple-Keychain genutzt
/// (Hardware-backed wenn StrongBox-equivalente Secure-Enclave da).
public actor KeychainSecretStore {

    public static let defaultAppGroup = "group.com.privycs.vpn"
    public static let service = "com.privycs.vpn.secrets"

    private let appGroup: String

    public init(appGroup: String = KeychainSecretStore.defaultAppGroup) {
        self.appGroup = appGroup
    }

    // MARK: — Public API

    /// Schreibt einen Secret-String unter `key` ins Keychain.
    /// Existierender Eintrag wird ersetzt. Wirft KeychainError bei
    /// jedem nicht-success OSStatus.
    public func set(_ value: String, for key: String) throws {
        guard let data = value.data(using: .utf8) else {
            throw KeychainError.encodingFailed
        }
        try setData(data, for: key)
    }

    /// Liest einen Secret-String. Nil wenn kein Eintrag existiert.
    /// Wirft bei Keychain-Lesefehlern (außer NotFound).
    public func get(_ key: String) throws -> String? {
        guard let data = try getData(key) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    /// Löscht einen Eintrag. Kein Error wenn nicht existiert.
    public func delete(_ key: String) throws {
        let query = baseQuery(for: key)
        let status = SecItemDelete(query as CFDictionary)
        if status != errSecSuccess && status != errSecItemNotFound {
            throw KeychainError.osStatus(status)
        }
    }

    /// Listet alle Keys die unter dem service+access-group im
    /// Keychain liegen. Nützlich für Migration + Diagnostics.
    public func listKeys() throws -> [String] {
        var query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: KeychainSecretStore.service,
            kSecAttrAccessGroup as String: appGroup,
            kSecReturnAttributes as String: true,
            kSecMatchLimit as String: kSecMatchLimitAll,
        ]
        // SecItemCopyMatching needs the attribute query without value
        _ = query.removeValue(forKey: kSecReturnData as String)

        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound {
            return []
        }
        if status != errSecSuccess {
            throw KeychainError.osStatus(status)
        }
        guard let items = result as? [[String: Any]] else { return [] }
        return items.compactMap { $0[kSecAttrAccount as String] as? String }
    }

    /// Löscht ALLE Privycs-Keychain-Einträge. Verwendet bei
    /// "Reset App" oder vollständigem Uninstall-Cleanup. Nicht
    /// verwechseln mit per-key delete.
    public func wipeAll() throws {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: KeychainSecretStore.service,
            kSecAttrAccessGroup as String: appGroup,
        ]
        let status = SecItemDelete(query as CFDictionary)
        if status != errSecSuccess && status != errSecItemNotFound {
            throw KeychainError.osStatus(status)
        }
    }

    // MARK: — Binary-Data API (für JSON/Codable serialisierte
    //          Container — Pools, SavedConnections etc.)

    public func setData(_ data: Data, for key: String) throws {
        // Update wenn existiert, sonst Add. Atomic via SecItem APIs.
        let attrs = baseQuery(for: key)
        let updateAttrs: [String: Any] = [
            kSecValueData as String: data,
        ]
        let updateStatus = SecItemUpdate(attrs as CFDictionary, updateAttrs as CFDictionary)
        if updateStatus == errSecSuccess { return }
        if updateStatus != errSecItemNotFound {
            throw KeychainError.osStatus(updateStatus)
        }
        // Add new
        var addAttrs = attrs
        addAttrs[kSecValueData as String] = data
        addAttrs[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        let addStatus = SecItemAdd(addAttrs as CFDictionary, nil)
        if addStatus != errSecSuccess {
            throw KeychainError.osStatus(addStatus)
        }
    }

    public func getData(_ key: String) throws -> Data? {
        var query = baseQuery(for: key)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne

        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        if status != errSecSuccess {
            throw KeychainError.osStatus(status)
        }
        return result as? Data
    }

    // MARK: — Private helpers

    private func baseQuery(for key: String) -> [String: Any] {
        return [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: KeychainSecretStore.service,
            kSecAttrAccount as String: key,
            kSecAttrAccessGroup as String: appGroup,
        ]
    }
}

/// Keychain-spezifische Errors. `osStatus` wraps die rohen OSStatus
/// codes für Debug-Lookups (siehe Apple `SecBase.h` für Bedeutungen).
public enum KeychainError: Error, Equatable {
    case osStatus(OSStatus)
    case encodingFailed

    public var localizedDescription: String {
        switch self {
        case .osStatus(let code):
            if let message = SecCopyErrorMessageString(code, nil) as String? {
                return "Keychain error (\(code)): \(message)"
            }
            return "Keychain error (\(code))"
        case .encodingFailed:
            return "Failed to encode value as UTF-8 data"
        }
    }
}

// MARK: — Canonical key namespace

/// Sammlung der stabilen Lookup-Keys für unsere Secrets. Eine
/// Stelle damit nichts driftet zwischen ConnectionRepository,
/// PacketTunnelProvider, Migration-Tooling.
public enum KeychainKey {
    /// Privycs Gateway API-Key. Plaintext API-key string.
    public static let apiKey = "gateway.api_key"

    /// Per-protocol-config encrypted content. Key-form
    /// "config.<connection-id>.<config-id>".
    public static func protocolConfig(connectionID: String, configID: String) -> String {
        "config.\(connectionID).\(configID)"
    }

    /// Per-pool-member encrypted config-content. Key-form
    /// "pool.<pool-id>.<member-id>".
    public static func poolMemberConfig(poolID: String, memberID: String) -> String {
        "pool.\(poolID).\(memberID)"
    }

    /// Pro-tier license-key as raw ed25519-signed base64 token.
    /// Anchor file für Cross-Platform License-Redeem.
    public static let proLicense = "license.pro"

    /// Persistente install-UUID für anonymous crash-reporting.
    /// Lebt im Keychain damit ein App-Reset auch die UUID killt
    /// (im Gegensatz zu UserDefaults das in iCloud-Backup landet).
    public static let installUUID = "telemetry.install_uuid"
}
