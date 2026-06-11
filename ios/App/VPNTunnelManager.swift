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

    /// Called once per protocol that FAILED to establish during a failover
    /// walk, so AppState can fold the outcome into the engine's adaptive (P4)
    /// stats. Success is recorded separately by AppState's status loop, so this
    /// fires only for failures (no double-count).
    var onConnectFailure: ((VpnProtocol) -> Void)?

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

    // IPSec (IKEv2) traffic: NEVPNConnection has no byte API, so we sum the
    // 64-bit kernel counters of the tunnel's ipsec* interface(s). We do NOT try
    // to pick the one live interface — an IPSec→IPSec switch moves the tunnel
    // ipsec0→ipsec1 with the old one lingering, and guessing wrong left the
    // counter at 0. Instead keep a per-interface baseline (first-sight) and sum
    // each interface's GROWTH since the session started: the dying interface
    // contributes ~0, the live one the real session bytes.
    private var ipsecIfaceBase: [String: (rx: Int64, tx: Int64)] = [:]
    private var ipsecTrafficLogged = false
    // connectedDate of the session we last baselined. Advancing = a NEW IKE
    // session (incl. an IPSec→IPSec profile switch), so drop the per-interface
    // baselines and re-capture against the new session.
    private var ipsecSessionDate: Date?
    // Server endpoint for the active IPSec connection, captured at connect (the
    // NEVPNConnection exposes no endpoint accessor), surfaced on the Connect
    // screen. The VPN IP is read live off the ipsec* interface in buildIKEv2Status.
    private var ipsecServerEndpoint = ""

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
                 rules: [NetworkRule] = [], engineOrder: [VpnProtocol]? = nil) async throws {
        // Build the ordered candidate configs for runtime auto-failover.
        //   • engineOrder set (Automatic protocol selection ON) → the engine's
        //     country/roaming/adaptive order, IGNORING the manual activeConfigID.
        //   • else → the explicit active pick leads, then the rest of the
        //     protocols in the effective failover order.
        // A single-config connection (or a synthesised pool member) yields ONE
        // candidate, so the loop runs once and behaves exactly like the prior
        // single-shot connect — pools keep their own member-failover in AppState.
        let candidates: [ProtocolConfig]
        if let eo = engineOrder {
            // Engine order already leads with the chosen protocol, so
            // orderedConfigs groups that protocol's configs first — same-
            // protocol-first holds.
            candidates = connection.orderedConfigs(order: eo)
        } else if let active = connection.resolvedActiveConfig(globalOrder: failoverOrder) {
            // Same-protocol-first: exhaust the active protocol's OTHER configs
            // (e.g. a second WireGuard endpoint) before switching protocol.
            // Promote the active protocol to the front of the order so its
            // siblings sort ahead of every other protocol; without this a
            // low-ranked active protocol would jump to a higher-ranked OTHER
            // protocol before trying its own siblings (the reported bug).
            let promoted = [active.protocol] + failoverOrder
            candidates = [active] + connection.orderedConfigs(order: promoted).filter { $0.id != active.id }
        } else {
            candidates = connection.orderedConfigs(order: failoverOrder)
        }
        guard !candidates.isEmpty else { throw VPNError.noConfig }

        activeConnectionName = connection.name
        activeConnectionID = connection.id
        self.onDemandEnabled = onDemand
        self.dnsOverride = dnsOverride
        self.killSwitch = killSwitch
        self.pendingRules = rules

        var lastError: Error?
        for (idx, config) in candidates.enumerated() {
            activeProtocol = config.protocol
            isPTPTunnel = config.protocol != .ipsec
            let isLast = idx == candidates.count - 1
            do {
                let conn: NEVPNConnection = config.protocol == .ipsec
                    ? try await connectViaIKEv2(connection: connection, config: config).connection
                    : try await connectViaPTP(connection: connection, config: config).connection
                // Last/only candidate → fire-and-forget exactly as before (start
                // returns, the health monitor catches any later drop). With more
                // candidates left, wait for the tunnel to actually establish and
                // fail over to the next protocol if it doesn't.
                // (`||` can't short-circuit an await — its RHS is a non-async
                // autoclosure — so evaluate the wait explicitly.)
                // IKEv2 (esp. with public-cert validation) can take longer to
                // establish than PTP and may blip through .disconnected during
                // the stale-config reload — give it more time + blip tolerance
                // so we don't falsely fail over to WireGuard.
                let waitTimeout: TimeInterval = config.protocol == .ipsec ? 30 : 12
                let established = isLast ? true : await waitForConnected(conn, timeout: waitTimeout)
                if established {
                    await reloadManagersAndRefresh()   // pick up the just-started manager
                    startPolling()
                    return
                }
                PrivycsLog.log("connect: \(config.protocol.rawValue) did not establish — failing over")
                onConnectFailure?(config.protocol)
                lastError = VPNError.tunnelDidNotEstablish(config.protocol)
                // Mid-loop teardown: skip the XPC manager reload — the next
                // candidate reloads preferences itself, and a successful connect
                // reloads once. (Reduces the redundant per-candidate reload.)
                await stopTunnelSessions(reload: false)
            } catch {
                PrivycsLog.log("connect: \(config.protocol.rawValue) start error: \(error.localizedDescription)")
                onConnectFailure?(config.protocol)
                lastError = error
                if !isLast { await stopTunnelSessions(reload: false) }
            }
        }
        throw lastError ?? VPNError.noConfig
    }

    /// Poll an NE connection until it reaches `.connected` or the window
    /// elapses. Returns early-false if it went active (connecting/reasserting)
    /// and then dropped back to disconnected — a real failure — so failover
    /// doesn't burn the whole window on an obviously-dead endpoint.
    private func waitForConnected(_ conn: NEVPNConnection, timeout: TimeInterval) async -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        var sawActivity = false
        var downStreak = 0
        while Date() < deadline {
            switch conn.status {
            case .connected: return true
            case .connecting, .reasserting: sawActivity = true; downStreak = 0
            case .disconnected, .invalid:
                if sawActivity {
                    // Tolerate a transient blip (IKEv2 stale-config reload bounces
                    // through .disconnected); only treat as failed once it
                    // persists (~1.2s) so we don't fail over a tunnel that's
                    // about to come up.
                    downStreak += 1
                    if downStreak >= 4 { return false }
                }
            default: break
            }
            try? await Task.sleep(nanoseconds: 300_000_000)
        }
        return conn.status == .connected
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
        // Iterate in ARRAY order — that is the real priority (the UI lists
        // "Rules (priority top-down)" and reorders the array; NetworkRulesEngine
        // evaluates `for rule in rules` in array order too). Do NOT sort by the
        // `priority` field: the UI's move/reorder updates the array but NOT the
        // stored `priority`, so a priority-sort scrambled the order — the
        // bottom "Always on" Connect(.any) landed ahead of a top
        // "SSID X → disconnect" rule, so iOS connected on X regardless. Array
        // order keeps iOS first-match identical to the foreground engine.
        for rule in pendingRules where rule.enabled {
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

    /// Does the current ruleset ever bring the tunnel UP? True iff there is at
    /// least one Connect-producing rule (allowlist) or an expressible no-VPN
    /// rule that flips the terminal to Connect(.any) (blocklist). When FALSE the
    /// only on-demand outcome is "Disconnect everywhere" — and enabling
    /// on-demand then makes iOS' NE daemon immediately tear down a manually
    /// started tunnel while never auto-connecting anything useful. That is the
    /// trap a fresh user hits with the master toggle ON but no rules yet: the
    /// Connect button looks broken because on-demand kills the session. So when
    /// this is false, callers MUST leave on-demand disabled — the master toggle
    /// alone, with no actionable rule, must never sabotage a manual connect.
    private func onDemandWouldConnect() -> Bool {
        let enabled = pendingRules.filter { $0.enabled }
        let hasConnect = enabled.contains {
            $0.action == .connectActive || $0.action == .connection || $0.action == .pool
        }
        let hasNoVpn = enabled.contains {
            $0.action == .noVpn && $0.matchType != .ssidPattern && $0.matchType != .bssid
        }
        return hasConnect || hasNoVpn
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
        await stopTunnelSessions(reload: true)
    }

    /// Stop the running session(s) and reset session state. `reload` controls
    /// the trailing `reloadManagersAndRefresh()` (an XPC preference reload):
    ///   • true  — public teardown (manual disconnect / pause / protocol-switch
    ///     / rule disconnect): refresh the cached managers so status reflects
    ///     the now-down tunnel.
    ///   • false — mid-failover-loop teardown in `connect()`: the very next
    ///     candidate's connectVia*/start reloads preferences anyway, and the
    ///     on-success branch reloads once; skipping the per-candidate reload
    ///     avoids a redundant XPC round-trip in the failover walk.
    private func stopTunnelSessions(reload: Bool) async {
        pollTask?.cancel()
        pollTask = nil
        let mgrs = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        for m in mgrs { m.connection.stopVPNTunnel() }
        NEVPNManager.shared().connection.stopVPNTunnel()
        TunnelStatsStore.clear()
        activeProtocol = nil
        isPTPTunnel = false
        ipsecIfaceBase.removeAll()
        ipsecSessionDate = nil
        ipsecTrafficLogged = false
        if reload { await reloadManagersAndRefresh() }
    }

    /// Enforce a SINGLE active VPN before starting one: disable every manager
    /// that isn't the one we're about to bring up. iOS happily keeps multiple
    /// managers `isEnabled` + on-demand-armed at once — so a stale WireGuard
    /// NETunnelProviderManager (armed from a prior session) fires while we bring
    /// up the IPSec NEVPNManager → "both start at the same time". This also made
    /// switching between IPSec profiles look broken: the injector overwrites the
    /// single NEVPNManager slot fine, but a still-armed WG manager raced it.
    /// Called from connectViaPTP / connectViaIKEv2 before arming the active one.
    private func deactivateOtherManagers(active: VpnProtocol, activeName: String) async {
        let mgrs = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        for m in mgrs {
            // Keep the active PTP manager (matched by name) armed; disable all
            // other PTP managers, and ALL PTP managers when IPSec is active.
            if active != .ipsec, m.localizedDescription == activeName { continue }
            let st = m.connection.status
            if m.isEnabled || m.isOnDemandEnabled || st == .connected || st == .connecting || st == .reasserting {
                // Disarm on-demand + disable AND SAVE *before* stopping. Stopping
                // an on-demand-armed tunnel makes iOS re-trigger it on the route
                // change our other tunnel causes — so it'd come right back up and
                // fight the VPN we're starting. Save first → no auto-restart.
                m.isOnDemandEnabled = false
                m.isEnabled = false
                try? await m.saveToPreferences()
                m.connection.stopVPNTunnel()
            }
        }
        // Disable the IPSec personal VPN when starting a PTP tunnel (the reverse
        // — disabling PTP when IPSec is active — is the loop above).
        if active != .ipsec {
            let ike = NEVPNManager.shared()
            try? await ike.loadFromPreferences()
            let st = ike.connection.status
            if ike.isEnabled || ike.isOnDemandEnabled || st == .connected || st == .connecting || st == .reasserting {
                ike.isOnDemandEnabled = false
                ike.isEnabled = false
                try? await ike.saveToPreferences()
                ike.connection.stopVPNTunnel()
            }
        }
    }

    /// Remove the OS-level VPN configuration(s) for a deleted connection so they
    /// don't orphan in iOS Settings ▸ VPN after the user deletes the connection
    /// in-app. PTP protocols (WG/AWG/OpenVPN) use a per-connection
    /// NETunnelProviderManager matched by `localizedDescription`; IPSec uses the
    /// shared NEVPNManager personal-VPN slot. Best-effort + idempotent.
    /// `otherIPSecConnectionsRemain` — true when at least one OTHER saved
    /// connection still has an IPSec config. All IPSec connections share the
    /// SINGLE NEVPNManager personal-VPN slot (matched only by name), so removing
    /// it on delete would wipe the slot another IPSec connection depends on the
    /// next time it connects. Only tear the IKEv2 slot down when no other IPSec
    /// connection is left AND the slot's name matches the deleted one.
    func removeOSConfigs(connectionName: String, otherIPSecConnectionsRemain: Bool = false) async {
        let mgrs = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        for m in mgrs where m.localizedDescription == connectionName {
            try? await m.removeFromPreferences()
        }
        if !otherIPSecConnectionsRemain {
            let ike = NEVPNManager.shared()
            try? await ike.loadFromPreferences()
            if ike.localizedDescription == connectionName {
                try? await ike.removeFromPreferences()
            }
        }
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

    @discardableResult
    private func connectViaPTP(connection: SavedConnection, config: ProtocolConfig) async throws -> NETunnelProviderManager {
        // Single active VPN: turn off the IPSec NEVPNManager + any other PTP
        // manager so iOS doesn't run two tunnels at once.
        await deactivateOtherManagers(active: config.protocol, activeName: connection.name)
        let mgrs = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        let mgr = mgrs.first { $0.localizedDescription == connection.name } ?? NETunnelProviderManager()
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = "com.privycs.vpn.tunnel"
        proto.serverAddress = config.serverAddress
        proto.providerConfiguration = TunnelProviderConfig.make(
            protocolRaw: config.protocol.rawValue,
            configContent: config.configContent,
            connectionId: connection.id,
            configId: config.id,
            dnsOverride: dnsOverride,
            killSwitch: killSwitch
        )
        mgr.protocolConfiguration = proto
        mgr.localizedDescription = connection.name
        mgr.isEnabled = true
        if onDemandEnabled && onDemandWouldConnect() {
            mgr.onDemandRules = onDemandRuleSet()
            mgr.isOnDemandEnabled = true
        } else {
            // Master toggle off, OR on but no rule that ever connects: never
            // arm a Disconnect-only ruleset — it would kill this manual connect.
            mgr.isOnDemandEnabled = false
        }
        try await mgr.saveToPreferences()
        try await mgr.loadFromPreferences()
        try await startTunnelRetrying(mgr)
        return mgr
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
        proto.providerConfiguration = TunnelProviderConfig.make(
            protocolRaw: config.protocol.rawValue,
            configContent: config.configContent,
            connectionId: connection.id,
            configId: config.id,
            dnsOverride: dnsOverride,
            killSwitch: killSwitch
        )
        mgr.protocolConfiguration = proto
        mgr.localizedDescription = connection.name
        mgr.isEnabled = true
        if onDemandWouldConnect() {
            mgr.onDemandRules = onDemandRuleSet()
            mgr.isOnDemandEnabled = true
        } else {
            // No actionable rule (only "Disconnect everywhere"): arming would
            // block all traffic via on-demand and never auto-connect — leave it
            // disabled so the user can still connect manually.
            mgr.isOnDemandEnabled = false
        }
        try await mgr.saveToPreferences()
        await reloadManagersAndRefresh()
        PrivycsLog.log("on-demand armed (persistent config) — \(connection.name), rules=\(rules.count), enabled=\(onDemandWouldConnect())")
    }

    @discardableResult
    private func connectViaIKEv2(connection: SavedConnection, config: ProtocolConfig) async throws -> NEVPNManager {
        // Parse the gateway's .sswan JSON profile into IKEv2 attributes.
        let profile = try SswanProfile.parse(config.configContent)

        // Reset the IPSec traffic baselines for the new session. buildIKEv2Status
        // re-captures a per-ipsec-interface baseline and sums growth (also re-armed
        // by a connectedDate change, so a switch that bypasses this path is covered).
        ipsecIfaceBase.removeAll()
        ipsecSessionDate = nil
        ipsecTrafficLogged = false
        ipsecServerEndpoint = config.serverAddress   // surfaced on the Connect screen

        // Single active VPN: turn off every PTP manager so a stale on-demand-
        // armed WireGuard doesn't start alongside this IPSec tunnel.
        await deactivateOtherManagers(active: .ipsec, activeName: connection.name)

        let mgr = NEVPNManager.shared()
        try await mgr.loadFromPreferences()

        let proto = NEVPNProtocolIKEv2()
        proto.serverAddress = profile.remote.addr
        proto.remoteIdentifier = profile.resolvedRemoteIdentifier
        if let localID = profile.local.id, !localID.isEmpty {
            proto.localIdentifier = localID
        }

        // Certificate auth via inline PKCS#12 (client cert + key + CA chain).
        // The device log (v1.1.3.11) confirms the CLIENT-cert method is NOT the
        // IPSec blocker: inline reaches IKE but the SA never establishes within
        // the watchdog → the failure is server-side IKE negotiation (server-cert
        // trust of the self-signed gateway CA, or a DH/proposal mismatch). The
        // earlier keychain identityReference attempt was a no-op (SecItemAdd
        // succeeded but returned no usable persistent ref → always fell back
        // here) so it's removed. Re-add a correct keychain path only if a system
        // (NEIKEv2) log shows client-auth is actually the problem.
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
        // Match the GATEWAY's documented IKE/ESP proposal exactly:
        // `aes256-sha256-modp2048` (AES-256-CBC + SHA256 + DH group 14), per the
        // Linux swanctl config + the Windows setup (NegotiateDH2048). The prior
        // values here forced AES256-GCM + DH group 19, which the gateway does
        // NOT offer → no common proposal → IKE_SA_INIT silently times out (the
        // clean ~12s "did not establish" with no handshake seen in the device
        // log v1.1.3.11). CBC needs an explicit integrity alg (GCM is AEAD).
        let ike = proto.ikeSecurityAssociationParameters
        ike.encryptionAlgorithm = .algorithmAES256
        ike.integrityAlgorithm = .SHA256
        ike.diffieHellmanGroup = .group14
        let esp = proto.childSecurityAssociationParameters
        esp.encryptionAlgorithm = .algorithmAES256
        esp.integrityAlgorithm = .SHA256
        esp.diffieHellmanGroup = .group14
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
        if onDemandEnabled && onDemandWouldConnect() {
            mgr.onDemandRules = onDemandRuleSet()
            mgr.isOnDemandEnabled = true
        } else {
            // Master toggle off, OR on but no rule that ever connects: never
            // arm a Disconnect-only ruleset — it would kill this manual connect.
            mgr.isOnDemandEnabled = false
        }
        try await mgr.saveToPreferences()
        try await mgr.loadFromPreferences()
        try await startTunnelRetrying(mgr)
        return mgr
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
        let ikeStatus = NEVPNManager.shared().connection.status
        let ikeLive = ikeStatus == .connected || ikeStatus == .connecting || ikeStatus == .reasserting
        // Prefer the IKEv2 personal-VPN status when IPSec is the active protocol
        // (its connection is the single NEVPNManager.shared() slot the injector
        // owns). Without this, a stale/connecting PTP manager MASKS a live IPSec
        // → the app thinks nothing is up → tears it down + fails over to
        // WireGuard (the reported "app doesn't recognise the personal VPN" bug).
        if activeProtocol == .ipsec, ikeLive {
            buildIKEv2Status()
        } else if let m = livePTP {
            buildPTPStatus(from: m)
        } else if ikeLive {
            buildIKEv2Status()
        } else {
            self.status = .disconnected
        }
        // Keep the throughput poller alive whenever the tunnel is UP — including
        // tunnels iOS brought up via on-demand AFTER a manual disconnect, which
        // never go through connect()/startPolling(). This observer fires on
        // NEVPNStatusDidChange, so a reconnect we didn't start (re)starts the
        // poller here; otherwise rx/tx froze until the app was killed+relaunched.
        // Stop it when down so we don't poll an idle tunnel. Cheap + leak-free
        // (reads cached managers + the App Group stats store; no preference
        // reload). The poller calls refreshStatus itself, but the pollTask==nil
        // guard prevents re-spawning each tick.
        if self.status.connected {
            if pollTask == nil { startPolling() }
        } else if pollTask != nil {
            pollTask?.cancel()
            pollTask = nil
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
        if age < 0 { return loc("just now") }
        if age < 2 { return loc("1 second ago") }
        if age < 60 { return loc("\(age) seconds ago") }
        if age < 120 { return loc("1 minute ago") }
        if age < 3600 { return loc("\(age / 60) minutes ago") }
        if age < 7200 { return loc("1 hour ago") }
        return loc("\(age / 3600) hours ago")
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

        // Traffic: IKEv2 has no NE byte API, so read the tunnel interface's
        // 64-bit kernel counters (sysctl NET_RT_IFLIST2 / if_data64). The IKEv2
        // Personal VPN uses `ipsec0` (not a utun); identify it as the interface
        // that appeared since the pre-connect snapshot, capture a baseline, then
        // report current-minus-baseline.
        var rx: Int64 = 0, tx: Int64 = 0
        if connected {
            // New IKE session (connectedDate advanced) → fresh tunnel; drop the
            // per-interface baselines so growth is measured from this session.
            // This is the path-independent re-arm for an IPSec→IPSec switch (which
            // doesn't always run connectViaIKEv2's reset).
            if let since = connection.connectedDate, since != ipsecSessionDate {
                ipsecSessionDate = since
                ipsecIfaceBase.removeAll()
                ipsecTrafficLogged = false
            }
            // Sum the GROWTH of every live ipsec* interface since the session
            // started. We don't assume which one carries the tunnel (a switch
            // moves ipsec0→ipsec1 with the old lingering): each gets a first-sight
            // baseline, so the dying interface contributes ~0 and the live one the
            // real session bytes.
            var diag: [String] = []
            for ifn in Self.utunNames().filter({ $0.hasPrefix("ipsec") }).sorted() {
                guard let c = Self.interfaceByteCounts(ifn) else { continue }
                let base = ipsecIfaceBase[ifn] ?? c
                if ipsecIfaceBase[ifn] == nil { ipsecIfaceBase[ifn] = c }
                rx += max(0, c.rx - base.rx)
                tx += max(0, c.tx - base.tx)
                diag.append("\(ifn):rx=\(c.rx),tx=\(c.tx)")
            }
            // One-time diagnostic: absolute bytes of every ipsec* interface so a
            // device log shows which one actually accounts traffic if rx/tx stay 0.
            if !ipsecTrafficLogged {
                ipsecTrafficLogged = true
                PrivycsLog.log("IPSec traffic: ifaces=[\(diag.joined(separator: " "))] -> session rx=\(rx) tx=\(tx)")
            }
        } else {
            ipsecIfaceBase.removeAll()
            ipsecSessionDate = nil
        }

        // Connect-screen detail rows: the assigned VPN IP read live off the
        // ipsec* interface, and the server endpoint captured at connect. (IKEv2
        // has no "last handshake" concept, so that row stays empty for IPSec.)
        let localAddr = connected ? Self.ipsecLocalAddress() : ""
        self.status = VpnStatus(
            connected: connected,
            connectionName: activeConnectionName,
            connectionID: activeConnectionID,
            activeProtocol: activeProtocol ?? .ipsec,
            uptime: uptime,
            rxBytes: rx,
            txBytes: tx,
            localAddress: localAddr,
            serverEndpoint: connected ? ipsecServerEndpoint : ""
        )
    }

    // MARK: - utun byte counters (IKEv2 traffic)

    /// Current VPN tunnel interface names. Apple's IKEv2 *Personal VPN*
    /// (NEVPNManager) brings up an `ipsec0` interface — NOT a `utun` (those are
    /// for packet-tunnel providers like WireGuard). A device log proved the
    /// connect added no new utun (`iface=none`), so we must watch `ipsec*` too;
    /// the snapshot-diff then finds `ipsec0` as the freshly-appeared interface.
    private static func utunNames() -> Set<String> {
        var names = Set<String>()
        var head: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&head) == 0, let start = head else { return names }
        defer { freeifaddrs(head) }
        var cur: UnsafeMutablePointer<ifaddrs>? = start
        while let p = cur {
            let n = String(cString: p.pointee.ifa_name)
            if n.hasPrefix("utun") || n.hasPrefix("ipsec") {
                names.insert(n)
            }
            cur = p.pointee.ifa_next
        }
        return names
    }

    /// Assigned inner IP(s) on the live `ipsec*` interface(s) — the IKEv2
    /// tunnel's VPN IP, for the Connect-screen "VPN IP" row (NEVPNConnection
    /// exposes no inner-address accessor). Skips link-local/loopback; strips the
    /// `%scope` suffix; comma-joins v4 + v6.
    private static func ipsecLocalAddress() -> String {
        var parts: [String] = []
        var head: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&head) == 0, let start = head else { return "" }
        defer { freeifaddrs(head) }
        var cur: UnsafeMutablePointer<ifaddrs>? = start
        while let p = cur {
            defer { cur = p.pointee.ifa_next }
            guard String(cString: p.pointee.ifa_name).hasPrefix("ipsec"),
                  let sa = p.pointee.ifa_addr else { continue }
            let fam = sa.pointee.sa_family
            guard fam == UInt8(AF_INET) || fam == UInt8(AF_INET6) else { continue }
            let salen = socklen_t(fam == UInt8(AF_INET)
                ? MemoryLayout<sockaddr_in>.size : MemoryLayout<sockaddr_in6>.size)
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            guard getnameinfo(sa, salen, &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0
            else { continue }
            let ip = String(cString: host).split(separator: "%").first.map(String.init) ?? ""
            if ip.isEmpty || ip == "::1" || ip.hasPrefix("127.") || ip.lowercased().hasPrefix("fe80") { continue }
            if !parts.contains(ip) { parts.append(ip) }
        }
        return parts.joined(separator: ", ")
    }

    /// 64-bit kernel rx/tx byte counters for an interface via
    /// sysctl(NET_RT_IFLIST2) → if_msghdr2.if_data64 (ifi_ibytes/ifi_obytes are
    /// u_int64_t). Avoids the 32-bit getifaddrs if_data wrap at 4 GB. nil if
    /// not found.
    private static func interfaceByteCounts(_ ifname: String) -> (rx: Int64, tx: Int64)? {
        var mib: [Int32] = [CTL_NET, AF_ROUTE, 0, 0, NET_RT_IFLIST2, 0]
        var len = 0
        guard sysctl(&mib, 6, nil, &len, nil, 0) == 0, len > 0 else { return nil }
        var buf = [UInt8](repeating: 0, count: len)
        let ok = buf.withUnsafeMutableBytes { sysctl(&mib, 6, $0.baseAddress, &len, nil, 0) == 0 }
        guard ok else { return nil }
        return buf.withUnsafeBytes { (raw: UnsafeRawBufferPointer) -> (Int64, Int64)? in
            guard let base = raw.baseAddress else { return nil }
            var off = 0
            while off + MemoryLayout<if_msghdr>.size <= len {
                let hdr = base.advanced(by: off).assumingMemoryBound(to: if_msghdr.self)
                let msglen = Int(hdr.pointee.ifm_msglen)
                if msglen <= 0 { break }
                // RTM_IFINFO2 == 0x12 (from <net/route.h>); the constant isn't
                // surfaced in the Swift Darwin module on this SDK, so use the
                // literal. if_msghdr2/if_data64 ARE available.
                if hdr.pointee.ifm_type == UInt8(0x12) {
                    let m2 = base.advanced(by: off).assumingMemoryBound(to: if_msghdr2.self)
                    var nameBuf = [CChar](repeating: 0, count: Int(IFNAMSIZ))
                    if if_indextoname(UInt32(m2.pointee.ifm_index), &nameBuf) != nil,
                       String(cString: nameBuf) == ifname {
                        let d = m2.pointee.ifm_data
                        return (Int64(bitPattern: d.ifi_ibytes), Int64(bitPattern: d.ifi_obytes))
                    }
                }
                off += msglen
            }
            return nil
        }
    }
}

enum VPNError: LocalizedError {
    case noConfig
    case tunnelDidNotEstablish(VpnProtocol)
    var errorDescription: String? {
        switch self {
        case .noConfig: return loc("No protocol config selected")
        case .tunnelDidNotEstablish(let p):
            return loc("\(p.displayName) tunnel did not establish")
        }
    }
}
