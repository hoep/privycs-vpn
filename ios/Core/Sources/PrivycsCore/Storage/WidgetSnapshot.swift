import Foundation

/// Rich home-screen-widget state, written by the MAIN APP into the shared
/// App Group and read by the WidgetKit extension. This complements the
/// live `TunnelStatsSnapshot` (written by the packet-tunnel extension):
///
///   • `TunnelStatsSnapshot` — authoritative for connected / rx / tx /
///     endpoint / localAddress while a PTP tunnel runs, refreshed by the
///     extension even when the app is dead.
///   • `WidgetSnapshot` — server identity + selection context the tunnel
///     doesn't know (connection / pool / member names, country code, the
///     available-protocol list for the pill row, pause state) plus the
///     speed history for the sparkline. Written by the app whenever its
///     status or selection changes; may lag the live snapshot when the
///     app isn't running, so the widget MERGES the two (live wins for
///     connected-state + traffic, this wins for identity).
///
/// Deliberately imports nothing platform-specific (no NetworkExtension)
/// so PrivycsCore keeps building + unit-testing on Linux.

/// One packet-tunnel protocol the active connection offers, with everything
/// the widget's in-place switch needs PRE-RESOLVED by the app (so the widget
/// duplicates no DNS/kill-switch logic — it only assembles the
/// `providerConfiguration` via `TunnelProviderConfig.make`). Only in-place-
/// switchable protocols (WG/AWG/OpenVPN) are emitted here; IPSec is omitted
/// and the widget opens the app for it.
public struct WidgetSwitchTarget: Codable, Equatable, Sendable {
    public var protocolRaw: String
    public var configId: String
    public var configContent: String
    public var serverAddress: String
    public var dnsOverride: String
    public init(protocolRaw: String = "", configId: String = "", configContent: String = "",
                serverAddress: String = "", dnsOverride: String = "") {
        self.protocolRaw = protocolRaw
        self.configId = configId
        self.configContent = configContent
        self.serverAddress = serverAddress
        self.dnsOverride = dnsOverride
    }
}

public struct WidgetSnapshot: Codable, Equatable, Sendable {
    /// The app's last-known connected state (advisory — the widget prefers
    /// the live `TunnelStatsSnapshot.connected` when present).
    public var connected: Bool
    /// `true` while a manual pause is active (widget shows a paused affordance).
    public var paused: Bool
    /// Active protocol raw value ("wireguard"/"amneziawg"/"openvpn"/"ipsec"), "" if none.
    public var protocolRaw: String
    /// Protocols the active connection offers, in display order — drives the
    /// pill row. Empty ⇒ hide the pill row (e.g. a pool is selected).
    public var availableProtocols: [String]
    /// `true` when the current selection is a pool (Android hides the pill row).
    public var isPool: Bool

    // Server identity (Android widget blocks 1/5/7/8)
    public var connectionName: String
    public var poolName: String
    public var memberName: String
    /// ISO-3166-1 alpha-2, "" if unknown. Widget renders flag + localized name.
    public var countryCode: String
    public var serverEndpoint: String
    public var localAddress: String

    // Traffic (totals authoritative-ish; histories for the sparkline)
    public var rxBytes: Int64
    public var txBytes: Int64
    public var rxSpeed: Int64
    public var txSpeed: Int64
    /// Rolling speed history (bytes/sec), newest last, ≤ `historyLength`.
    public var rxHistory: [Double]
    public var txHistory: [Double]

    /// Epoch seconds the tunnel came up (0 = down) — widget uptime clock.
    public var connectedAtEpoch: Int64
    /// Epoch seconds this snapshot was written — widget staleness guard.
    public var updatedAtEpoch: Int64

    /// Active single-connection id (""=none/pool) — the manager the widget's
    /// in-place protocol switch reconfigures.
    public var connectionId: String
    /// Resolved kill-switch flag for the active connection (app-side setting).
    public var killSwitch: Bool
    /// In-place-switchable protocol targets for the active connection (empty
    /// for pools / IPSec-only). Drives the interactive pill row.
    public var switchTargets: [WidgetSwitchTarget]

    /// Matches Android's `SpeedTracker.HISTORY_LEN` so the sparkline ports 1:1.
    public static let historyLength = 30

    public init(
        connected: Bool = false,
        paused: Bool = false,
        protocolRaw: String = "",
        availableProtocols: [String] = [],
        isPool: Bool = false,
        connectionName: String = "",
        poolName: String = "",
        memberName: String = "",
        countryCode: String = "",
        serverEndpoint: String = "",
        localAddress: String = "",
        rxBytes: Int64 = 0,
        txBytes: Int64 = 0,
        rxSpeed: Int64 = 0,
        txSpeed: Int64 = 0,
        rxHistory: [Double] = [],
        txHistory: [Double] = [],
        connectedAtEpoch: Int64 = 0,
        updatedAtEpoch: Int64 = 0,
        connectionId: String = "",
        killSwitch: Bool = false,
        switchTargets: [WidgetSwitchTarget] = []
    ) {
        self.connected = connected
        self.paused = paused
        self.protocolRaw = protocolRaw
        self.availableProtocols = availableProtocols
        self.isPool = isPool
        self.connectionName = connectionName
        self.poolName = poolName
        self.memberName = memberName
        self.countryCode = countryCode
        self.serverEndpoint = serverEndpoint
        self.localAddress = localAddress
        self.rxBytes = rxBytes
        self.txBytes = txBytes
        self.rxSpeed = rxSpeed
        self.txSpeed = txSpeed
        self.rxHistory = rxHistory
        self.txHistory = txHistory
        self.connectedAtEpoch = connectedAtEpoch
        self.updatedAtEpoch = updatedAtEpoch
        self.connectionId = connectionId
        self.killSwitch = killSwitch
        self.switchTargets = switchTargets
    }

    private enum CodingKeys: String, CodingKey {
        case connected, paused, protocolRaw, availableProtocols, isPool
        case connectionName, poolName, memberName, countryCode, serverEndpoint, localAddress
        case rxBytes, txBytes, rxSpeed, txSpeed, rxHistory, txHistory
        case connectedAtEpoch, updatedAtEpoch
        case connectionId, killSwitch, switchTargets
    }

    /// Tolerant decoder — a snapshot written by an older build (missing
    /// fields) still decodes instead of failing the widget read.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        connected = try c.decodeIfPresent(Bool.self, forKey: .connected) ?? false
        paused = try c.decodeIfPresent(Bool.self, forKey: .paused) ?? false
        protocolRaw = try c.decodeIfPresent(String.self, forKey: .protocolRaw) ?? ""
        availableProtocols = try c.decodeIfPresent([String].self, forKey: .availableProtocols) ?? []
        isPool = try c.decodeIfPresent(Bool.self, forKey: .isPool) ?? false
        connectionName = try c.decodeIfPresent(String.self, forKey: .connectionName) ?? ""
        poolName = try c.decodeIfPresent(String.self, forKey: .poolName) ?? ""
        memberName = try c.decodeIfPresent(String.self, forKey: .memberName) ?? ""
        countryCode = try c.decodeIfPresent(String.self, forKey: .countryCode) ?? ""
        serverEndpoint = try c.decodeIfPresent(String.self, forKey: .serverEndpoint) ?? ""
        localAddress = try c.decodeIfPresent(String.self, forKey: .localAddress) ?? ""
        rxBytes = try c.decodeIfPresent(Int64.self, forKey: .rxBytes) ?? 0
        txBytes = try c.decodeIfPresent(Int64.self, forKey: .txBytes) ?? 0
        rxSpeed = try c.decodeIfPresent(Int64.self, forKey: .rxSpeed) ?? 0
        txSpeed = try c.decodeIfPresent(Int64.self, forKey: .txSpeed) ?? 0
        rxHistory = try c.decodeIfPresent([Double].self, forKey: .rxHistory) ?? []
        txHistory = try c.decodeIfPresent([Double].self, forKey: .txHistory) ?? []
        connectedAtEpoch = try c.decodeIfPresent(Int64.self, forKey: .connectedAtEpoch) ?? 0
        updatedAtEpoch = try c.decodeIfPresent(Int64.self, forKey: .updatedAtEpoch) ?? 0
        connectionId = try c.decodeIfPresent(String.self, forKey: .connectionId) ?? ""
        killSwitch = try c.decodeIfPresent(Bool.self, forKey: .killSwitch) ?? false
        switchTargets = try c.decodeIfPresent([WidgetSwitchTarget].self, forKey: .switchTargets) ?? []
    }
}

public enum WidgetSnapshotStore {
    public static let appGroup = "group.com.privycs.vpn"
    private static let key = "widget_snapshot"

    private static var defaults: UserDefaults? { UserDefaults(suiteName: appGroup) }

    /// Called from the main app whenever status or selection changes.
    public static func write(_ snapshot: WidgetSnapshot) {
        guard let d = defaults, let data = try? JSONEncoder().encode(snapshot) else { return }
        d.set(data, forKey: key)
    }

    /// Called from the WidgetKit extension's timeline provider.
    public static func read() -> WidgetSnapshot? {
        guard let d = defaults,
              let data = d.data(forKey: key),
              let snap = try? JSONDecoder().decode(WidgetSnapshot.self, from: data)
        else { return nil }
        return snap
    }

    public static func clear() { defaults?.removeObject(forKey: key) }
}
