import Foundation
import PrivycsCore

/// Encrypted backup export/import wired to the live repositories — mirror
/// of Android CloudBackupManager.export / importAndMerge. Crypto + wire
/// format live in PrivycsCore.BackupManager (cross-platform compatible).
@MainActor
extension AppState {

    /// Build a v4 payload from the current repositories and encrypt it.
    func exportBackup(password: String) async throws -> Data {
        let conns = (try? await connectionRepo.loadAll()) ?? connections
        let pls = (try? await poolRepo.loadAll()) ?? pools
        let rls = (try? await rulesRepo.loadAll()) ?? rules
        let activePool = await poolRepo.activePoolID()
        let payload = BackupManager.Payload(
            connections: BackupManager.ConnectionRegistry(connections: conns, activeId: ""),
            settings: settings,
            pools: BackupManager.PoolFile(pools: pls, activeId: activePool),
            networkRules: rls
        )
        return try BackupManager.export(payload, password: password)
    }

    /// Decrypt + merge a backup. Connections/pools merge by id (skip
    /// existing); networkRules REPLACE (v4 priority-order semantics);
    /// settings are restored. Returns counts for a user summary.
    @discardableResult
    func importBackup(_ data: Data, password: String) async throws
        -> (connections: Int, pools: Int, rules: Int) {
        let payload = try BackupManager.decrypt(data, password: password)

        let existingConnIDs = Set(((try? await connectionRepo.loadAll()) ?? []).map { $0.id })
        var addedConns = 0
        for c in payload.connections.connections where !existingConnIDs.contains(c.id) {
            try? await connectionRepo.save(c)
            addedConns += 1
        }

        var addedPools = 0
        if let pf = payload.pools {
            let existingPoolIDs = Set(((try? await poolRepo.loadAll()) ?? []).map { $0.id })
            for p in pf.pools where !existingPoolIDs.contains(p.id) {
                try? await poolRepo.save(p)
                addedPools += 1
            }
        }

        // Network rules replace wholesale (preserves priority order) — only
        // when the backup actually carried rules (v4+); never wipe on an
        // older backup that lacked the field.
        if !payload.networkRules.isEmpty {
            try? await rulesRepo.save(payload.networkRules)
        }

        try? await settingsRepo.save(payload.settings)

        // Refresh published state.
        connections = (try? await connectionRepo.loadAll()) ?? connections
        pools = (try? await poolRepo.loadAll()) ?? pools
        rules = (try? await rulesRepo.loadAll()) ?? rules
        settings = payload.settings

        return (addedConns, addedPools, payload.networkRules.count)
    }
}
