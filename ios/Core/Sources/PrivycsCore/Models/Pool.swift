import Foundation

/// A Pool ist eine logische Gruppe von Servern (Members) mit einer
/// Auswahl-Policy. Mirror der Android `Pool` data class — gleiche
/// JSON-snake_case Wire-Format für cross-platform Export/Import.
public struct Pool: Codable, Identifiable, Equatable, Hashable {
    public let id: String
    public var name: String
    public var policy: PoolPolicy
    public var members: [PoolMember]
    /// Rotation-Konfiguration. Nil = keine automatische Rotation.
    public var rotation: PoolRotation?
    /// Aktuell ausgewähltes Member im Pool, leer = noch keine Wahl.
    public var activeMemberID: String
    /// Beim Restore aus Backup gesetzte split-tunnel Konfiguration.
    public var splitTunnel: PoolSplitTunnel?
    /// Optionale Region-Einschränkung — Member außerhalb dieser
    /// Regionen werden vom Policy-Picker übersprungen.
    public var restrictRegions: [String]
    /// Country-Override (Geo-Nearest etc.) — wenn nicht leer, wird
    /// dieses Land statt der detektierten User-Location verwendet.
    public var countryOverride: String
    /// Per-pool DNS-Override.
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
        dnsOverride: String = ""
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
    }

    private enum CodingKeys: String, CodingKey {
        case id, name, policy, members, rotation
        case activeMemberID = "active_member_id"
        case splitTunnel = "split_tunnel"
        case restrictRegions = "restrict_regions"
        case countryOverride = "country_override"
        case dnsOverride = "dns_override"
    }
}

/// Per-pool Auswahl-Strategy. Stable serialised values matchen die
/// Android `PoolPolicy` enum-werte.
public enum PoolPolicy: String, Codable, CaseIterable, Identifiable, Hashable {
    /// Erstes Mitglied im definierten Index-Sortierschlüssel — keine
    /// Auswahl-Logik, lineare Reihenfolge.
    case firstAvailable = "first_available"
    /// Zufällige Auswahl, alle Mitglieder gleichwahrscheinlich.
    case random = "random"
    /// Geo-Nearest: Country → Region → Distance-Heuristik. Braucht
    /// SelfIp-Detection + bundled MMDB; ohne degradiert zu random.
    case geoNearest = "geo_nearest"
    /// Round-Robin via PoolRotation.lastUsedIndex + Wrap-Around.
    case roundRobin = "round_robin"

    public var id: String { rawValue }

    public var displayName: String {
        switch self {
        case .firstAvailable: return "First Available"
        case .random: return "Random"
        case .geoNearest: return "Geo-Nearest"
        case .roundRobin: return "Round Robin"
        }
    }
}

/// Ein einzelnes Member innerhalb eines Pools — repräsentiert einen
/// Server-Endpunkt mit Geo-Metadata + protokollspezifischer Config.
public struct PoolMember: Codable, Identifiable, Equatable, Hashable {
    public let id: String
    /// Anzeigename (z.B. "DE-Frankfurt-01"). Für Geo-Nearest oft
    /// per Hostname-Pattern automatisch parsbar.
    public var name: String
    /// ISO 3166-1 Alpha-2. Leer = unbekannt → Geo-Nearest fällt
    /// zurück auf alphabetisch-erste Region.
    public var country: String
    /// Stadtname / Region für UI-Anzeige + Sortierung.
    public var region: String
    /// Latitude (für distance-based scoring). NaN = no data.
    public var latitude: Double
    /// Longitude. NaN = no data.
    public var longitude: Double
    /// Index im Pool (für stable sort + Round-Robin-Pointer).
    public var index: Int
    /// Welches Protokoll bringt dieses Member mit?
    public var `protocol`: VpnProtocol
    /// Raw Config-Content für dieses Member (encrypted-at-rest via
    /// Keychain — Plaintext nur während Pool-Connect-Operation).
    public var configContent: String
    /// Hostname/IP des Servers — für UI-Display +
    /// duplicate-detection in Imports.
    public var serverAddress: String

    public init(
        id: String,
        name: String,
        country: String = "",
        region: String = "",
        latitude: Double = .nan,
        longitude: Double = .nan,
        index: Int,
        protocol: VpnProtocol,
        configContent: String = "",
        serverAddress: String = ""
    ) {
        self.id = id
        self.name = name
        self.country = country
        self.region = region
        self.latitude = latitude
        self.longitude = longitude
        self.index = index
        self.protocol = `protocol`
        self.configContent = configContent
        self.serverAddress = serverAddress
    }

    private enum CodingKeys: String, CodingKey {
        case id, name, country, region, latitude, longitude, index, `protocol`
        case configContent = "config_content"
        case serverAddress = "server_address"
    }
}

/// Rotation-Settings für einen Pool — wenn nicht-nil, rotiert der
/// PoolRotator periodisch zum nächsten Member.
public struct PoolRotation: Codable, Equatable, Hashable {
    /// Rotation-Intervall in Sekunden. 0 = keine periodische Rotation
    /// (nur manuell + bei Member-Failure).
    public var intervalSeconds: Int
    /// Index des zuletzt aktiven Members (für Round-Robin-Pointer).
    public var lastUsedIndex: Int
    /// UNIX-Timestamp wann die nächste Rotation fällig ist.
    /// 0 = unbestimmt (wird beim nächsten Connect berechnet).
    public var nextRotationAt: Int64

    public init(
        intervalSeconds: Int = 0,
        lastUsedIndex: Int = -1,
        nextRotationAt: Int64 = 0
    ) {
        self.intervalSeconds = intervalSeconds
        self.lastUsedIndex = lastUsedIndex
        self.nextRotationAt = nextRotationAt
    }

    private enum CodingKeys: String, CodingKey {
        case intervalSeconds = "interval_seconds"
        case lastUsedIndex = "last_used_index"
        case nextRotationAt = "next_rotation_at"
    }
}

/// Split-Tunnel Konfiguration eines Pools — welche CIDRs gehen durch
/// den Tunnel, welche bleiben am Default-Interface vorbei.
public struct PoolSplitTunnel: Codable, Equatable, Hashable {
    public var mode: SplitTunnelMode
    /// Liste von CIDRs ohne Whitespace. IPv4 + IPv6 erlaubt.
    public var cidrs: [String]

    public init(mode: SplitTunnelMode = .off, cidrs: [String] = []) {
        self.mode = mode
        self.cidrs = cidrs
    }

    public enum SplitTunnelMode: String, Codable, CaseIterable, Hashable {
        case off
        /// Nur die in `cidrs` enthaltenen Networks gehen durch den Tunnel.
        case includeOnly = "include_only"
        /// Alles geht durch den Tunnel AUSSER die in `cidrs` enthaltenen.
        case excludeListed = "exclude_listed"
    }
}
