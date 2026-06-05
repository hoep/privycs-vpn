import Foundation

/// One imported VPN configuration. Mirrors the Android
/// `SavedConnection` data class field-for-field so JSON-based
/// backup-import (Settings → Export/Restore) works cross-platform.
///
/// Note: `protocols` carries one entry per protocol class —
/// AmneziaWG, WireGuard, OpenVPN, IPSec. A single SavedConnection
/// may hold multiple ProtocolConfigs per protocol (e.g. WG-UDP +
/// WG-TCP failover, or multi-region pool members). The active
/// pick at any given moment is `activeConfigID`.
public struct SavedConnection: Codable, Identifiable, Equatable, Hashable {
    public let id: String
    public var name: String
    public var protocols: [ProtocolConfig]
    /// `id` of the currently-active ProtocolConfig within `protocols`.
    /// Empty string when nothing has been picked yet (fresh import).
    public var activeConfigID: String
    /// Per-protocol failover order. Empty = default
    /// (amneziawg → wireguard → openvpn → ipsec).
    public var protocolFailoverOrder: [VpnProtocol]
    /// User-configured DNS override applied while this connection
    /// is active. Empty = no override.
    public var dnsOverride: String
    /// True after the user has explicitly verified this connection
    /// works — gates upgrade-prompts + diagnostics flow.
    public var verified: Bool
    /// Last time the connection was successfully established.
    /// Nil = never connected.
    public var lastConnectedAt: Date?
    /// Legacy/compat: active protocol enum. Authoritative pick is
    /// `activeConfigID`; this mirrors Android's `activeProtocol` field so
    /// cross-platform backup round-trips without dropping it. Nil = derive
    /// from activeConfigID.
    public var activeProtocol: VpnProtocol?
    /// RFC3339 creation timestamp (Android `created_at`). "" = unknown.
    public var createdAt: String
    /// Favorite flag (Android `is_favorite`).
    public var isFavorite: Bool

    public init(
        id: String,
        name: String,
        protocols: [ProtocolConfig],
        activeConfigID: String = "",
        protocolFailoverOrder: [VpnProtocol] = [],
        dnsOverride: String = "",
        verified: Bool = false,
        lastConnectedAt: Date? = nil,
        activeProtocol: VpnProtocol? = nil,
        createdAt: String = "",
        isFavorite: Bool = false
    ) {
        self.id = id
        self.name = name
        self.protocols = protocols
        self.activeConfigID = activeConfigID
        self.protocolFailoverOrder = protocolFailoverOrder
        self.dnsOverride = dnsOverride
        self.verified = verified
        self.lastConnectedAt = lastConnectedAt
        self.activeProtocol = activeProtocol
        self.createdAt = createdAt
        self.isFavorite = isFavorite
    }

    // JSON keys match Android/Desktop wire format (snake_case).
    private enum CodingKeys: String, CodingKey {
        case id
        case name
        case protocols
        case activeConfigID = "active_config_id"
        case protocolFailoverOrder = "protocol_failover_order"
        case dnsOverride = "dns_override"
        case verified
        case lastConnectedAt = "last_connected_at"
        case activeProtocol = "active_protocol"
        case createdAt = "created_at"
        case isFavorite = "is_favorite"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decode(String.self, forKey: .name)
        protocols = try c.decode([ProtocolConfig].self, forKey: .protocols)
        activeConfigID = try c.decodeIfPresent(String.self, forKey: .activeConfigID) ?? ""
        protocolFailoverOrder = try c.decodeIfPresent([VpnProtocol].self, forKey: .protocolFailoverOrder) ?? []
        dnsOverride = try c.decodeIfPresent(String.self, forKey: .dnsOverride) ?? ""
        verified = try c.decodeIfPresent(Bool.self, forKey: .verified) ?? false
        lastConnectedAt = try c.decodeIfPresent(Date.self, forKey: .lastConnectedAt)
        activeProtocol = try c.decodeIfPresent(VpnProtocol.self, forKey: .activeProtocol)
        createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt) ?? ""
        isFavorite = try c.decodeIfPresent(Bool.self, forKey: .isFavorite) ?? false
    }
}

/// One protocol-specific config attached to a [SavedConnection].
/// Multiple of these can live under one connection to support
/// multi-config-per-protocol (mirrors v0.9.15.18 Android model).
public struct ProtocolConfig: Codable, Identifiable, Equatable, Hashable {
    public let id: String
    public let `protocol`: VpnProtocol
    /// Filename of the imported config (`*.conf` / `*.ovpn` / `*.sswan`).
    public var filename: String
    /// User-set nickname for disambiguation when multiple configs of
    /// the same protocol exist under one connection. Empty falls
    /// back to filename or protocol name.
    public var nickname: String
    /// Raw config content. **Encrypted at rest via Keychain** —
    /// the on-disk representation is the Keychain reference, this
    /// field is populated only after `KeychainSecretStore.load(...)`.
    public var configContent: String
    /// Parsed remote endpoint. `host:port` form (IPv6 in brackets).
    public var serverAddress: String
    /// Inner/tunnel IP cached from the last successful connect (Android
    /// `local_address`). "" = unknown. Surfaced as "VPN IP" in the UI.
    public var localAddress: String
    /// RFC3339 timestamp this config was added (Android `added_at`).
    /// Drives stable multi-config ordering. "" = unknown.
    public var addedAt: String

    public init(
        id: String,
        protocol: VpnProtocol,
        filename: String,
        nickname: String = "",
        configContent: String = "",
        serverAddress: String = "",
        localAddress: String = "",
        addedAt: String = ""
    ) {
        self.id = id
        self.protocol = `protocol`
        self.filename = filename
        self.nickname = nickname
        self.configContent = configContent
        self.serverAddress = serverAddress
        self.localAddress = localAddress
        self.addedAt = addedAt
    }

    private enum CodingKeys: String, CodingKey {
        case id
        case `protocol`
        case filename
        case nickname
        case configContent = "config_content"
        case serverAddress = "server_address"
        case localAddress = "local_address"
        case addedAt = "added_at"
    }

    // Tolerant decoder — Swift's synthesized decoder THROWS on missing
    // keys (it ignores default values), which would break loading older
    // iOS data or Android backups lacking the newer optional keys. All
    // non-identity fields fall back to their defaults when absent.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        `protocol` = try c.decode(VpnProtocol.self, forKey: .protocol)
        filename = try c.decodeIfPresent(String.self, forKey: .filename) ?? ""
        nickname = try c.decodeIfPresent(String.self, forKey: .nickname) ?? ""
        configContent = try c.decodeIfPresent(String.self, forKey: .configContent) ?? ""
        serverAddress = try c.decodeIfPresent(String.self, forKey: .serverAddress) ?? ""
        localAddress = try c.decodeIfPresent(String.self, forKey: .localAddress) ?? ""
        addedAt = try c.decodeIfPresent(String.self, forKey: .addedAt) ?? ""
    }
}

public extension SavedConnection {
    /// The config to connect with. Honors the explicit `activeConfigID`;
    /// when it is unset or stale, picks the first config whose protocol
    /// comes earliest in the effective failover order (per-connection
    /// override → the supplied global order → the built-in default).
    /// Mirrors Android's default-protocol pick (AmneziaWG-first). Falls
    /// back to the first imported config if no protocol matches.
    func resolvedActiveConfig(globalOrder: [VpnProtocol] = []) -> ProtocolConfig? {
        if let c = protocols.first(where: { $0.id == activeConfigID }) { return c }
        let order = !protocolFailoverOrder.isEmpty
            ? protocolFailoverOrder
            : (!globalOrder.isEmpty ? globalOrder : VpnProtocol.defaultFailoverOrder)
        for proto in order {
            if let c = protocols.first(where: { $0.protocol == proto }) { return c }
        }
        return protocols.first
    }

    /// The config the Smart Decision Engine picks in active mode: the first
    /// config whose protocol comes earliest in `order` — deliberately IGNORING
    /// the manual `activeConfigID` pin, because the engine owns the choice.
    /// Falls back to the first imported config.
    func enginePickedConfig(order: [VpnProtocol]) -> ProtocolConfig? {
        for proto in order {
            if let c = protocols.first(where: { $0.protocol == proto }) { return c }
        }
        return protocols.first
    }
}

/// One of four supported VPN protocol classes. Stable serialised
/// values match Android `VpnProtocol` enum.
public enum VpnProtocol: String, Codable, CaseIterable, Identifiable, Hashable {
    case amneziawg = "amneziawg"
    case wireguard = "wireguard"
    case openvpn = "openvpn"
    case ipsec = "ipsec"

    public var id: String { rawValue }

    /// Display name for UI surfaces. Localised at the call site via
    /// `String(localized:)` if needed; the value here is the
    /// canonical brand label (always English-spelled).
    public var displayName: String {
        switch self {
        case .amneziawg: return "AmneziaWG"
        case .wireguard: return "WireGuard"
        case .openvpn: return "OpenVPN"
        case .ipsec: return "IPSec"
        }
    }

    /// Default order applied when `SavedConnection.protocolFailoverOrder`
    /// is empty. Mirrors Android default (`amneziawg → wireguard → openvpn → ipsec`).
    public static let defaultFailoverOrder: [VpnProtocol] = [
        .amneziawg, .wireguard, .openvpn, .ipsec,
    ]
}
