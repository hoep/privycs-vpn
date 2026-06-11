import NetworkExtension
import PrivycsCore
#if os(iOS)
import WidgetKit   // not available to the tvOS tunnel target
#endif
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

    /// The WireGuardKit / OpenVPNAdapter call this to apply the tunnel's
    /// network settings. We override ONLY to log the EXACT IPv4/IPv6
    /// interface addresses + IPv6 included routes the OS actually receives.
    /// The adapter's own log prints route COUNTS but not the v6 SOURCE
    /// address — the missing datum for diagnosing "AmneziaWG has no IPv6"
    /// (a tunnel with a v6 route but no v6 source address can't originate v6).
    public override func setTunnelNetworkSettings(_ tunnelNetworkSettings: NETunnelNetworkSettings?,
                                                  completionHandler: ((Error?) -> Void)? = nil) {
        if let s = tunnelNetworkSettings as? NEPacketTunnelNetworkSettings {
            let v4 = s.ipv4Settings?.addresses ?? []
            let v6 = s.ipv6Settings?.addresses ?? []
            let v6routes = (s.ipv6Settings?.includedRoutes ?? [])
                .map { "\($0.destinationAddress)/\($0.destinationNetworkPrefixLength)" }
            PrivycsLog.log("PTP applied settings — v4-addr=\(v4) v6-addr=\(v6) v6-inc-routes=\(v6routes) dns=\(s.dnsSettings?.servers ?? [])")
        }
        super.setTunnelNetworkSettings(tunnelNetworkSettings, completionHandler: completionHandler)
    }

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
            // OpenVPNAdapter (ss-abramchuk / OpenVPN3) is iOS/macOS-only — it
            // does NOT build for tvOS. The tvOS tunnel target therefore does
            // NOT link OpenVPNAdapter, so `canImport(OpenVPNAdapter)` is false
            // there and the OpenVPN branch compiles out (the OpenVPNBridge body
            // is likewise `#if canImport(OpenVPNAdapter)`-guarded). On iOS the
            // tunnel links OpenVPNAdapter, so this branch is present unchanged.
#if canImport(OpenVPNAdapter)
            bridge = OpenVPNBridge(provider: self)
#else
            logger.error("PTP: OpenVPN is not supported on this platform (tvOS)")
            completionHandler(TunnelError.unsupportedProtocol(proto))
            return
#endif
        case .ipsec:
            // Sollte hier nie ankommen — IPSec geht via NEVPNManager
            // direkt, NICHT durch PTP. Defensive log + reject.
            logger.error("PTP: IPSec routes via NEVPNManager, not PTP")
            completionHandler(TunnelError.unsupportedProtocol(proto))
            return
        }
        activeBridge = bridge
        self.protocolRaw = proto.rawValue

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
            var tick = 0
            var prevRx: Int64 = 0, prevTx: Int64 = 0
            var havePrev = false
            while !Task.isCancelled {
                guard let self, let bridge = self.activeBridge else { break }
                let s = await bridge.currentStats()
                // ~1s loop → byte delta ≈ bytes/sec. Published so the widget can
                // project the counter forward between iOS's throttled refreshes.
                let rxSpeed = havePrev ? max(0, s.rx - prevRx) : 0
                let txSpeed = havePrev ? max(0, s.tx - prevTx) : 0
                prevRx = s.rx; prevTx = s.tx; havePrev = true
                TunnelStatsStore.write(TunnelStatsSnapshot(
                    connected: true,
                    rxBytes: s.rx,
                    txBytes: s.tx,
                    localAddress: s.localAddress,
                    serverEndpoint: s.serverEndpoint,
                    protocolRaw: self.protocolRaw,
                    connectedAtEpoch: self.connectedAtEpoch,
                    lastHandshakeEpoch: s.lastHandshakeEpoch,
                    rxSpeed: rxSpeed,
                    txSpeed: txSpeed
                ))
                // Nudge the home-screen widget to re-read the fresh counters every
                // ~30s. iOS throttles widget refreshes (a closed-app widget can't
                // update per-second — OS budget), so this is best-effort.
                tick += 1
                #if os(iOS)
                if tick % 30 == 0 { WidgetCenter.shared.reloadAllTimelines() }
                #endif
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
    /// UNIX epoch (seconds) of the last successful WireGuard/AmneziaWG
    /// handshake. 0 = none yet / protocol without handshake tracking.
    public var lastHandshakeEpoch: Int64
    public init(rx: Int64 = 0, tx: Int64 = 0, localAddress: String = "",
                serverEndpoint: String = "", lastHandshakeEpoch: Int64 = 0) {
        self.rx = rx
        self.tx = tx
        self.localAddress = localAddress
        self.serverEndpoint = serverEndpoint
        self.lastHandshakeEpoch = lastHandshakeEpoch
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
