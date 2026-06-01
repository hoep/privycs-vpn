import Foundation

/// In-memory + on-disk-persisted Liste aller SavedConnections.
/// Der RAW `[SavedConnection]` JSON wird im App-Group
/// UserDefaults gehalten; die EINZELNEN `ProtocolConfig.configContent`
/// Strings (die wirklich sensitive sind: WG-private-key,
/// OpenVPN-inline-cert, IPSec PKCS#12) liegen separat im Keychain.
///
/// ConnectionRepository löst die Trennung beim Load + Restore
/// transparent — die public-API gibt Connections mit gefüllten
/// configContent-Strings zurück.
///
/// Mirror der Android `ConnectionRepository`. Threadsafe via actor.
public actor ConnectionRepository {

    private let userDefaults: UserDefaults
    private let secretStore: KeychainSecretStore
    private let connectionsKey = "privycs.connections.v1"

    public init(
        appGroup: String = KeychainSecretStore.defaultAppGroup,
        secretStore: KeychainSecretStore? = nil
    ) {
        guard let suite = UserDefaults(suiteName: appGroup) else {
            // Fallback to standard suite during unit tests where the
            // App Group isn't configured. Never happens in shipping
            // app where entitlements + provisioning are correct.
            self.userDefaults = .standard
            self.secretStore = secretStore ?? KeychainSecretStore(appGroup: appGroup)
            return
        }
        self.userDefaults = suite
        self.secretStore = secretStore ?? KeychainSecretStore(appGroup: appGroup)
    }

    // MARK: — Public API

    /// Lädt alle Connections. ConfigContents werden aus dem Keychain
    /// gemerged. Throw wenn der UserDefaults-JSON parse-fail oder
    /// Keychain-read fail.
    public func loadAll() async throws -> [SavedConnection] {
        let connections = try loadConnectionsRaw()
        var hydrated: [SavedConnection] = []
        for var conn in connections {
            for i in conn.protocols.indices {
                let key = KeychainKey.protocolConfig(
                    connectionID: conn.id,
                    configID: conn.protocols[i].id
                )
                if let plaintext = try await secretStore.get(key) {
                    conn.protocols[i].configContent = plaintext
                }
            }
            hydrated.append(conn)
        }
        return hydrated
    }

    /// Speichert eine Connection. Vorhandene mit gleicher ID wird
    /// ersetzt. configContent-Strings werden separat ins Keychain
    /// geschrieben, der zurückbleibende UserDefaults-JSON enthält
    /// die Configs OHNE configContent (= leerer String).
    public func save(_ connection: SavedConnection) async throws {
        // 1. ConfigContent ins Keychain pro ProtocolConfig
        for config in connection.protocols {
            guard !config.configContent.isEmpty else { continue }
            let key = KeychainKey.protocolConfig(
                connectionID: connection.id,
                configID: config.id
            )
            try await secretStore.set(config.configContent, for: key)
        }
        // 2. Stripped Connection (ohne configContent) ins UserDefaults
        var stripped = connection
        for i in stripped.protocols.indices {
            stripped.protocols[i].configContent = ""
        }
        var all = (try? loadConnectionsRaw()) ?? []
        all.removeAll { $0.id == connection.id }
        all.append(stripped)
        try saveConnectionsRaw(all)
    }

    /// Add a protocol config to an EXISTING connection, or create a new
    /// connection when `connectionID` is nil. Mirror of Android
    /// `ConnectionRepository.addOrUpdate`: a config matching the same
    /// protocol+filename is updated in place (keeping its id), otherwise
    /// it is appended — so multi-config-per-protocol works and re-import
    /// of the same file doesn't duplicate.
    @discardableResult
    public func addOrUpdate(connectionID: String?, name: String, config: ProtocolConfig) async throws -> SavedConnection {
        let all = try await loadAll()
        if let cid = connectionID, var conn = all.first(where: { $0.id == cid }) {
            if let idx = conn.protocols.firstIndex(where: {
                $0.protocol == config.protocol && $0.filename == config.filename
            }) {
                // Update in place, preserve the existing config id.
                let existingID = conn.protocols[idx].id
                conn.protocols[idx] = ProtocolConfig(
                    id: existingID,
                    protocol: config.protocol,
                    filename: config.filename,
                    nickname: config.nickname,
                    configContent: config.configContent,
                    serverAddress: config.serverAddress
                )
            } else {
                conn.protocols.append(config)
            }
            if conn.activeConfigID.isEmpty { conn.activeConfigID = config.id }
            try await save(conn)
            return conn
        }
        let conn = SavedConnection(
            id: UUID().uuidString,
            name: name,
            protocols: [config],
            activeConfigID: config.id
        )
        try await save(conn)
        return conn
    }

    /// Remove one protocol config from a connection (Android removeConfig).
    /// Deletes the whole connection if it was the last config.
    public func removeConfig(connectionID: String, configID: String) async throws {
        let all = try await loadAll()
        guard var conn = all.first(where: { $0.id == connectionID }) else { return }
        conn.protocols.removeAll { $0.id == configID }
        if conn.protocols.isEmpty {
            try await delete(connectionID)
            return
        }
        if conn.activeConfigID == configID {
            conn.activeConfigID = conn.protocols.first?.id ?? ""
        }
        try await secretStore.delete(KeychainKey.protocolConfig(connectionID: connectionID, configID: configID))
        try await save(conn)
    }

    /// Set the active ProtocolConfig (per-protocol switch on Connect).
    public func setActiveConfig(connectionID: String, configID: String) async throws {
        let all = try await loadAll()
        guard var conn = all.first(where: { $0.id == connectionID }),
              conn.protocols.contains(where: { $0.id == configID }) else { return }
        conn.activeConfigID = configID
        try await save(conn)
    }

    /// Löscht eine Connection inkl. aller zugehörigen Keychain-Items.
    public func delete(_ connectionID: String) async throws {
        var all = (try? loadConnectionsRaw()) ?? []
        let toRemove = all.first { $0.id == connectionID }
        all.removeAll { $0.id == connectionID }
        try saveConnectionsRaw(all)
        if let toRemove {
            for cfg in toRemove.protocols {
                let key = KeychainKey.protocolConfig(
                    connectionID: connectionID,
                    configID: cfg.id
                )
                try await secretStore.delete(key)
            }
        }
    }

    /// Findet die aktive ProtocolConfig einer Connection — verwendet
    /// für Single-Connection-Picks. Nil wenn keine aktive Config
    /// gesetzt oder Config nicht mehr in der Liste.
    public func activeConfig(for connection: SavedConnection) -> ProtocolConfig? {
        guard !connection.activeConfigID.isEmpty else { return nil }
        return connection.protocols.first { $0.id == connection.activeConfigID }
    }

    // MARK: — Private serialisation

    private func loadConnectionsRaw() throws -> [SavedConnection] {
        guard let data = userDefaults.data(forKey: connectionsKey) else {
            return []
        }
        return try JSONDecoder().decode([SavedConnection].self, from: data)
    }

    private func saveConnectionsRaw(_ connections: [SavedConnection]) throws {
        let data = try JSONEncoder().encode(connections)
        userDefaults.set(data, forKey: connectionsKey)
    }
}
