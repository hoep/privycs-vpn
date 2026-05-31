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

    public init(
        id: String,
        name: String,
        protocols: [ProtocolConfig],
        activeConfigID: String = "",
        protocolFailoverOrder: [VpnProtocol] = [],
        dnsOverride: String = "",
        verified: Bool = false,
        lastConnectedAt: Date? = nil
    ) {
        self.id = id
        self.name = name
        self.protocols = protocols
        self.activeConfigID = activeConfigID
        self.protocolFailoverOrder = protocolFailoverOrder
        self.dnsOverride = dnsOverride
        self.verified = verified
        self.lastConnectedAt = lastConnectedAt
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

    public init(
        id: String,
        protocol: VpnProtocol,
        filename: String,
        nickname: String = "",
        configContent: String = "",
        serverAddress: String = ""
    ) {
        self.id = id
        self.protocol = `protocol`
        self.filename = filename
        self.nickname = nickname
        self.configContent = configContent
        self.serverAddress = serverAddress
    }

    private enum CodingKeys: String, CodingKey {
        case id
        case `protocol`
        case filename
        case nickname
        case configContent = "config_content"
        case serverAddress = "server_address"
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
