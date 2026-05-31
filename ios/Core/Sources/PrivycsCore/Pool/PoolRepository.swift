import Foundation

/// Persistente Pool-Liste. Trennung analog ConnectionRepository:
/// Pool-Struktur (mit member-list + policy etc.) im UserDefaults,
/// einzelne `PoolMember.configContent` Strings im Keychain.
public actor PoolRepository {

    private let userDefaults: UserDefaults
    private let secretStore: KeychainSecretStore
    private let poolsKey = "privycs.pools.v1"
    private let activePoolKey = "privycs.active_pool_id.v1"

    public init(
        appGroup: String = KeychainSecretStore.defaultAppGroup,
        secretStore: KeychainSecretStore? = nil
    ) {
        guard let suite = UserDefaults(suiteName: appGroup) else {
            self.userDefaults = .standard
            self.secretStore = secretStore ?? KeychainSecretStore(appGroup: appGroup)
            return
        }
        self.userDefaults = suite
        self.secretStore = secretStore ?? KeychainSecretStore(appGroup: appGroup)
    }

    public func loadAll() async throws -> [Pool] {
        let pools = try loadPoolsRaw()
        var hydrated: [Pool] = []
        for var pool in pools {
            for i in pool.members.indices {
                let key = KeychainKey.poolMemberConfig(
                    poolID: pool.id,
                    memberID: pool.members[i].id
                )
                if let plaintext = try await secretStore.get(key) {
                    pool.members[i].configContent = plaintext
                }
            }
            hydrated.append(pool)
        }
        return hydrated
    }

    public func save(_ pool: Pool) async throws {
        for member in pool.members where !member.configContent.isEmpty {
            let key = KeychainKey.poolMemberConfig(
                poolID: pool.id,
                memberID: member.id
            )
            try await secretStore.set(member.configContent, for: key)
        }
        var stripped = pool
        for i in stripped.members.indices {
            stripped.members[i].configContent = ""
        }
        var all = (try? loadPoolsRaw()) ?? []
        all.removeAll { $0.id == pool.id }
        all.append(stripped)
        try savePoolsRaw(all)
    }

    public func delete(_ poolID: String) async throws {
        var all = (try? loadPoolsRaw()) ?? []
        let toRemove = all.first { $0.id == poolID }
        all.removeAll { $0.id == poolID }
        try savePoolsRaw(all)
        if let toRemove {
            for m in toRemove.members {
                let key = KeychainKey.poolMemberConfig(poolID: poolID, memberID: m.id)
                try await secretStore.delete(key)
            }
        }
        // Wenn der gelöschte Pool der aktive war, clear.
        if activePoolID() == poolID {
            setActivePoolID("")
        }
    }

    public func activePoolID() -> String {
        userDefaults.string(forKey: activePoolKey) ?? ""
    }

    public func setActivePoolID(_ id: String) {
        if id.isEmpty {
            userDefaults.removeObject(forKey: activePoolKey)
        } else {
            userDefaults.set(id, forKey: activePoolKey)
        }
    }

    // MARK: — Private

    private func loadPoolsRaw() throws -> [Pool] {
        guard let data = userDefaults.data(forKey: poolsKey) else {
            return []
        }
        return try JSONDecoder().decode([Pool].self, from: data)
    }

    private func savePoolsRaw(_ pools: [Pool]) throws {
        let data = try JSONEncoder().encode(pools)
        userDefaults.set(data, forKey: poolsKey)
    }
}
