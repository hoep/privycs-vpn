import Foundation

/// Tracks pool members marked unreachable after a failed connect/probe,
/// with a TTL so a transient failure self-heals. Android parity:
/// PoolStateRepository's recent-failure / unreachable bookkeeping.
public actor PoolHealthStore {
    private let defaults: UserDefaults
    private let key = "privycs.pool_unreachable.v1"
    /// How long a member stays excluded after a failure (Android: 30 min).
    private let ttl: TimeInterval = 1800

    public init(appGroup: String = KeychainSecretStore.defaultAppGroup) {
        self.defaults = UserDefaults(suiteName: appGroup) ?? .standard
    }

    private func load() -> [String: Double] {
        (defaults.dictionary(forKey: key) as? [String: Double]) ?? [:]
    }
    private func store(_ m: [String: Double]) {
        defaults.set(m, forKey: key)
    }
    private func composite(_ pool: String, _ member: String) -> String { "\(pool):\(member)" }

    /// Mark a member unreachable as of `now`.
    public func markUnreachable(pool: String, member: String, now: Date = Date()) {
        var m = load()
        m[composite(pool, member)] = now.timeIntervalSince1970
        store(m)
    }

    /// Member ids of `pool` still within the unreachable TTL.
    public func unreachableMembers(pool: String, now: Date = Date()) -> Set<String> {
        let cutoff = now.timeIntervalSince1970 - ttl
        let prefix = "\(pool):"
        var out = Set<String>()
        for (k, ts) in load() where k.hasPrefix(prefix) && ts >= cutoff {
            out.insert(String(k.dropFirst(prefix.count)))
        }
        return out
    }

    /// Clear all unreachable marks for a pool (e.g. after connectivity
    /// returns and every member was excluded).
    public func clear(pool: String) {
        let prefix = "\(pool):"
        var m = load()
        m = m.filter { !$0.key.hasPrefix(prefix) }
        store(m)
    }
}
