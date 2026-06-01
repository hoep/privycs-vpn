import NetworkExtension
import PrivycsCore
import os

/// Apple Network Extension PacketTunnelProvider. Eines davon pro
/// Tunnel-Lifetime — wird vom System gestartet (cold-start),
/// stoppt sich selbst bei `cancelTunnel(...)`, hat ~10MB Memory-
/// Limit und KEIN UI-Access. Status-Communication zur Main-App
/// über `NWTCPConnection`-style IPC oder UserDefaults (App Group).
///
/// Diese Klasse dispatched zu protocol-spezifischen Bridges
/// (WireGuardBridge, AmneziaWGBridge, OpenVPNBridge). IPSec läuft
/// NICHT durch den PTP — der nutzt NEVPNManager mit
/// NEIKEv2VPNConfiguration aus der Main-App direkt.
public final class PrivycsPacketTunnelProvider: NEPacketTunnelProvider {

    /// Aktuell aktive Bridge (eine pro Tunnel-Lifetime).
    private var activeBridge: (any TunnelProtocolBridge)?

    /// Logger via os_log — landet im macOS/iOS Console.app unter
    /// subsystem com.privycs.vpn.tunnel.
    private let logger = Logger(subsystem: "com.privycs.vpn.tunnel", category: "PTP")

    /// Periodic stats reporter — publishes rx/tx + endpoint to the
    /// App Group store so the main app's Connect screen can render
    /// live throughput. Cancelled on stop.
    private var statsTask: Task<Void, Never>?
    private var protocolRaw: String = ""
    private var connectedAtEpoch: Int64 = 0

    // MARK: — NEPacketTunnelProvider override

    public override func startTunnel(
        options: [String: NSObject]?,
        completionHandler: @escaping (Error?) -> Void
    ) {
        logger.info("PacketTunnelProvider startTunnel")
        // Tunnel-Config kommt von der Main-App via
        // protocolConfiguration.providerConfiguration (set when
        // installing the NEVPNManager configuration). Erwartet
        // mind. den protocol-string + connection/pool-id.
        guard let providerConfig = (self.protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration,
              let protocolRaw = providerConfig["protocol"] as? String,
              let proto = VpnProtocol(rawValue: protocolRaw) else {
            logger.error("PTP: missing or invalid 'protocol' in providerConfiguration")
            completionHandler(TunnelError.missingProviderConfig)
            return
        }

        // Dispatch zur richtigen Bridge.
        let bridge: any TunnelProtocolBridge
        switch proto {
        case .wireguard:
            bridge = WireGuardBridge(provider: self)
        case .amneziawg:
            bridge = AmneziaWGBridge(provider: self)
        case .openvpn:
            bridge = OpenVPNBridge(provider: self)
        case .ipsec:
            // Sollte hier nie ankommen — IPSec geht via NEVPNManager
            // direkt, NICHT durch PTP. Defensive log + reject.
            logger.error("PTP: IPSec routes via NEVPNManager, not PTP")
            completionHandler(TunnelError.unsupportedProtocol(proto))
            return
        }
        activeBridge = bridge
        protocolRaw = proto.rawValue

        Task {
            do {
                try await bridge.start(providerConfig: providerConfig)
                self.connectedAtEpoch = Int64(Date().timeIntervalSince1970)
                self.startStatsReporting()
                PrivycsLog.log("tunnel up — \(proto.rawValue)")
                completionHandler(nil)
            } catch {
                self.logger.error("PTP: bridge.start failed: \(error.localizedDescription)")
                PrivycsLog.log("tunnel start failed (\(proto.rawValue)): \(error.localizedDescription)")
                TunnelStatsStore.write(TunnelStatsSnapshot(
                    connected: false, protocolRaw: self.protocolRaw,
                    lastError: error.localizedDescription))
                completionHandler(error)
            }
        }
    }

    /// 1s loop publishing the bridge's live stats to the App Group.
    private func startStatsReporting() {
        statsTask?.cancel()
        statsTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self, let bridge = self.activeBridge else { break }
                let s = await bridge.currentStats()
                TunnelStatsStore.write(TunnelStatsSnapshot(
                    connected: true,
                    rxBytes: s.rx,
                    txBytes: s.tx,
                    localAddress: s.localAddress,
                    serverEndpoint: s.serverEndpoint,
                    protocolRaw: self.protocolRaw,
                    connectedAtEpoch: self.connectedAtEpoch
                ))
                try? await Task.sleep(nanoseconds: 1_000_000_000)
            }
        }
    }

    public override func stopTunnel(
        with reason: NEProviderStopReason,
        completionHandler: @escaping () -> Void
    ) {
        logger.info("PTP stopTunnel reason=\(reason.rawValue)")
        PrivycsLog.log("tunnel down — reason \(reason.rawValue)")
        statsTask?.cancel()
        statsTask = nil
        TunnelStatsStore.clear()
        Task {
            await activeBridge?.stop(reason: reason)
            activeBridge = nil
            completionHandler()
        }
    }

    public override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)? = nil) {
        // Main-App ↔ PTP IPC channel. Erwartet JSON {"cmd": "..."}
        // mit befehlen wie "stats", "ping". Phase 2: nur stub.
        let response = "{\"ok\":true}".data(using: .utf8)
        completionHandler?(response)
    }
}

// MARK: — Protocol-Bridge interface

/// Einheitliches Interface das alle protokoll-spezifischen Bridges
/// implementieren. Async/await — die ist NE-Framework-freundlich
/// weil PTP's completionHandler-Pattern via Task wrapped wird.
public protocol TunnelProtocolBridge: AnyObject, Sendable {
    func start(providerConfig: [String: Any]) async throws
    func stop(reason: NEProviderStopReason) async
    /// Live throughput + tunnel info for the App-Group stats channel.
    /// Default returns zeros; bridges override where the backend
    /// exposes counters.
    func currentStats() async -> BridgeStats
}

public extension TunnelProtocolBridge {
    func currentStats() async -> BridgeStats { BridgeStats() }
}

/// Per-poll stats contribution from a bridge.
public struct BridgeStats: Sendable {
    public var rx: Int64
    public var tx: Int64
    public var localAddress: String
    public var serverEndpoint: String
    public init(rx: Int64 = 0, tx: Int64 = 0, localAddress: String = "", serverEndpoint: String = "") {
        self.rx = rx
        self.tx = tx
        self.localAddress = localAddress
        self.serverEndpoint = serverEndpoint
    }
}

public enum TunnelError: LocalizedError {
    case missingProviderConfig
    case unsupportedProtocol(VpnProtocol)
    case bridgeNotImplemented(VpnProtocol)
    case nativeFault(String)

    public var errorDescription: String? {
        switch self {
        case .missingProviderConfig:
            return "providerConfiguration is missing required keys"
        case .unsupportedProtocol(let p):
            return "Protocol \(p.rawValue) is not supported by the packet-tunnel provider"
        case .bridgeNotImplemented(let p):
            return "Bridge for protocol \(p.rawValue) is not yet implemented"
        case .nativeFault(let msg):
            return "Native tunnel fault: \(msg)"
        }
    }
}
