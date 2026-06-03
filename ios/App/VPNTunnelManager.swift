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
    /// Cache of all saved PTP managers — refreshed from preferences whenever
    /// the system VPN status changes, so `refreshStatus` can derive state
    /// from the ACTUAL connection (incl. tunnels iOS brought up via on-demand
    /// that the app never started itself).
    private var managers: [NETunnelProviderManager] = []

    init() {
        // System-side NEVPNStatusDidChange observer. CRITICAL: only re-derive
        // status from the ALREADY-CACHED managers here — do NOT reload from
        // preferences. Reloading per notification created fresh
        // NETunnelProviderManager/NEVPNConnection objects whose XPC sessions
        // leaked Mach ports → EXC_RESOURCE/PORT_SPACE crash (beta.22). Reading
        // a cached connection.status is cheap and allocates nothing. The cache
        // is (re)loaded only at launch + after explicit ops.
        observer = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            self?.refreshStatus()
        }
        Task { @MainActor in await reloadManagersAndRefresh() }   // one-time launch load
    }

    /// Reload all saved PTP managers and re-derive status from real system
    /// state. Allocates XPC sessions — call ONLY at launch + after explicit
    /// config ops (connect/disconnect/arm/disarm), NEVER from the status
    /// observer or a hot loop (that leaks Mach ports → crash).
    private func reloadManagersAndRefresh() async {
        managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        refreshStatus()
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
        await reloadManagersAndRefresh()   // pick up the just-started manager
        startPolling()
    }

    /// System-level on-demand rules. Translates the user's NetworkRules into
    /// `NEOnDemandRule` objects so iOS enforces them in the NE daemon even
    /// while the app is suspended/asleep (the app-side engine in AppState
    /// only runs in the foreground).
    ///
    /// Faithful mirror of `NetworkRulesEngine`: first-match-wins in priority
    /// order. The rule set is COMPLETE (every net → explicit Connect or
    /// Disconnect, never Ignore — WireGuard-exact); the terminal default is
    /// blocklist/allowlist-aware (see bottom of this function). iOS' NE daemon
    /// evaluates these rules in the background (incl. reliable SSID matching),
    /// so SSID-based automation works fg AND bg without the app polling.
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
        // Terminal rule — WireGuard-exact COMPLETE rule set: every network maps
        // to an explicit Connect or Disconnect (WG never uses Ignore). The
        // default for otherwise-unmatched nets mirrors WG's two modes:
        //   • Only "no-VPN" rule(s), NO connect rule ⇒ BLOCKLIST: VPN ON
        //     everywhere except the listed nets → Connect(.any) terminal
        //     (exactly WG's "exceptSpecificSSIDs"). A lone "disconnect on SSID
        //     X" rule therefore means "VPN on, but not on X".
        //   • A connect rule present, OR no rules at all ⇒ ALLOWLIST: VPN only
        //     where a Connect rule matched → Disconnect(.any) terminal. (No
        //     rules ⇒ off, so the master toggle alone never auto-connects
        //     everywhere — that was the old "connects no matter the rules".)
        let enabled = pendingRules.filter { $0.enabled }
        let hasConnect = enabled.contains {
            $0.action == .connectActive || $0.action == .connection || $0.action == .pool
        }
        // Only EXPRESSIBLE no-VPN rules flip the terminal to blocklist-connect.
        // A glob/BSSID no-VPN rule isn't encoded as a NEOnDemandRule (the
        // foreground engine handles it), so it must NOT make iOS connect on the
        // very net it can't exclude.
        let hasNoVpn = enabled.contains {
            $0.action == .noVpn && $0.matchType != .ssidPattern && $0.matchType != .bssid
        }
        let terminal: NEOnDemandRule = (hasNoVpn && !hasConnect)
            ? NEOnDemandRuleConnect() : NEOnDemandRuleDisconnect()
        terminal.interfaceTypeMatch = .any
        out.append(terminal)
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

    /// Stop the running tunnel session(s) WITHOUT touching on-demand — the
    /// WireGuard-app manual-disconnect model: on-demand stays armed so iOS
    /// reconnects where a connect rule applies (and the iOS Settings toggle
    /// keeps respecting the rules). Pause/master-off disarm explicitly via
    /// `disarmOnDemand()`.
    func stopTunnel() async {
        pollTask?.cancel()
        pollTask = nil
        let mgrs = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        for m in mgrs { m.connection.stopVPNTunnel() }
        NEVPNManager.shared().connection.stopVPNTunnel()
        TunnelStatsStore.clear()
        activeProtocol = nil
        isPTPTunnel = false
        await reloadManagersAndRefresh()
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
        let mgrs = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        let mgr = mgrs.first { $0.localizedDescription == connection.name } ?? NETunnelProviderManager()
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
        let mgrs = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        for m in mgrs where m.isOnDemandEnabled {
            m.isOnDemandEnabled = false
            try? await m.saveToPreferences()
        }
        let ike = NEVPNManager.shared()
        try? await ike.loadFromPreferences()
        if ike.isOnDemandEnabled { ike.isOnDemandEnabled = false; try? await ike.saveToPreferences() }
        await reloadManagersAndRefresh()
        PrivycsLog.log("on-demand disarmed (all managers)")
    }

    /// Persist the on-demand configuration (isOnDemandEnabled + faithful
    /// rules) on the connection's saved manager WITHOUT starting the tunnel.
    /// Keeps on-demand armed so iOS — and the iOS Settings VPN toggle —
    /// respect the rules even when the app isn't running, and the
    /// block-until-connect kill switch is armed where a connect rule applies.
    /// PTP only (WG/AWG/OVPN); IPSec/pools are not pre-armed.
    func armOnDemand(_ connection: SavedConnection, dnsOverride: String,
                     killSwitch: Bool, rules: [NetworkRule]) async throws {
        guard let config = connection.resolvedActiveConfig(), config.protocol != .ipsec else { return }
        // Identity drives onDemandRuleSet's per-connection mapping.
        activeConnectionID = connection.id
        activeConnectionName = connection.name
        self.dnsOverride = dnsOverride
        self.killSwitch = killSwitch
        self.pendingRules = rules
        let mgrs = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        let mgr = mgrs.first { $0.localizedDescription == connection.name } ?? NETunnelProviderManager()
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
        mgr.onDemandRules = onDemandRuleSet()
        mgr.isOnDemandEnabled = true
        try await mgr.saveToPreferences()
        await reloadManagersAndRefresh()
        PrivycsLog.log("on-demand armed (persistent config) — \(connection.name), rules=\(rules.count)")
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
        // Derive status from the ACTUAL system state, regardless of who
        // started the tunnel (manual connect OR iOS on-demand). Find a live
        // PTP manager; else fall back to the IKEv2 personal-VPN session.
        let livePTP = managers.first { m in
            switch m.connection.status {
            case .connected, .connecting, .reasserting: return true
            default: return false
            }
        }
        if let m = livePTP {
            buildPTPStatus(from: m)
        } else {
            let ikeStatus = NEVPNManager.shared().connection.status
            if ikeStatus == .connected || ikeStatus == .connecting || ikeStatus == .reasserting {
                buildIKEv2Status()
            } else {
                self.status = .disconnected
            }
        }
        for (_, c) in statusContinuations {
            c.yield(self.status)
        }
    }

    /// PTP protocols (WG / AWG / OVPN): connected-state + identity from the
    /// live NETunnelProviderManager (works for app-started AND iOS on-demand
    /// tunnels); rx/tx + endpoint + handshake from the App Group store the
    /// extension writes.
    private func buildPTPStatus(from m: NETunnelProviderManager) {
        let connected = m.connection.status == .connected
        let pc = (m.protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration
        let connName = m.localizedDescription ?? activeConnectionName
        let connID = (pc?["connection_id"] as? String) ?? activeConnectionID
        let proto = VpnProtocol(rawValue: (pc?["protocol"] as? String) ?? "") ?? activeProtocol
        let snap = TunnelStatsStore.read()
        let uptime: Int64
        if let at = snap?.connectedAtEpoch, at > 0, connected {
            uptime = max(0, Int64(Date().timeIntervalSince1970) - at)
        } else {
            uptime = 0
        }
        self.status = VpnStatus(
            connected: connected,
            connectionName: connName,
            connectionID: connID,
            activeProtocol: proto,
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
