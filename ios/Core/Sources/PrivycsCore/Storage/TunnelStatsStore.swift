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

    public init(
        connected: Bool = false,
        rxBytes: Int64 = 0,
        txBytes: Int64 = 0,
        localAddress: String = "",
        serverEndpoint: String = "",
        protocolRaw: String = "",
        connectedAtEpoch: Int64 = 0,
        lastError: String = "",
        lastHandshakeEpoch: Int64 = 0
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
    }

    private enum CodingKeys: String, CodingKey {
        case connected, rxBytes, txBytes, localAddress, serverEndpoint
        case protocolRaw, connectedAtEpoch, lastError, lastHandshakeEpoch
    }

    // Tolerant decoder so a snapshot written by an older build (without
    // lastHandshakeEpoch) still decodes instead of failing the read.
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
