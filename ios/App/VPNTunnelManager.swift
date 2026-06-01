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

    // Active-target metadata, recorded on connect so refreshStatus can
    // build a complete VpnStatus (the system gives us connected-state
    // only; name/protocol/stats we track ourselves).
    private var activeConnectionName = ""
    private var activeConnectionID = ""
    private var activeProtocol: VpnProtocol?
    private var isPTPTunnel = false
    private var onDemandEnabled = false
    /// Poll loop that pulls PTP stats from the App Group store while up.
    private var pollTask: Task<Void, Never>?

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
    /// `onDemand` = attach an NEOnDemandRule so iOS keeps the tunnel up /
    /// auto-reconnects on network change AND shows the system On-Demand
    /// toggle (Settings ▸ VPN ▸ (i)), like the WireGuard app. Gated by
    /// the app's auto-tunnel master (networkRulesEnabled).
    func connect(_ connection: SavedConnection, onDemand: Bool = false) async throws {
        guard let config = connection.protocols.first(where: { $0.id == connection.activeConfigID })
            ?? connection.protocols.first else {
            throw VPNError.noConfig
        }
        activeConnectionName = connection.name
        activeConnectionID = connection.id
        activeProtocol = config.protocol
        isPTPTunnel = config.protocol != .ipsec
        self.onDemandEnabled = onDemand

        if config.protocol == .ipsec {
            try await connectViaIKEv2(connection: connection, config: config)
        } else {
            try await connectViaPTP(connection: connection, config: config)
        }
        startPolling()
    }

    /// Baseline on-demand rule set: connect on ANY interface when enabled.
    /// The rule-aware per-SSID translation (connect/disconnect by network)
    /// is a later batch; this is what makes the system toggle appear + the
    /// tunnel auto-reconnect.
    private func onDemandRuleSet() -> [NEOnDemandRule] {
        let connectRule = NEOnDemandRuleConnect()
        connectRule.interfaceTypeMatch = .any
        return [connectRule]
    }

    func disconnect() async {
        pollTask?.cancel()
        pollTask = nil
        // Disable on-demand BEFORE stopping so iOS doesn't immediately
        // reconnect the tunnel we're tearing down (the WireGuard-app
        // behaviour: a manual disconnect turns off on-demand).
        let managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        for m in managers {
            if m.isOnDemandEnabled {
                m.isOnDemandEnabled = false
                try? await m.saveToPreferences()
            }
            m.connection.stopVPNTunnel()
        }
        let ike = NEVPNManager.shared()
        try? await ike.loadFromPreferences()
        if ike.isOnDemandEnabled {
            ike.isOnDemandEnabled = false
            try? await ike.saveToPreferences()
        }
        ike.connection.stopVPNTunnel()
        TunnelStatsStore.clear()
        activeProtocol = nil
        isPTPTunnel = false
        refreshStatus()
    }

    /// While a tunnel is up, poll the App Group stats store (PTP) once
    /// a second so the Connect screen shows live throughput. IKEv2 has
    /// no public byte API, so its rx/tx stay 0 (matches the Android
    /// limitation for the system-IKEv2 path).
    private func startPolling() {
        pollTask?.cancel()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                await MainActor.run { self?.refreshStatus() }
                try? await Task.sleep(nanoseconds: 1_000_000_000)
            }
        }
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
        if onDemandEnabled {
            mgr.onDemandRules = onDemandRuleSet()
            mgr.isOnDemandEnabled = true
        } else {
            mgr.isOnDemandEnabled = false
        }
        try await mgr.saveToPreferences()
        try await mgr.loadFromPreferences()
        try mgr.connection.startVPNTunnel()
    }

    private func connectViaIKEv2(connection: SavedConnection, config: ProtocolConfig) async throws {
        // Parse the gateway's .sswan JSON profile into IKEv2 attributes.
        let profile = try SswanProfile.parse(config.configContent)

        let mgr = NEVPNManager.shared()
        try await mgr.loadFromPreferences()

        let proto = NEVPNProtocolIKEv2()
        proto.serverAddress = profile.remote.addr
        proto.remoteIdentifier = profile.resolvedRemoteIdentifier
        if let localID = profile.local.id, !localID.isEmpty {
            proto.localIdentifier = localID
        }

        // Certificate auth via inline PKCS#12 (client cert + key + CA
        // chain). identityData/identityDataPassword is exactly the
        // NEVPNProtocolIKEv2 inline-credential path — no Keychain
        // pre-install dance like Android needs.
        guard let p12 = profile.pkcs12Data else {
            throw SswanError.missingCertificate
        }
        proto.authenticationMethod = .certificate
        proto.identityData = p12
        proto.identityDataPassword = profile.local.p12Password
        proto.useExtendedAuthentication = false

        // Harden defaults — strong IKE/ESP ciphers, MOBIKE, PFS, DPD.
        proto.useConfigurationAttributeInternalIPSubnet = false
        proto.disableMOBIKE = false
        proto.disableRedirect = false
        proto.enablePFS = true
        proto.deadPeerDetectionRate = .medium
        let ike = proto.ikeSecurityAssociationParameters
        ike.encryptionAlgorithm = .algorithmAES256GCM
        ike.integrityAlgorithm = .SHA256
        ike.diffieHellmanGroup = .group19
        let esp = proto.childSecurityAssociationParameters
        esp.encryptionAlgorithm = .algorithmAES256GCM
        esp.integrityAlgorithm = .SHA256
        esp.diffieHellmanGroup = .group19
        if let mtu = profile.mtu, mtu > 0 {
            // NEVPNProtocolIKEv2 has no MTU knob; server-pushed config
            // governs it. Logged for parity, applied where supported.
            _ = mtu
        }

        mgr.protocolConfiguration = proto
        mgr.localizedDescription = connection.name
        mgr.isEnabled = true
        if onDemandEnabled {
            mgr.onDemandRules = onDemandRuleSet()
            mgr.isOnDemandEnabled = true
        } else {
            mgr.isOnDemandEnabled = false
        }
        try await mgr.saveToPreferences()
        try await mgr.loadFromPreferences()
        try mgr.connection.startVPNTunnel()
    }

    private func refreshStatus() {
        if isPTPTunnel {
            buildPTPStatus()
        } else {
            buildIKEv2Status()
        }
        for (_, c) in statusContinuations {
            c.yield(self.status)
        }
    }

    /// PTP protocols (WG / AWG / OVPN): connected-state from the
    /// NETunnelProviderManager session if available, rx/tx + endpoint
    /// from the App Group store the extension writes.
    private func buildPTPStatus() {
        let snap = TunnelStatsStore.read()
        let connected = snap?.connected ?? false
        let uptime: Int64
        if let at = snap?.connectedAtEpoch, at > 0, connected {
            uptime = max(0, Int64(Date().timeIntervalSince1970) - at)
        } else {
            uptime = 0
        }
        self.status = VpnStatus(
            connected: connected,
            connectionName: activeConnectionName,
            connectionID: activeConnectionID,
            activeProtocol: activeProtocol,
            uptime: uptime,
            rxBytes: snap?.rxBytes ?? 0,
            txBytes: snap?.txBytes ?? 0,
            localAddress: snap?.localAddress ?? "",
            serverEndpoint: snap?.serverEndpoint ?? "",
            error: snap?.lastError ?? ""
        )
    }

    /// IKEv2 Personal VPN: connected-state from NEVPNConnection. No
    /// public byte counters on iOS, so rx/tx remain 0.
    private func buildIKEv2Status() {
        let connection = NEVPNManager.shared().connection
        let connected = connection.status == .connected
        let uptime: Int64
        if connected, let since = connection.connectedDate {
            uptime = max(0, Int64(Date().timeIntervalSince(since)))
        } else {
            uptime = 0
        }
        self.status = VpnStatus(
            connected: connected,
            connectionName: activeConnectionName,
            connectionID: activeConnectionID,
            activeProtocol: activeProtocol ?? .ipsec,
            uptime: uptime,
            localAddress: status.localAddress,
            serverEndpoint: status.serverEndpoint
        )
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
