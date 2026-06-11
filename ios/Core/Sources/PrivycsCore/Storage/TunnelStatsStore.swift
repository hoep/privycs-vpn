import Foundation

/// Shared live-tunnel snapshot written by the PacketTunnelProvider
/// extension and read by the main app. NEPacketTunnelProvider runs in
/// a separate process with no shared memory, so we use the App Group
/// UserDefaults as a tiny message box. The PTP writes rx/tx + endpoint
/// + local address on a timer; the app polls this to drive the live
/// Connect-screen stats + sparklines.
///
/// IKEv2 (Personal VPN) does NOT use this — its byte counts come from
/// the system NEVPNConnection. This store is the PTP-protocols
/// (WireGuard / AmneziaWG / OpenVPN) channel.
public struct TunnelStatsSnapshot: Codable, Equatable, Sendable {
    public var connected: Bool
    public var rxBytes: Int64
    public var txBytes: Int64
    public var localAddress: String
    public var serverEndpoint: String
    public var protocolRaw: String
    /// Epoch seconds when the tunnel came up (0 = not up).
    public var connectedAtEpoch: Int64
    public var lastError: String
    /// Epoch seconds of the last WG/AWG handshake (0 = none / N/A).
    public var lastHandshakeEpoch: Int64
    /// Instantaneous throughput (bytes/sec) the tunnel computed from the last
    /// 1s byte delta — drives the live speed readout.
    public var rxSpeed: Int64
    public var txSpeed: Int64
    /// Recent throughput samples (bytes/sec, oldest→newest) the tunnel keeps so
    /// the widget sparkline reflects REAL recent traffic on each refresh (the
    /// app-written history is stale when the app is closed).
    public var rxHistory: [Double]
    public var txHistory: [Double]

    public init(
        connected: Bool = false,
        rxBytes: Int64 = 0,
        txBytes: Int64 = 0,
        localAddress: String = "",
        serverEndpoint: String = "",
        protocolRaw: String = "",
        connectedAtEpoch: Int64 = 0,
        lastError: String = "",
        lastHandshakeEpoch: Int64 = 0,
        rxSpeed: Int64 = 0,
        txSpeed: Int64 = 0,
        rxHistory: [Double] = [],
        txHistory: [Double] = []
    ) {
        self.connected = connected
        self.rxBytes = rxBytes
        self.txBytes = txBytes
        self.localAddress = localAddress
        self.serverEndpoint = serverEndpoint
        self.protocolRaw = protocolRaw
        self.connectedAtEpoch = connectedAtEpoch
        self.lastError = lastError
        self.lastHandshakeEpoch = lastHandshakeEpoch
        self.rxSpeed = rxSpeed
        self.txSpeed = txSpeed
        self.rxHistory = rxHistory
        self.txHistory = txHistory
    }

    private enum CodingKeys: String, CodingKey {
        case connected, rxBytes, txBytes, localAddress, serverEndpoint
        case protocolRaw, connectedAtEpoch, lastError, lastHandshakeEpoch
        case rxSpeed, txSpeed, rxHistory, txHistory
    }

    // Tolerant decoder so a snapshot written by an older build (without
    // lastHandshakeEpoch / speeds / history) still decodes instead of failing.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        connected = try c.decodeIfPresent(Bool.self, forKey: .connected) ?? false
        rxBytes = try c.decodeIfPresent(Int64.self, forKey: .rxBytes) ?? 0
        txBytes = try c.decodeIfPresent(Int64.self, forKey: .txBytes) ?? 0
        localAddress = try c.decodeIfPresent(String.self, forKey: .localAddress) ?? ""
        serverEndpoint = try c.decodeIfPresent(String.self, forKey: .serverEndpoint) ?? ""
        protocolRaw = try c.decodeIfPresent(String.self, forKey: .protocolRaw) ?? ""
        connectedAtEpoch = try c.decodeIfPresent(Int64.self, forKey: .connectedAtEpoch) ?? 0
        lastError = try c.decodeIfPresent(String.self, forKey: .lastError) ?? ""
        lastHandshakeEpoch = try c.decodeIfPresent(Int64.self, forKey: .lastHandshakeEpoch) ?? 0
        rxSpeed = try c.decodeIfPresent(Int64.self, forKey: .rxSpeed) ?? 0
        txSpeed = try c.decodeIfPresent(Int64.self, forKey: .txSpeed) ?? 0
        rxHistory = try c.decodeIfPresent([Double].self, forKey: .rxHistory) ?? []
        txHistory = try c.decodeIfPresent([Double].self, forKey: .txHistory) ?? []
    }
}

public enum TunnelStatsStore {
    public static let appGroup = "group.com.privycs.vpn"
    private static let key = "tunnel_stats_snapshot"

    private static var defaults: UserDefaults? {
        UserDefaults(suiteName: appGroup)
    }

    /// Called from the PTP extension to publish the latest snapshot.
    public static func write(_ snapshot: TunnelStatsSnapshot) {
        guard let d = defaults,
              let data = try? JSONEncoder().encode(snapshot) else { return }
        d.set(data, forKey: key)
    }

    /// Called from the main app to read the latest PTP-published stats.
    public static func read() -> TunnelStatsSnapshot? {
        guard let d = defaults,
              let data = d.data(forKey: key),
              let snap = try? JSONDecoder().decode(TunnelStatsSnapshot.self, from: data)
        else { return nil }
        return snap
    }

    /// Clear on disconnect so a stale snapshot doesn't linger.
    public static func clear() {
        defaults?.removeObject(forKey: key)
    }
}
