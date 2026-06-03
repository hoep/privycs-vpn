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
    private var dnsOverride = ""
    private var killSwitch = true
    /// The user's network rules, translated into system NEOnDemandRule
    /// objects at connect time so iOS enforces them while the app is
    /// suspended (the app-side engine only runs in the foreground).
    private var pendingRules: [NetworkRule] = []
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
    func connect(_ connection: SavedConnection, onDemand: Bool = false, dnsOverride: String = "",
                 failoverOrder: [VpnProtocol] = [], killSwitch: Bool = true,
                 rules: [NetworkRule] = []) async throws {
        // Pick the config honoring the explicit active selection, then the
        // effective protocol-failover order (per-connection → global → default
        // AmneziaWG-first) rather than just the first imported config.
        guard let config = connection.resolvedActiveConfig(globalOrder: failoverOrder) else {
            throw VPNError.noConfig
        }
        activeConnectionName = connection.name
        activeConnectionID = connection.id
        activeProtocol = config.protocol
        isPTPTunnel = config.protocol != .ipsec
        self.onDemandEnabled = onDemand
        self.dnsOverride = dnsOverride
        self.killSwitch = killSwitch
        self.pendingRules = rules

        if config.protocol == .ipsec {
            try await connectViaIKEv2(connection: connection, config: config)
        } else {
            try await connectViaPTP(connection: connection, config: config)
        }
        startPolling()
    }

    /// System-level on-demand rules. Translates the user's NetworkRules into
    /// `NEOnDemandRule` objects so iOS enforces them in the NE daemon even
    /// while the app is suspended/asleep (the app-side engine in AppState
    /// only runs in the foreground).
    ///
    /// Faithful mirror of `NetworkRulesEngine`: first-match-wins in priority
    /// order, and — crucially — NO MATCH ⇒ **Ignore** (leave the tunnel in
    /// its current state), exactly like the engine's `RuleResolution.NoMatch`.
    /// (The previous trailing connect-on-any made iOS connect on EVERY network
    /// regardless of the configured rules — the "connects no matter what"
    /// bug.)
    ///
    /// A single manager's on-demand can only Connect/Disconnect/Ignore ITS OWN
    /// tunnel — the one we're arming (`activeConnectionID`). So each app rule
    /// is mapped to what should happen to THIS connection on the matched net:
    ///   • .noVpn                          → Disconnect (trusted network)
    ///   • .connectActive                  → Connect (this IS the active conn)
    ///   • .connection where target == self → Connect
    ///   • .connection where target != self → Disconnect (a different conn wins)
    ///   • .pool                           → Disconnect (pools rotate — the
    ///                                        foreground engine owns pool switch)
    /// Match types `.ssidPattern` (glob) and `.bssid` can't be expressed as a
    /// NEOnDemandRule (only exact SSID list + interface type) → skipped, so
    /// they stay foreground-only (AppState.evaluateAndApplyRules).
    private func onDemandRuleSet() -> [NEOnDemandRule] {
        var out: [NEOnDemandRule] = []
        for rule in pendingRules.sorted(by: { $0.priority < $1.priority }) where rule.enabled {
            // What should happen to the connection we're arming, on a match?
            let connectThis: Bool
            switch rule.action {
            case .noVpn:         connectThis = false
            case .connectActive: connectThis = true
            case .connection:    connectThis = (rule.targetId == activeConnectionID)
            case .pool:          connectThis = false
            }
            func mk() -> NEOnDemandRule {
                connectThis ? NEOnDemandRuleConnect() : NEOnDemandRuleDisconnect()
            }
            switch rule.matchType {
            case .ssidExact:
                guard !rule.matchValue.isEmpty else { continue }
                let r = mk()
                r.interfaceTypeMatch = .wiFi
                r.ssidMatch = [rule.matchValue]
                out.append(r)
            case .networkType:
                let r = mk()
                r.interfaceTypeMatch = Self.onDemandInterface(rule.matchValue)
                out.append(r)
            case .any:
                let r = mk()
                r.interfaceTypeMatch = .any
                out.append(r)
            case .ssidPattern, .bssid:
                continue   // not expressible as NEOnDemandRule — foreground-only
            }
        }
        // No rule matched ⇒ leave the tunnel in its current state — engine
        // parity (RuleResolution.NoMatch = take no action). NOT a blanket
        // connect.
        let ignore = NEOnDemandRuleIgnore()
        ignore.interfaceTypeMatch = .any
        out.append(ignore)
        return out
    }

    /// Map a network-type matchValue to an on-demand interface type.
    /// `ethernet` collapses to `.any` (no iOS ethernet on-demand interface).
    private static func onDemandInterface(_ v: String) -> NEOnDemandRuleInterfaceType {
        switch v.lowercased() {
        case "wifi":             return .wiFi
        case "mobile", "cellular": return .cellular
        default:                 return .any
        }
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

    /// Start the tunnel with the WireGuard-app's stale-config retry. The very
    /// first `startVPNTunnel()` after a fresh `saveToPreferences()` frequently
    /// throws `NEVPNError.configurationStale`/`.configurationInvalid`; the OS
    /// needs the manager reloaded before it accepts the start. WG retries up to
    /// 8× (reload → retry). Without this a fresh connect/auto-arm silently fails
    /// — a prime cause of "doesn't connect in doze".
    private func startTunnelRetrying(_ mgr: NEVPNManager, attempt: Int = 0) async throws {
        do {
            try mgr.connection.startVPNTunnel()
        } catch let err as NEVPNError where
            (err.code == .configurationStale || err.code == .configurationInvalid) && attempt < 8 {
            PrivycsLog.log("startTunnel stale/invalid (attempt \(attempt)) — reloading + retrying")
            try await mgr.loadFromPreferences()
            try await startTunnelRetrying(mgr, attempt: attempt + 1)
        } catch {
            throw error
        }
    }

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
            "dns_override": dnsOverride,
            "killSwitch": killSwitch,
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
        try await startTunnelRetrying(mgr)
    }

    /// Disarm persistent on-demand on every saved manager (master toggle off,
    /// or manual disconnect in mode A) so iOS stops auto-connecting — without
    /// deleting the saved configuration.
    func disarmOnDemand() async {
        let managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        for m in managers where m.isOnDemandEnabled {
            m.isOnDemandEnabled = false
            try? await m.saveToPreferences()
        }
        let ike = NEVPNManager.shared()
        try? await ike.loadFromPreferences()
        if ike.isOnDemandEnabled { ike.isOnDemandEnabled = false; try? await ike.saveToPreferences() }
        PrivycsLog.log("on-demand disarmed (all managers)")
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

        // Diagnostic: the most common IKEv2 failure on iOS is server-cert
        // validation — a self-signed gateway CA is trusted by Android's
        // strongSwan but NOT auto-trusted by iOS NEVPNManager (which checks
        // the system trust store). Log key params so a failed connect can be
        // traced from the device log.
        PrivycsLog.log("IPSec/IKEv2 connecting — server=\(profile.remote.addr) remoteID=\(proto.remoteIdentifier ?? "?") cert=\(profile.pkcs12Data != nil)")

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
        try await startTunnelRetrying(mgr)
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
            lastHandshake: Self.formatHandshakeAge(snap?.lastHandshakeEpoch ?? 0),
            error: snap?.lastError ?? ""
        )
    }

    /// Format a last-handshake epoch as a human "x ago" string (WG/AWG
    /// only; "" when unknown). Mirrors Android's lastHandshake display.
    static func formatHandshakeAge(_ epoch: Int64) -> String {
        guard epoch > 0 else { return "" }
        let age = Int64(Date().timeIntervalSince1970) - epoch
        if age < 0 { return "just now" }
        if age < 2 { return "1 second ago" }
        if age < 60 { return "\(age) seconds ago" }
        if age < 120 { return "1 minute ago" }
        if age < 3600 { return "\(age / 60) minutes ago" }
        if age < 7200 { return "1 hour ago" }
        return "\(age / 3600) hours ago"
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
