import Foundation
import NetworkExtension
import PrivycsCore

/// App-side wrapper um NEVPNManager + NETunnelProviderManager.
/// Steuert das Installieren der VPN-Profile (Personal-VPN-Prompt
/// beim ersten Connect) + Connect/Disconnect-Aktionen + State-
/// Observation.
///
/// Für IPSec wird NEVPNManager mit NEIKEv2VPNConfiguration
/// verwendet. Für WG/AWG/OVPN NETunnelProviderManager mit unserem
/// PrivycsPacketTunnelProvider als provider-bundle.
@MainActor
final class VPNTunnelManager: ObservableObject {

    @Published var status: VpnStatus = .disconnected

    private var statusContinuations: [UUID: AsyncStream<VpnStatus>.Continuation] = [:]
    private var observer: NSObjectProtocol?

    init() {
        // System-side NEVPNStatusDidChange notifications observer.
        observer = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            self?.refreshStatus()
        }
    }

    deinit {
        if let observer { NotificationCenter.default.removeObserver(observer) }
    }

    func observeStatus() -> AsyncStream<VpnStatus> {
        let id = UUID()
        return AsyncStream(bufferingPolicy: .bufferingNewest(4)) { continuation in
            self.statusContinuations[id] = continuation
            continuation.yield(self.status)
            continuation.onTermination = { _ in
                Task { @MainActor in self.statusContinuations.removeValue(forKey: id) }
            }
        }
    }

    /// Verbindet eine SavedConnection. Wählt den active config aus,
    /// installiert (oder reuses) ein NETunnelProviderManager-Profil,
    /// und startet den Tunnel. Wenn das active config IPSec ist,
    /// nimmt der NEVPNManager-IKEv2-Pfad statt PTP.
    func connect(_ connection: SavedConnection) async throws {
        guard let config = connection.protocols.first(where: { $0.id == connection.activeConfigID })
            ?? connection.protocols.first else {
            throw VPNError.noConfig
        }
        if config.protocol == .ipsec {
            try await connectViaIKEv2(connection: connection, config: config)
        } else {
            try await connectViaPTP(connection: connection, config: config)
        }
    }

    func disconnect() async {
        let managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        for m in managers {
            m.connection.stopVPNTunnel()
        }
        // Personal-VPN (IPSec) — load + stop. loadFromPreferences returns Void;
        // we only care it ran without throw so connection is fresh.
        try? await NEVPNManager.shared().loadFromPreferences()
        NEVPNManager.shared().connection.stopVPNTunnel()
    }

    // MARK: — Private

    private func connectViaPTP(connection: SavedConnection, config: ProtocolConfig) async throws {
        let managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        let mgr = managers.first { $0.localizedDescription == connection.name } ?? NETunnelProviderManager()
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = "com.privycs.vpn.tunnel"
        proto.serverAddress = config.serverAddress
        proto.providerConfiguration = [
            "protocol": config.protocol.rawValue,
            "config_content": config.configContent,
            "connection_id": connection.id,
            "config_id": config.id,
        ]
        mgr.protocolConfiguration = proto
        mgr.localizedDescription = connection.name
        mgr.isEnabled = true
        try await mgr.saveToPreferences()
        try await mgr.loadFromPreferences()
        try mgr.connection.startVPNTunnel()
    }

    private func connectViaIKEv2(connection: SavedConnection, config: ProtocolConfig) async throws {
        let mgr = NEVPNManager.shared()
        try await mgr.loadFromPreferences()
        let proto = NEVPNProtocolIKEv2()
        proto.serverAddress = config.serverAddress
        proto.remoteIdentifier = config.serverAddress
        proto.useExtendedAuthentication = false
        // TODO Phase 3: parse config.configContent (.sswan /
        // .mobileconfig) zu IKEv2-attributes (certs, EAP, etc.)
        mgr.protocolConfiguration = proto
        mgr.localizedDescription = connection.name
        mgr.isEnabled = true
        try await mgr.saveToPreferences()
        try await mgr.loadFromPreferences()
        try mgr.connection.startVPNTunnel()
    }

    private func refreshStatus() {
        // Map NEVPNStatus zur PrivycsCore VpnStatus.
        let connection = NEVPNManager.shared().connection
        let connected = connection.status == .connected
        self.status = VpnStatus(
            connected: connected,
            connectionName: status.connectionName,
            connectionID: status.connectionID,
            activeProtocol: status.activeProtocol,
            uptime: connected ? status.uptime : 0,
            rxBytes: status.rxBytes,
            txBytes: status.txBytes
        )
        for (_, c) in statusContinuations {
            c.yield(self.status)
        }
    }
}

enum VPNError: LocalizedError {
    case noConfig
    var errorDescription: String? {
        switch self {
        case .noConfig: return "No protocol config selected"
        }
    }
}
