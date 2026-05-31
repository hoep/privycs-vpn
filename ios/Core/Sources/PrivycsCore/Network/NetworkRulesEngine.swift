import Foundation

/// Stateless evaluator: gegeben eine Liste von Rules + ein
/// NetworkState → gibt die erste passende Rule + deren Action
/// zurück. Mirror der Android `NetworkRulesEngine`. Reihenfolge
/// in `rules` = Priorität (top-down).
public struct NetworkRulesEngine: Sendable {

    public init() {}

    /// Ergebnis der Evaluation.
    public struct Result: Equatable, Sendable {
        /// Erste matching rule, oder nil wenn keine Rule paßt.
        public let matchedRule: NetworkRule?
        /// Die zu führende Action — entweder von der matched-rule,
        /// oder `.keepAsIs` als Default wenn keine matched.
        public let action: NetworkRule.Action

        public static let noMatch = Result(matchedRule: nil, action: .keepAsIs)
    }

    public func evaluate(
        rules: [NetworkRule],
        state: NetworkState,
        masterEnabled: Bool
    ) -> Result {
        if !masterEnabled {
            // Master toggle OFF — engine takes no action regardless of
            // matching rules. UI surfaces this as "Manual control only".
            return .noMatch
        }
        for rule in rules where rule.enabled {
            if matches(rule: rule, state: state) {
                return Result(matchedRule: rule, action: rule.action)
            }
        }
        return .noMatch
    }

    func matches(rule: NetworkRule, state: NetworkState) -> Bool {
        let m = rule.match
        // 1. Network-type-Gate
        if m.networkType != .any && m.networkType != state.networkType {
            return false
        }
        // 2. SSID-Gate (nur relevant bei wifi)
        if state.networkType == .wifi {
            switch m.ssidMode {
            case .all:
                return true
            case .only:
                if state.ssid.isEmpty { return false }
                return m.ssidList.contains(state.ssid)
            case .except:
                if state.ssid.isEmpty { return true }
                return !m.ssidList.contains(state.ssid)
            }
        }
        // 3. Non-WiFi: SSID-Mode ignored
        return true
    }
}

/// Persistente Liste von NetworkRules + master toggle. Mirror der
/// Android `NetworkRulesRepository`.
public actor NetworkRulesRepository {
    private let userDefaults: UserDefaults
    private let rulesKey = "privycs.network_rules.v1"

    public init(appGroup: String = KeychainSecretStore.defaultAppGroup) {
        guard let suite = UserDefaults(suiteName: appGroup) else {
            self.userDefaults = .standard
            return
        }
        self.userDefaults = suite
    }

    public func loadAll() throws -> [NetworkRule] {
        guard let data = userDefaults.data(forKey: rulesKey) else {
            return []
        }
        return try JSONDecoder().decode([NetworkRule].self, from: data)
    }

    public func save(_ rules: [NetworkRule]) throws {
        let data = try JSONEncoder().encode(rules)
        userDefaults.set(data, forKey: rulesKey)
    }
}
