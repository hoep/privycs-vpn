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

        Task {
            do {
                try await bridge.start(providerConfig: providerConfig)
                completionHandler(nil)
            } catch {
                self.logger.error("PTP: bridge.start failed: \(error.localizedDescription)")
                completionHandler(error)
            }
        }
    }

    public override func stopTunnel(
        with reason: NEProviderStopReason,
        completionHandler: @escaping () -> Void
    ) {
        logger.info("PTP stopTunnel reason=\(reason.rawValue)")
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
