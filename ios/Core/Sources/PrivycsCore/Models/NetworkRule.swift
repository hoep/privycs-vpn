import Foundation

/// Eine einzelne Regel in der NetworkRules-Engine. Mirror der
/// Android `NetworkRule` data class — gleiche Action+Match-Semantik.
/// Reihenfolge in `NetworkRulesRegistry.rules` entscheidet welche
/// Regel zuerst greift (top-down).
public struct NetworkRule: Codable, Identifiable, Equatable, Hashable {
    public let id: String
    /// User-Display-Name. "" = automatisch aus Match-Bedingung
    /// generiert (e.g. "WiFi \"Home\"" oder "Mobile").
    public var name: String
    /// Match-Bedingung: welche Netzwerk-Situation matcht diese Regel?
    public var match: Match
    /// Was tun wenn match? Connect-to-X / Disconnect / KeepAsIs.
    public var action: Action
    /// True = aktiv, False = vom User deaktiviert (nicht gelöscht).
    public var enabled: Bool

    public init(
        id: String,
        name: String = "",
        match: Match,
        action: Action,
        enabled: Bool = true
    ) {
        self.id = id
        self.name = name
        self.match = match
        self.action = action
        self.enabled = enabled
    }

    /// Network-Situation gegen die gematcht wird.
    public struct Match: Codable, Equatable, Hashable {
        /// Netzwerk-Typ. "any" = jeder.
        public var networkType: NetworkType
        /// SSID-Matching. Nur relevant wenn networkType == wifi.
        public var ssidMode: SSIDMode
        /// SSID-Liste — Semantik abhängig von ssidMode.
        public var ssidList: [String]

        public init(
            networkType: NetworkType = .any,
            ssidMode: SSIDMode = .all,
            ssidList: [String] = []
        ) {
            self.networkType = networkType
            self.ssidMode = ssidMode
            self.ssidList = ssidList
        }

        public enum NetworkType: String, Codable, CaseIterable, Hashable {
            case any
            case wifi
            case mobile
            case ethernet
            case none
        }

        public enum SSIDMode: String, Codable, CaseIterable, Hashable {
            /// Jede SSID matcht (ssidList ignored).
            case all
            /// Nur SSIDs aus ssidList matchen.
            case only
            /// Alle SSIDs außer denen aus ssidList matchen.
            case except
        }

        private enum CodingKeys: String, CodingKey {
            case networkType = "network_type"
            case ssidMode = "ssid_mode"
            case ssidList = "ssid_list"
        }
    }

    /// Was tun wenn die Regel matcht.
    public enum Action: Codable, Equatable, Hashable {
        /// VPN trennen (oder verbunden lassen falls schon disconnected).
        case disconnect
        /// Aktuellen Zustand beibehalten — passt nichts an, übergibt
        /// an die nächste matching Rule oder Default-Behaviour.
        case keepAsIs
        /// Eine bestimmte Verbindung connecten.
        case connectToConnection(connectionID: String)
        /// Einen bestimmten Pool aktivieren.
        case connectToPool(poolID: String)

        // Custom Codable: action ist tagged-union mit type+value.
        private enum CodingKeys: String, CodingKey {
            case type
            case value
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            let type = try c.decode(String.self, forKey: .type)
            switch type {
            case "disconnect":
                self = .disconnect
            case "keep_as_is":
                self = .keepAsIs
            case "connect_to_connection":
                let id = try c.decode(String.self, forKey: .value)
                self = .connectToConnection(connectionID: id)
            case "connect_to_pool":
                let id = try c.decode(String.self, forKey: .value)
                self = .connectToPool(poolID: id)
            default:
                throw DecodingError.dataCorrupted(.init(
                    codingPath: decoder.codingPath,
                    debugDescription: "Unknown NetworkRule.Action type: \(type)"
                ))
            }
        }

        public func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            switch self {
            case .disconnect:
                try c.encode("disconnect", forKey: .type)
            case .keepAsIs:
                try c.encode("keep_as_is", forKey: .type)
            case .connectToConnection(let id):
                try c.encode("connect_to_connection", forKey: .type)
                try c.encode(id, forKey: .value)
            case .connectToPool(let id):
                try c.encode("connect_to_pool", forKey: .type)
                try c.encode(id, forKey: .value)
            }
        }
    }
}

/// Live-Snapshot der aktuellen Netzwerk-Situation für die
/// Rules-Engine. Vom NetworkMonitor periodisch produziert.
public struct NetworkState: Equatable, Hashable, Sendable {
    public let networkType: NetworkRule.Match.NetworkType
    /// SSID — leer wenn nicht WiFi oder wenn Location-Permission
    /// nicht erteilt (iOS gibt SSID nur mit Permission + im
    /// Foreground frei).
    public let ssid: String

    public init(networkType: NetworkRule.Match.NetworkType, ssid: String) {
        self.networkType = networkType
        self.ssid = ssid
    }

    public static let none = NetworkState(networkType: .none, ssid: "")
}
