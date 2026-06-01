import Foundation

/// Stateless evaluator: gegeben eine Liste von Rules + ein
/// NetworkState → gibt die erste passende Rule + deren Action
/// zurück. Mirror der Android `NetworkRulesEngine`. Reihenfolge
/// in `rules` = Priorität (top-down).
public struct NetworkRulesEngine: Sendable {

    public init() {}

    /// Evaluation result. `matchedRule == nil` = no rule matched → engine
    /// takes no action (matches Android RuleResolution.NoMatch).
    public struct Result: Equatable, Sendable {
        public let matchedRule: NetworkRule?
        public static let noMatch = Result(matchedRule: nil)
    }

    /// First-match-wins in priority (list) order. Master toggle OFF = no action.
    public func evaluate(
        rules: [NetworkRule],
        state: NetworkState,
        masterEnabled: Bool
    ) -> Result {
        if !masterEnabled { return .noMatch }
        let nt = state.networkType.rawValue
        for rule in rules where rule.enabled {
            if rule.matches(networkType: nt, ssid: state.ssid, bssid: state.bssid) {
                return Result(matchedRule: rule)
            }
        }
        return .noMatch
    }

    func matches(rule: NetworkRule, state: NetworkState) -> Bool {
        rule.matches(networkType: state.networkType.rawValue, ssid: state.ssid, bssid: state.bssid)
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
