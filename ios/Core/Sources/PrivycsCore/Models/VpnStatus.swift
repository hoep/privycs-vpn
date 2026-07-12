import Foundation

/// Live tunnel status. Produced by the PacketTunnelProvider and
/// observed by the App-side ViewModels. Mirror der Android
/// `VpnStatus` data class, gleiche Field-Semantik.
public struct VpnStatus: Equatable, Sendable {
    /// True wenn ein Tunnel up ist UND mindestens ein Handshake
    /// erfolgreich war. False während connecting / disconnecting /
    /// idle.
    public let connected: Bool

    /// Display-Name der aktiven Verbindung (oder Pool).
    public let connectionName: String

    /// Stable ID der aktiven Verbindung / Pool. "pool:<id>" Prefix
    /// für Pools.
    public let connectionID: String

    /// Aktuell aktives Protokoll.
    public let activeProtocol: VpnProtocol?

    /// Uptime in Sekunden seit erfolgreicher Verbindung.
    public let uptime: Int64

    /// Cumulative Bytes empfangen seit Verbindungsstart.
    public let rxBytes: Int64

    /// Cumulative Bytes gesendet seit Verbindungsstart.
    public let txBytes: Int64

    /// Local virtual IP des Tunnel-Interfaces. IPv4 + IPv6 möglich.
    public let localAddress: String

    /// Server-Endpoint (host:port).
    public let serverEndpoint: String

    /// Letzter erfolgreicher WireGuard/AmneziaWG-Handshake als
    /// menschenlesbare "vor X" Zeichenkette ("" wenn unbekannt oder
    /// Protokoll ohne Handshake-Tracking — OpenVPN/IPSec). Analog
    /// Android VpnStatus.lastHandshake.
    public let lastHandshake: String

    /// Server-Country (ISO 3166-1 Alpha-2). Aus Pool-Member oder
    /// Hostname-Pattern.
    public let serverCountryCode: String

    /// Error message wenn connected=false und letzter Versuch
    /// fehlgeschlagen ist. "" wenn idle.
    public let error: String

    // ---- Pool-spezifische Felder (leer wenn kein Pool aktiv) ----

    public let poolID: String
    public let poolName: String
    public let poolPolicy: PoolPolicy?
    public let activeMemberID: String
    public let activeMemberName: String
    public let activeMemberCountry: String
    public let pendingMemberName: String
    public let pendingMemberCountry: String

    /// UNIX-Timestamp der nächsten Rotation. 0 = keine Rotation
    /// geplant.
    public let nextRotationAt: Int64

    /// Epoch-MILLIsekunden, zu denen rxBytes/txBytes ERHOBEN wurden — die Uhr des
    /// Produzenten, nicht die des Lesers. Für PTP-Protokolle stammt der Wert aus
    /// dem App-Group-Snapshot der Extension; für IKEv2 (Live-Syscall gegen
    /// NEVPNConnection) ist es schlicht der Lesezeitpunkt. Der Throughput-Tracker
    /// misst sein dt daran und überspringt Ticks, bei denen sich der Stempel nicht
    /// bewegt hat — sonst aliast der freilaufende 1s-Poller der App gegen den
    /// freilaufenden 1s-Writer der Extension und die Anzeige springt zwischen
    /// 0 B/s und dem doppelten Wert. 0 = unbekannt (alter Snapshot).
    public let countersAtEpochMs: Int64

    public init(
        connected: Bool = false,
        connectionName: String = "",
        connectionID: String = "",
        activeProtocol: VpnProtocol? = nil,
        uptime: Int64 = 0,
        rxBytes: Int64 = 0,
        txBytes: Int64 = 0,
        localAddress: String = "",
        serverEndpoint: String = "",
        lastHandshake: String = "",
        serverCountryCode: String = "",
        error: String = "",
        poolID: String = "",
        poolName: String = "",
        poolPolicy: PoolPolicy? = nil,
        activeMemberID: String = "",
        activeMemberName: String = "",
        activeMemberCountry: String = "",
        pendingMemberName: String = "",
        pendingMemberCountry: String = "",
        nextRotationAt: Int64 = 0,
        countersAtEpochMs: Int64 = 0
    ) {
        self.connected = connected
        self.connectionName = connectionName
        self.connectionID = connectionID
        self.activeProtocol = activeProtocol
        self.uptime = uptime
        self.rxBytes = rxBytes
        self.txBytes = txBytes
        self.localAddress = localAddress
        self.serverEndpoint = serverEndpoint
        self.lastHandshake = lastHandshake
        self.serverCountryCode = serverCountryCode
        self.error = error
        self.poolID = poolID
        self.poolName = poolName
        self.poolPolicy = poolPolicy
        self.activeMemberID = activeMemberID
        self.activeMemberName = activeMemberName
        self.activeMemberCountry = activeMemberCountry
        self.pendingMemberName = pendingMemberName
        self.pendingMemberCountry = pendingMemberCountry
        self.nextRotationAt = nextRotationAt
        self.countersAtEpochMs = countersAtEpochMs
    }

    public static let disconnected = VpnStatus()
}
