import Foundation

/// Match type for a NetworkRule. Serialized values are byte-identical
/// to Android `RuleMatchType` @SerialName so rules round-trip in backups.
public enum RuleMatchType: String, Codable, CaseIterable, Hashable, Sendable {
    case ssidExact = "ssid_exact"
    case ssidPattern = "ssid_pattern"
    case networkType = "network_type"
    case bssid = "bssid"
    case any = "any"
}

/// Action a matched rule applies. Mirrors Android `RuleAction`.
public enum RuleAction: String, Codable, CaseIterable, Hashable, Sendable {
    /// Disconnect if connected — the "trusted network" pattern.
    case noVpn = "no_vpn"
    /// Switch to the pool with id = targetId.
    case pool = "pool"
    /// Switch to the single connection with id = targetId.
    case connection = "connection"
    /// Connect whatever the user currently has selected as active
    /// (not a pinned target).
    case connectActive = "connect_active"
}

/// Live network transport class. rawValues match the strings Android's
/// NetworkRule.matches expects.
public enum NetworkType: String, Codable, CaseIterable, Hashable {
    case any, wifi, mobile, ethernet, none
}

/// Per-network auto-tunnel routing rule — field-for-field port of the
/// Android `NetworkRule` data class. The engine walks the list in
/// priority order on every network event; the first matching rule wins.
public struct NetworkRule: Codable, Identifiable, Equatable, Hashable, Sendable {
    public let id: String
    public var priority: Int
    public var matchType: RuleMatchType
    public var matchValue: String
    public var action: RuleAction
    /// Target pool/connection id for `.pool` / `.connection` actions.
    public var targetId: String
    public var enabled: Bool
    public var name: String

    public init(
        id: String,
        priority: Int = 0,
        matchType: RuleMatchType,
        matchValue: String = "",
        action: RuleAction,
        targetId: String = "",
        enabled: Bool = true,
        name: String = ""
    ) {
        self.id = id
        self.priority = priority
        self.matchType = matchType
        self.matchValue = matchValue
        self.action = action
        self.targetId = targetId
        self.enabled = enabled
        self.name = name
    }

    private enum CodingKeys: String, CodingKey {
        case id, priority, action, enabled, name
        case matchType = "match_type"
        case matchValue = "match_value"
        case targetId = "target_id"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        priority = try c.decodeIfPresent(Int.self, forKey: .priority) ?? 0
        matchType = try c.decode(RuleMatchType.self, forKey: .matchType)
        matchValue = try c.decodeIfPresent(String.self, forKey: .matchValue) ?? ""
        action = try c.decode(RuleAction.self, forKey: .action)
        targetId = try c.decodeIfPresent(String.self, forKey: .targetId) ?? ""
        enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? true
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
    }

    /// True if this rule matches the supplied network state. `networkType`
    /// is one of "wifi"/"mobile"/"ethernet"/"none". Ported 1:1 from
    /// Android NetworkRule.matches.
    public func matches(networkType: String, ssid: String, bssid: String) -> Bool {
        if !enabled { return false }
        switch matchType {
        case .ssidExact:
            return networkType == "wifi" && ssid.caseInsensitiveCompare(matchValue) == .orderedSame
        case .ssidPattern:
            return networkType == "wifi" && !ssid.isEmpty && Self.globMatches(matchValue, ssid)
        case .networkType:
            switch matchValue.lowercased() {
            case "any": return networkType != "none"
            case "wifi": return networkType == "wifi"
            case "mobile": return networkType == "mobile"
            case "ethernet": return networkType == "ethernet"
            case "wifi_mobile": return networkType == "wifi" || networkType == "mobile"
            default: return false
            }
        case .bssid:
            return networkType == "wifi" && !bssid.isEmpty && bssid.caseInsensitiveCompare(matchValue) == .orderedSame
        case .any:
            return networkType != "none"
        }
    }

    /// Glob match: '*' = any substring, '?' = single char. Case-insensitive.
    /// Mirrors Android NetworkRule.globMatches (literal-with-wildcards).
    static func globMatches(_ pattern: String, _ input: String) -> Bool {
        var regex = "^"
        for ch in pattern {
            switch ch {
            case "*": regex += ".*"
            case "?": regex += "."
            case "\\", ".", "[", "]", "(", ")", "{", "}", "+", "|", "^", "$":
                regex += "\\" + String(ch)
            default: regex += String(ch)
            }
        }
        regex += "$"
        return input.range(of: regex, options: [.regularExpression, .caseInsensitive]) != nil
    }
}

/// Live snapshot of the current network situation for the rules engine.
/// Produced by NetworkMonitor.
public struct NetworkState: Equatable, Hashable, Sendable {
    public let networkType: NetworkType
    /// SSID — empty when not Wi-Fi or when the Access-WiFi-Information
    /// entitlement / location permission isn't granted (iOS gates SSID).
    public let ssid: String
    /// BSSID (access-point MAC) — empty unless the WiFi-info entitlement
    /// is present and resolved.
    public let bssid: String

    public init(networkType: NetworkType, ssid: String = "", bssid: String = "") {
        self.networkType = networkType
        self.ssid = ssid
        self.bssid = bssid
    }

    public static let none = NetworkState(networkType: .none)
}
