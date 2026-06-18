import Foundation

/// A Pool is a logical group of servers (members) with a selection
/// policy. Field-for-field mirror of the Android `Pool` data class so
/// the JSON snake_case wire format round-trips for cross-platform
/// backup export/import.
public struct Pool: Codable, Identifiable, Equatable, Hashable {
    public let id: String
    public var name: String
    /// RFC3339 creation timestamp (Android `created_at`). "" = unknown.
    public var createdAt: String
    public var policy: PoolPolicy
    public var members: [PoolMember]
    /// Rotation config. Nil = no automatic rotation.
    public var rotation: PoolRotation?
    /// iOS-runtime: currently selected member id. Android keeps active
    /// member in a separate state store, so `active_member_id` is an
    /// iOS-extra key Android tolerates (ignoreUnknownKeys) on import.
    public var activeMemberID: String
    public var splitTunnel: PoolSplitTunnel?
    /// Region restriction — members outside these are skipped by the picker.
    public var restrictRegions: [String]
    /// Country override for Geo-Nearest (else detected user location).
    public var countryOverride: String
    /// Per-pool DNS override.
    public var dnsOverride: String

    public init(
        id: String,
        name: String,
        policy: PoolPolicy,
        members: [PoolMember] = [],
        rotation: PoolRotation? = nil,
        activeMemberID: String = "",
        splitTunnel: PoolSplitTunnel? = nil,
        restrictRegions: [String] = [],
        countryOverride: String = "",
        dnsOverride: String = "",
        createdAt: String = ""
    ) {
        self.id = id
        self.name = name
        self.policy = policy
        self.members = members
        self.rotation = rotation
        self.activeMemberID = activeMemberID
        self.splitTunnel = splitTunnel
        self.restrictRegions = restrictRegions
        self.countryOverride = countryOverride
        self.dnsOverride = dnsOverride
        self.createdAt = createdAt
    }

    private enum CodingKeys: String, CodingKey {
        case id, name, policy, members, rotation
        case createdAt = "created_at"
        case activeMemberID = "active_member_id"
        case splitTunnel = "split_tunnel"
        case restrictRegions = "restrict_regions"
        case countryOverride = "country_override"
        case dnsOverride = "dns_override"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        policy = try c.decode(PoolPolicy.self, forKey: .policy)
        members = try c.decodeIfPresent([PoolMember].self, forKey: .members) ?? []
        rotation = try c.decodeIfPresent(PoolRotation.self, forKey: .rotation)
        activeMemberID = try c.decodeIfPresent(String.self, forKey: .activeMemberID) ?? ""
        splitTunnel = try c.decodeIfPresent(PoolSplitTunnel.self, forKey: .splitTunnel)
        restrictRegions = try c.decodeIfPresent([String].self, forKey: .restrictRegions) ?? []
        countryOverride = try c.decodeIfPresent(String.self, forKey: .countryOverride) ?? ""
        dnsOverride = try c.decodeIfPresent(String.self, forKey: .dnsOverride) ?? ""
        createdAt = try c.decodeIfPresent(String.self, forKey: .createdAt) ?? ""
    }
}

/// Per-pool selection strategy. Serialized values are byte-identical to
/// the Android `PoolPolicy` @SerialName values so pool backups
/// round-trip across platforms.
public enum PoolPolicy: String, Codable, CaseIterable, Identifiable, Hashable, Sendable {
    case geoNearest = "geo-nearest"
    case random = "random"
    case roundRobin = "round-robin-region"

    public var id: String { rawValue }

    public var displayName: String {
        switch self {
        case .geoNearest: return "Geo-Nearest"
        case .random: return "Random"
        case .roundRobin: return "Round-Robin"
        }
    }
}

/// One member in a pool — a server endpoint with geo metadata + a
/// protocol-specific config. Mirrors Android `PoolMember`: the config
/// is NESTED (`config`), and there is an `active` Pro-tier cap flag.
/// (Android has no lat/lon/index; round-robin uses a per-region
/// member-id cursor and geo-nearest uses region matching.)
public struct PoolMember: Codable, Identifiable, Equatable, Hashable {
    public let id: String
    /// Display name (e.g. "DE-Frankfurt-01"). Often parseable for geo.
    public var name: String
    /// Nested protocol config — Android wire format key `config`.
    public var config: ProtocolConfig
    /// ISO 3166-1 alpha-2. "" = unknown.
    public var country: String
    /// Continent-level region for geo-nearest cohorting + UI.
    public var region: String
    /// Pro-tier cap flag (Android `active`). Default true.
    public var active: Bool

    // Convenience forwarders so existing runtime code (member.protocol,
    // member.configContent, member.serverAddress) keeps working against
    // the nested config without touching every call site.
    public var `protocol`: VpnProtocol { config.protocol }
    public var configContent: String {
        get { config.configContent }
        set { config.configContent = newValue }
    }
    public var serverAddress: String { config.serverAddress }

    /// Primary init — nested config (Android-shaped).
    public init(
        id: String,
        name: String,
        config: ProtocolConfig,
        country: String = "",
        region: String = "",
        active: Bool = true
    ) {
        self.id = id
        self.name = name
        self.config = config
        self.country = country
        self.region = region
        self.active = active
    }

    /// Legacy flat init — builds the nested config from protocol +
    /// content. Keeps importer/test call sites compiling unchanged.
    public init(
        id: String,
        name: String,
        country: String = "",
        region: String = "",
        index: Int = 0,
        protocol proto: VpnProtocol,
        configContent: String = "",
        serverAddress: String = ""
    ) {
        self.init(
            id: id,
            name: name,
            config: ProtocolConfig(
                id: id, protocol: proto, filename: name,
                configContent: configContent, serverAddress: serverAddress
            ),
            country: country,
            region: region,
            active: true
        )
    }

    private enum CodingKeys: String, CodingKey {
        case id, name, config, country, region, active
        // legacy iOS flat keys (read-only fallback for pre-refactor data)
        case protocolLegacy = "protocol"
        case configContentLegacy = "config_content"
        case serverAddressLegacy = "server_address"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        country = try c.decodeIfPresent(String.self, forKey: .country) ?? ""
        region = try c.decodeIfPresent(String.self, forKey: .region) ?? ""
        active = try c.decodeIfPresent(Bool.self, forKey: .active) ?? true
        if let cfg = try c.decodeIfPresent(ProtocolConfig.self, forKey: .config) {
            config = cfg
        } else {
            // pre-refactor iOS flat form
            let proto = try c.decodeIfPresent(VpnProtocol.self, forKey: .protocolLegacy) ?? .wireguard
            let content = try c.decodeIfPresent(String.self, forKey: .configContentLegacy) ?? ""
            let srv = try c.decodeIfPresent(String.self, forKey: .serverAddressLegacy) ?? ""
            config = ProtocolConfig(id: id, protocol: proto, filename: name,
                                    configContent: content, serverAddress: srv)
        }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(name, forKey: .name)
        try c.encode(config, forKey: .config)
        try c.encode(country, forKey: .country)
        try c.encode(region, forKey: .region)
        try c.encode(active, forKey: .active)
    }
}

/// Rotation settings for a pool. Mirrors Android `PoolRotation`:
/// `interval_min` / `idle_aware` / `force_after_min` config fields.
/// Runtime state (last-used cursor + next-rotation timestamp) is carried
/// as iOS-extra keys Android tolerates on import.
public struct PoolRotation: Codable, Equatable, Hashable {
    /// Rotation interval in MINUTES (Android `interval_min`). 0 = no
    /// periodic rotation (manual + on-failure only).
    public var intervalMin: Int
    /// Skip rotation while the device is idle (Android `idle_aware`).
    public var idleAware: Bool
    /// Force-rotate after this many minutes even if idle (Android
    /// `force_after_min`).
    public var forceAfterMin: Int
    // iOS-runtime extras (Android ignores these unknown keys):
    /// Member id picked last (round-robin cursor — Android uses a
    /// per-region member-id cursor, not an index).
    public var lastUsedMemberID: String
    /// UNIX timestamp of the next due rotation. 0 = recompute on connect.
    public var nextRotationAt: Int64

    /// Convenience: interval in seconds for the runtime scheduler.
    public var intervalSeconds: Int {
        get { intervalMin * 60 }
        set { intervalMin = max(0, newValue / 60) }
    }

    public init(
        intervalMin: Int = 30,
        idleAware: Bool = true,
        forceAfterMin: Int = 60,
        lastUsedMemberID: String = "",
        nextRotationAt: Int64 = 0
    ) {
        self.intervalMin = intervalMin
        self.idleAware = idleAware
        self.forceAfterMin = forceAfterMin
        self.lastUsedMemberID = lastUsedMemberID
        self.nextRotationAt = nextRotationAt
    }

    /// Legacy seconds-based init — keeps existing call sites + tests
    /// compiling. Converts seconds → minutes.
    public init(intervalSeconds: Int, lastUsedIndex: Int = -1, nextRotationAt: Int64 = 0) {
        self.init(
            intervalMin: max(0, intervalSeconds / 60),
            idleAware: true,
            forceAfterMin: 60,
            lastUsedMemberID: "",
            nextRotationAt: nextRotationAt
        )
    }

    private enum CodingKeys: String, CodingKey {
        case intervalMin = "interval_min"
        case idleAware = "idle_aware"
        case forceAfterMin = "force_after_min"
        case lastUsedMemberID = "last_used_member_id"
        case nextRotationAt = "next_rotation_at"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        intervalMin = try c.decodeIfPresent(Int.self, forKey: .intervalMin) ?? 30
        idleAware = try c.decodeIfPresent(Bool.self, forKey: .idleAware) ?? true
        forceAfterMin = try c.decodeIfPresent(Int.self, forKey: .forceAfterMin) ?? 60
        lastUsedMemberID = try c.decodeIfPresent(String.self, forKey: .lastUsedMemberID) ?? ""
        nextRotationAt = try c.decodeIfPresent(Int64.self, forKey: .nextRotationAt) ?? 0
    }
}

/// Split-tunnel config of a pool. Mirrors Android `PoolSplitTunnel`:
/// a bypass-CIDR list + an exclude-private-networks toggle. The
/// `mode`/`cidrs` accessors adapt the iOS UI to this shape.
public struct PoolSplitTunnel: Codable, Equatable, Hashable {
    /// CIDRs that BYPASS the tunnel (go direct). Android `bypass_cidrs`.
    public var bypassCidrs: [String]
    /// Also bypass RFC1918 / link-local ranges. Android
    /// `exclude_private_networks`.
    public var excludePrivateNetworks: Bool

    public init(bypassCidrs: [String] = [], excludePrivateNetworks: Bool = false) {
        self.bypassCidrs = bypassCidrs
        self.excludePrivateNetworks = excludePrivateNetworks
    }

    private enum CodingKeys: String, CodingKey {
        case bypassCidrs = "bypass_cidrs"
        case excludePrivateNetworks = "exclude_private_networks"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        bypassCidrs = try c.decodeIfPresent([String].self, forKey: .bypassCidrs) ?? []
        excludePrivateNetworks = try c.decodeIfPresent(Bool.self, forKey: .excludePrivateNetworks) ?? false
    }

    // MARK: UI adapter (the iOS PoolDetailView speaks mode/cidrs)

    public enum SplitTunnelMode: String, Codable, CaseIterable, Hashable {
        case off
        case excludeListed = "exclude_listed"   // everything tunneled except cidrs (= bypass)
    }

    /// Compat accessor for the iOS UI. The Android model only expresses
    /// bypass semantics, so mode is off (empty) or excludeListed (bypass).
    public var mode: SplitTunnelMode {
        get { bypassCidrs.isEmpty && !excludePrivateNetworks ? .off : .excludeListed }
        set { if newValue == .off { bypassCidrs = []; excludePrivateNetworks = false } }
    }
    public var cidrs: [String] {
        get { bypassCidrs }
        set { bypassCidrs = newValue }
    }

    /// Compat init for the iOS UI's mode/cidrs form.
    public init(mode: SplitTunnelMode, cidrs: [String]) {
        self.bypassCidrs = mode == .off ? [] : cidrs
        self.excludePrivateNetworks = false
    }
}
