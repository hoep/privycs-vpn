import SwiftUI
import PrivycsCore
import WidgetKit

@main
struct PrivycsVPNApp: App {

    @StateObject private var appState = AppState()
    @Environment(\.scenePhase) private var scenePhase

    init() {
        // BGTaskScheduler handlers must be registered during launch.
        BackgroundRotation.register()
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(appState)
                // Design-system default typeface: Inter as the inherited
                // app font (Text/labels without an explicit .font pick it
                // up). Explicit per-view fonts use PrivycsFont.inter/.mono.
                .font(PrivycsFont.inter(17))
                .task {
                    await appState.bootstrap()
                }
                .preferredColorScheme(appState.colorScheme)
                .onChange(of: scenePhase) { phase in
                    appState.onScenePhase(phase == .active)
                }
        }
    }
}

/// Global app state — top-level container für die singletons
/// (Repositories, Manager) die alle Screens brauchen. ObservableObject
/// so SwiftUI's @EnvironmentObject das injection-Pattern macht.
@MainActor
final class AppState: ObservableObject {
    let connectionRepo = ConnectionRepository()
    let settingsRepo = SettingsRepository()
    let poolRepo = PoolRepository()
    let rulesRepo = NetworkRulesRepository()
    let poolHealth = PoolHealthStore()
    let entitlementRepo = EntitlementRepository()
    let networkMonitor = NetworkMonitor()
    let crashReporter = CrashReporter()
    let tunnelManager = VPNTunnelManager()
    /// Owns CLLocationManager — must be a strong, long-lived reference or the
    /// location-permission prompt is dismissed before the user can answer.
    /// Without location granted, iOS hands out NO Wi-Fi SSID, so SSID-based
    /// rules (and iOS on-demand ssidMatch) can never match.
    let ssidProvider = SSIDProvider()

    /// Smart Decision Engine (shadow, v1.0.9) — the same Go core (engine/ffi)
    /// as desktop/Android, via the gomobile Engine.xcframework. Observes the
    /// real connect/disconnect + tunnel-health and explains what it WOULD
    /// choose; drives nothing. The EngineDecisionsView polls its decisions.
    let engineShadow = EngineShadow()
    /// Connected-edge latch so the status stream's repeated emissions map to a
    /// single observeConnect()/observeDisconnect() per real transition.
    private var engineConnectedLatch = false
    /// Profile sync: reconcile the app's selection to the VPN actually up in
    /// iOS once at launch (one-shot, so live user switches aren't overridden).
    private var didReconcileActiveFromOS = false
    /// User's pre-VPN country (ISO alpha-2) from the IP→MMDB SelfIPDetector;
    /// "" until detected. Feeds the engine's network-aware reason AND upgrades
    /// Geo-Nearest from the locale proxy. See the userCountry computed property.
    private var detectedCountry = ""

    @Published var settings: AppSettings = .default
    @Published var connections: [SavedConnection] = []
    @Published var pools: [Pool] = []
    /// Resolved ISO country per connection-endpoint host (for flags). Filled
    /// in the background from the bundled IP→country DB; empty until resolved.
    @Published var endpointCountries: [String: String] = [:]
    @Published var rules: [NetworkRule] = []
    @Published var status: VpnStatus = .disconnected
    @Published var networkState: NetworkState = .none

    /// User-picked connect target. A bare connection `id`, or
    /// `"pool:<id>"` when a pool is selected. Empty = nothing chosen
    /// yet (defaults to the first connection).
    @Published var selectedTargetID: String = ""
    @Published var connecting: Bool = false
    @Published var connectError: String?

    // Active pool context (nil when a single connection is active).
    @Published var activePool: Pool?
    @Published var activePoolMember: PoolMember?
    /// UNIX epoch of the next scheduled rotation (0 = none).
    @Published var nextRotationAt: Int64 = 0
    private var rotationTimer: Task<Void, Never>?
    /// Live tunnel health (nil = inactive/hidden). Driven by the
    /// reachability monitor in TunnelHealthService.
    @Published var tunnelHealth: TunnelHealthPill.Health?
    var healthTask: Task<Void, Never>?
    /// True while the app is in the foreground. The health probe is
    /// foreground-only (iOS suspends the app in background, so probes there
    /// spuriously fail and falsely report "degraded"). See onScenePhase.
    @Published var appActive = true
    /// Manual pause: the instant the tunnel auto-resumes. nil = not paused,
    /// `.distantFuture` = paused until the user resumes. While paused, all
    /// rule-driven automation is frozen (Android ManualPauseSheet parity).
    @Published var pausedUntil: Date?
    private var pauseTimer: Task<Void, Never>?
    private let rotator = PoolRotator()
    private let rulesEngine = NetworkRulesEngine()
    /// Suppress rule-driven auto-connect after an explicit user
    /// disconnect (mirrors Android's 30s manual-disconnect cooldown).
    private var manualDisconnectUntil: Date?
    /// Last rule-driven target we acted on, to avoid re-firing the same
    /// connect every network tick.
    private var lastRuleTargetID: String?

    // Live throughput derived from successive status byte samples.
    @Published var rxSpeed: Double = 0       // bytes/sec
    @Published var txSpeed: Double = 0
    @Published var rxHistory: [Double] = []  // rolling window for sparkline
    @Published var txHistory: [Double] = []
    private var lastSampleRx: Int64 = 0
    private var lastSampleTx: Int64 = 0
    private var lastSampleAt: Date?
    private let historyWindow = 24

    var colorScheme: ColorScheme? {
        switch settings.theme {
        case "dark": return .dark
        case "light": return .light
        default: return nil  // system default
        }
    }

    /// Resolves the currently-picked connection (nil if a pool is
    /// selected or nothing imported yet). Falls back to the first
    /// connection when no explicit pick has been made.
    var selectedConnection: SavedConnection? {
        if selectedTargetID.hasPrefix("pool:") { return nil }
        if let c = connections.first(where: { $0.id == selectedTargetID }) { return c }
        return connections.first
    }

    var selectedPool: Pool? {
        if selectedTargetID.hasPrefix("pool:") {
            let id = String(selectedTargetID.dropFirst("pool:".count))
            return pools.first(where: { $0.id == id })
        }
        // No explicit pick AND no single connection to default to → default to
        // the first pool so a pool-only setup is connectable. Without this both
        // selectedConnection (→ connections.first = nil) AND selectedPool were
        // nil, so connectSelected() fell through both branches and did nothing:
        // no saveToPreferences → iOS never showed the VPN-permission prompt
        // ("verbindet nicht / fragt nicht nach Erlaubnis"). All iOS versions.
        return connections.isEmpty ? pools.first : nil
    }

    /// Display label for the active picker selection.
    var selectedLabel: String {
        if let p = selectedPool { return p.name }
        if let c = selectedConnection { return c.name }
        return loc("No connection")
    }

    /// Protocol whose brand logo the (idle) connect button should show —
    /// the one that would ACTUALLY be started for the current selection,
    /// not a stale `status.activeProtocol` (which lingered as IPSec and
    /// showed the strongSwan logo while disconnected). When connected we
    /// defer to the live `status.activeProtocol`; otherwise we resolve the
    /// selected pool's member protocol (active member, else first) or the
    /// connection's effective active config protocol (same resolution the
    /// connect path uses). nil ⇒ button falls back to its shield glyph.
    var displayProtocol: VpnProtocol? {
        if status.connected, let p = status.activeProtocol { return p }
        if let pool = selectedPool {
            let m = pool.members.first(where: { $0.id == pool.activeMemberID }) ?? pool.members.first
            return m?.protocol
        }
        if let conn = selectedConnection {
            return conn.resolvedActiveConfig(globalOrder: settings.protocolFailoverOrder)?.protocol
        }
        return nil
    }

    // MARK: - Connect orchestration (Session 1: connection path real;
    // pool-connect rotation lands in Session 3)

    func toggleConnection() async {
        if status.connected {
            await disconnect()
        } else {
            await connectSelected()
        }
    }

    func connectSelected() async {
        // Re-entrancy guard: a double-tap (or an overlapping rule-driven +
        // manual connect) must not launch two connect walks against the single
        // shared NEVPNManager slot at once. `connecting` is the in-flight latch.
        guard !connecting else { return }
        connectError = nil
        // A manual connect cancels any active pause.
        pausedUntil = nil; pauseTimer?.cancel(); pauseTimer = nil
        if let pool = selectedPool {
            await connectPool(pool)
        } else if let conn = selectedConnection {
            // A single connection is now active — clear any pool context so
            // the pool indicator card (rotate / next-rotation) disappears.
            activePool = nil; activePoolMember = nil; nextRotationAt = 0
            await poolRepo.setActivePoolID("")
            connecting = true
            defer { connecting = false }
            resetSpeedTracking()
            do { try await tunnelManager.connect(conn, onDemand: settings.networkRulesEnabled,
                                                 dnsOverride: resolvedDNS(for: conn),
                                                 failoverOrder: settings.protocolFailoverOrder,
                                                 killSwitch: settings.killSwitchEnabled,
                                                 rules: rules, engineOrder: engineOrder(for: conn)) }
            catch { connectError = error.localizedDescription }
        }
    }

    /// React to foreground/background transitions. The tunnel-health probe
    /// is paused in the background (where it would falsely fail) and restarted
    /// fresh on return to the foreground so a stale "degraded" doesn't linger.
    func onScenePhase(_ active: Bool) {
        appActive = active
        guard active else { return }
        startHealthMonitor()   // restart → failure counter resets
        // A widget protocol-switch pill records its request in the App Group and
        // opens the app (the widget extension can't reliably reconfigure+start a
        // packet-tunnel itself). Perform it here via the proven setActiveConfig path.
        Task { await consumePendingProtocolSwitch() }
        // A timed pause's in-app timer (Task.sleep) does NOT survive iOS
        // suspension/doze, so a pause that elapsed while backgrounded never
        // auto-resumed ("connection stale"). On return to the foreground,
        // resume immediately if it already elapsed, else re-arm the timer.
        if let until = pausedUntil {
            if until != .distantFuture && Date() >= until {
                Task { await resume() }
            } else {
                schedulePauseExpiry()
            }
        }
        Task { await syncOnDemand() }
    }

    /// Apply a protocol switch requested from the home-screen widget. The widget's
    /// SwitchProtocolIntent records `<connectionID>|<configID>` (non-secret) in the
    /// App Group and opens the app, because the widget extension can't reliably
    /// reconfigure + start a packet-tunnel ("configuration type is wrong"). We run
    /// it through the same proven setActiveConfig path the in-app pills use.
    func consumePendingProtocolSwitch() async {
        guard let d = UserDefaults(suiteName: "group.com.privycs.vpn"),
              let pending = d.string(forKey: "pendingProtocolSwitch"), !pending.isEmpty else { return }
        d.removeObject(forKey: "pendingProtocolSwitch")
        let parts = pending.split(separator: "|", maxSplits: 1).map(String.init)
        guard parts.count == 2, !parts[0].isEmpty, !parts[1].isEmpty else { return }
        PrivycsLog.log("app: applying pending widget protocol switch conn=\(parts[0]) cfg=\(parts[1])")
        await setActiveConfig(connectionID: parts[0], configID: parts[1])
    }

    /// Persistent on-demand (WireGuard-app model). When the auto-tunnel master
    /// toggle is on, the connection's saved manager is kept ARMED — on-demand
    /// enabled + faithfully-translated rules — at all times, so iOS (incl. the
    /// iOS Settings VPN toggle) respects the rules and the block-until-connect
    /// kill switch is armed where a connect rule applies, even when the app
    /// isn't running. On top of that we also START the tunnel right now if the
    /// engine says THIS connection belongs up on the CURRENT network (so it
    /// comes up without waiting for a network change; a never-started profile
    /// alone is inert on iOS). Master toggle off OR paused ⇒ disarm (Pause is
    /// the "fully off, traffic flows, no kill switch" escape hatch). Single
    /// (non-pool, non-IPSec) connections only — pools rotate and can't be
    /// pre-armed.
    func syncOnDemand() async {
        guard settings.networkRulesEnabled, !isPaused else {
            await tunnelManager.disarmOnDemand()
            return
        }
        guard selectedPool == nil, let conn = selectedConnection,
              (conn.resolvedActiveConfig(globalOrder: settings.protocolFailoverOrder)?.protocol ?? .wireguard) != .ipsec
        else { return }
        // What do the rules say for the CURRENT network? Keep the foreground
        // decision and the iOS on-demand armed-state CONSISTENT so they never
        // fight (that fight = the flapping).
        let result = rulesEngine.evaluate(rules: rules, state: networkState, masterEnabled: true)
        guard let matched = result.matchedRule else {
            // No rule matches the current net → arm the persistent on-demand
            // config WITHOUT starting. Its terminal Disconnect keeps the
            // current net off, while its Connect rules let iOS bring the tunnel
            // up on OTHER networks (incl. doze) and the Settings toggle keeps
            // respecting the rules.
            if !status.connected, !connecting {
                try? await tunnelManager.armOnDemand(
                    conn, dnsOverride: resolvedDNS(for: conn),
                    killSwitch: settings.killSwitchEnabled, rules: rules)
            }
            return
        }
        // Does the matched rule want THIS connection up on this network?
        let connectThis: Bool
        switch matched.action {
        case .connectActive: connectThis = true
        case .connection:    connectThis = (matched.targetId == conn.id)
        case .pool, .noVpn:  connectThis = false
        }
        if connectThis {
            // connectSelected also arms persistent on-demand.
            if !status.connected, !connecting { await connectSelected() }
        } else if matched.matchType == .ssidPattern || matched.matchType == .bssid {
            // INEXPRESSIBLE off-rule (glob SSID / BSSID): on-demand can't encode
            // it, so the armed profile's Connect rules would fight us here →
            // disarm so iOS won't auto-connect (no flapping). Foreground-only
            // for this net (background can't honor glob/BSSID anyway).
            await tunnelManager.disarmOnDemand()
        } else if !status.connected, !connecting {
            // EXPRESSIBLE off-rule (noVpn ssidExact/networkType) or pool/other-
            // target: keep on-demand ARMED. onDemandRuleSet encodes an explicit
            // Disconnect for this net (priority-sorted ahead of any Connect), so
            // iOS keeps it off here — no flap — while background/doze still
            // works on the nets where a Connect rule applies.
            try? await tunnelManager.armOnDemand(
                conn, dnsOverride: resolvedDNS(for: conn),
                killSwitch: settings.killSwitchEnabled, rules: rules)
        }
    }

    /// Pick a target from the Connect-screen dropdown. Always allowed —
    /// even while connected: switch live by tearing down the old tunnel and
    /// connecting the newly chosen connection/pool.
    func selectTarget(_ id: String) async {
        let wasConnected = status.connected
        selectedTargetID = id
        pushWidgetSnapshot()
        // Don't kick off a live switch while a connect walk is already in flight
        // — that would tear down a tunnel mid-establish and race the shared
        // NEVPNManager slot. The new selection is recorded above; the user can
        // re-tap once the in-flight attempt settles.
        guard wasConnected, !connecting else { return }
        await teardownTunnel(armManualCooldown: false)
        await connectSelected()
    }

    func disconnect() async {
        connecting = true
        defer { connecting = false }
        // A manual disconnect cancels any active pause.
        pausedUntil = nil; pauseTimer?.cancel(); pauseTimer = nil
        // WG model: stop the current session but KEEP on-demand armed (the
        // teardown does NOT disarm) — iOS reconnects on its own schedule where
        // a connect rule applies, and the Settings toggle keeps respecting the
        // rules. We do NOT proactively reconnect here (that would fight the
        // user's tap on an always-on network). To stay off everywhere → Pause.
        await teardownTunnel(armManualCooldown: true)
    }

    // MARK: - Manual pause / resume (Android ManualPauseSheet parity)

    /// True while a manual pause is in effect.
    var isPaused: Bool {
        guard let until = pausedUntil else { return false }
        return Date() < until
    }

    /// Pause the VPN — the "fully off" escape hatch. Tears the tunnel down AND
    /// disarms on-demand, so iOS does NOT auto-reconnect and — crucially — the
    /// block-until-connect kill switch does NOT block: traffic flows normally
    /// while paused. Frozen until `seconds` elapse (nil = until the user
    /// resumes). syncOnDemand also disarms while `isPaused`, so a stray network
    /// event can't re-arm it mid-pause.
    func pause(seconds: TimeInterval?) async {
        pausedUntil = seconds.map { Date().addingTimeInterval($0) } ?? .distantFuture
        schedulePauseExpiry()
        connecting = true
        defer { connecting = false }
        await teardownTunnel(armManualCooldown: false)
        await tunnelManager.disarmOnDemand()   // no kill-switch block while paused
    }

    /// Resume from a pause by reconnecting the current selection.
    func resume() async {
        pausedUntil = nil
        pauseTimer?.cancel(); pauseTimer = nil
        await connectSelected()
    }

    /// Auto-resume timer for a timed pause (no-op for an indefinite pause).
    private func schedulePauseExpiry() {
        pauseTimer?.cancel()
        guard let until = pausedUntil, until != .distantFuture else { return }
        let delay = until.timeIntervalSinceNow
        guard delay > 0 else { Task { await resume() }; return }
        pauseTimer = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(delay * 1_000_000_000))
            guard let self, !Task.isCancelled else { return }
            await self.resume()
        }
    }

    /// Shared tunnel teardown (manual disconnect + pause). Does NOT touch
    /// pause state — callers own that.
    private func teardownTunnel(armManualCooldown: Bool) async {
        if armManualCooldown { manualDisconnectUntil = Date().addingTimeInterval(30) }
        lastRuleTargetID = nil
        rotationTimer?.cancel(); rotationTimer = nil
        BackgroundRotation.cancel()
        await tunnelManager.stopTunnel()
        await poolRepo.setActivePoolID("")
        activePool = nil
        activePoolMember = nil
        nextRotationAt = 0
        resetSpeedTracking()
    }

    // MARK: - Pool connect + rotation (Session 3)

    /// Detected user country for Geo-Nearest + the engine's network-aware
    /// reason. Prefers the real IP→MMDB result (detectedCountry); falls back to
    /// the device locale region when offline / not yet probed.
    private var userCountry: String {
        if !detectedCountry.isEmpty { return detectedCountry }
        if #available(iOS 16, *) { return Locale.current.region?.identifier ?? "" }
        return Locale.current.regionCode ?? ""
    }

    /// "wifi"/"cellular"/"ethernet"/"" — the engine's interface token for roaming.
    var engineIface: String {
        switch networkState.networkType {
        case .wifi: return "wifi"
        case .mobile: return "cellular"
        case .ethernet: return "ethernet"
        default: return ""
        }
    }

    /// The engine's ranked protocol order for a connection (context + roaming +
    /// adaptive stats) when Automatic protocol selection is on, else nil (manual
    /// path). Passed to tunnelManager.connect.
    private func engineOrder(for conn: SavedConnection) -> [VpnProtocol]? {
        guard UserDefaults.standard.bool(forKey: "auto_protocol_selection") else { return nil }
        let avail = Array(Set(conn.protocols.map { $0.protocol }))
        let order = engineShadow.selectOrder(available: avail, country: userCountry, iface: engineIface)
        return order.isEmpty ? nil : order
    }

    func connectPool(_ pool: Pool) async {
        connecting = true
        defer { connecting = false }

        // Resilient connect (Android PoolConnector parity): up to 3 attempts,
        // excluding members that recently failed (unreachable TTL) and ones
        // tried this round; verify the tunnel actually passes traffic before
        // declaring success; mark dead members unreachable and move on.
        let unreachable = await poolHealth.unreachableMembers(pool: pool.id)
        var tried = Set<String>()
        var lastError: String?

        for _ in 0..<3 {
            guard let (member, updated) = rotator.pick(
                from: pool, userCountry: userCountry,
                excludingMemberIDs: unreachable.union(tried)
            ) else { break }
            tried.insert(member.id)
            resetSpeedTracking()
            try? await poolRepo.save(updated)
            await poolRepo.setActivePoolID(pool.id)
            activePool = updated
            activePoolMember = member
            nextRotationAt = updated.rotation?.nextRotationAt ?? 0

            do {
                let synth = synthConnection(for: member, pool: updated)
                try await tunnelManager.connect(synth, onDemand: settings.networkRulesEnabled,
                                                dnsOverride: resolvedDNS(for: synth), killSwitch: settings.killSwitchEnabled, rules: rules)
            } catch {
                lastError = error.localizedDescription
                await poolHealth.markUnreachable(pool: pool.id, member: member.id)
                continue
            }

            // Post-up health probe — WG/AWG expose rx counters via the App
            // Group snapshot; if no traffic arrives within the window the
            // member is dead-on-arrival, mark it and try the next.
            if member.protocol == .wireguard || member.protocol == .amneziawg {
                try? await Task.sleep(nanoseconds: 5_000_000_000)
                let snap = TunnelStatsStore.read()
                if snap?.connected != true || (snap?.rxBytes ?? 0) == 0 {
                    await poolHealth.markUnreachable(pool: pool.id, member: member.id)
                    lastError = loc("\(member.name) did not pass traffic")
                    continue
                }
            }

            scheduleRotationIfNeeded(updated)
            connectError = nil
            return
        }

        // Every candidate failed. If the device clearly has internet, the
        // marks are likely stale (provider rotated IPs) — clear them so the
        // next attempt starts fresh instead of a permanently-dead pool.
        if await AppState.reachable(host: "1.1.1.1", timeout: 4) {
            await poolHealth.clear(pool: pool.id)
        }
        connectError = lastError ?? loc("Pool has no reachable members")
    }

    /// 3-tier DNS override: the connection's own override (pool members
    /// carry the pool's), else the global setting. Mirrors Android's
    /// resolveDnsOverrideServers precedence (pool→connection→global).
    private func resolvedDNS(for conn: SavedConnection) -> String {
        conn.dnsOverride.isEmpty ? settings.dnsOverride : conn.dnsOverride
    }

    /// Build a transient SavedConnection wrapping one pool member so we
    /// can reuse the standard connect path. ID carries the pool prefix
    /// so status/UI can tell it's pool-driven.
    private func synthConnection(for member: PoolMember, pool: Pool) -> SavedConnection {
        let cfg = ProtocolConfig(
            id: member.id,
            protocol: member.protocol,
            filename: member.name,
            configContent: member.configContent,
            serverAddress: member.serverAddress
        )
        return SavedConnection(
            id: "pool:\(pool.id)",
            name: pool.name,
            protocols: [cfg],
            activeConfigID: member.id,
            dnsOverride: pool.dnsOverride
        )
    }

    /// Rotation scheduler — a foreground timer fires at nextRotationAt while
    /// the app is open, and a BGTaskScheduler request covers the backgrounded
    /// case (best-effort; iOS runs it opportunistically, not precisely).
    private func scheduleRotationIfNeeded(_ pool: Pool) {
        rotationTimer?.cancel()
        guard let rot = pool.rotation, rot.intervalSeconds > 0 else {
            BackgroundRotation.cancel()
            return
        }
        if nextRotationAt > 0 {
            BackgroundRotation.schedule(at: Date(timeIntervalSince1970: TimeInterval(nextRotationAt)))
        }
        rotationTimer = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000) // 5s tick
                guard let self else { return }
                let due = await MainActor.run { () -> Bool in
                    guard self.status.connected, self.activePool != nil else { return false }
                    let now = Int64(Date().timeIntervalSince1970)
                    return self.nextRotationAt > 0 && now >= self.nextRotationAt
                }
                if due { await self.rotatePool() }
            }
        }
    }

    /// Rotate to the next member of the active pool (manual or timer).
    func rotatePool() async {
        guard let pool = activePool else { return }
        let unreachable = await poolHealth.unreachableMembers(pool: pool.id)
        guard let (member, updated) = rotator.pick(
            from: pool, userCountry: userCountry, excludingMemberIDs: unreachable
        ) else { return }
        try? await poolRepo.save(updated)
        activePool = updated
        activePoolMember = member
        nextRotationAt = updated.rotation?.nextRotationAt ?? 0
        resetSpeedTracking()
        let synth = synthConnection(for: member, pool: updated)
        try? await tunnelManager.connect(synth, onDemand: settings.networkRulesEnabled, dnsOverride: resolvedDNS(for: synth), killSwitch: settings.killSwitchEnabled, rules: rules)
    }

    // MARK: - Import + Gateway (Session 5)

    /// Import a raw config (file / QR / gateway). `intoConnectionID` nil
    /// = new connection; non-nil = add this protocol to that existing
    /// connection (Android addOrUpdate semantics).
    func importConnection(name: String, filename: String, content: String, intoConnectionID: String? = nil, configID: String? = nil) async {
        let proto = ConfigImport.detectProtocol(filename: filename, content: content)
        let cfg = ProtocolConfig(
            // Gateway imports pass a STABLE id ("gw-<proto>-<id>", like Android)
            // so a re-download matches+updates the existing config instead of
            // creating a duplicate; file/QR imports get a fresh UUID.
            id: configID ?? UUID().uuidString,
            protocol: proto,
            filename: filename,
            configContent: content,
            serverAddress: ConfigImport.extractServerAddress(content, proto)
        )
        _ = try? await connectionRepo.addOrUpdate(connectionID: intoConnectionID, name: name, config: cfg)
        connections = (try? await connectionRepo.loadAll()) ?? connections
        await refreshEndpointCountries()
    }

    /// Resolve the ISO country for every connection-endpoint host (IP→country
    /// via the bundled DB, DNS-resolving hostnames first) and cache it for the
    /// flag badges. Mirrors the pool-member geo enrichment, but for single
    /// connections (whose ProtocolConfig has no stored country field).
    func refreshEndpointCountries() async {
        guard let mmdb = MmdbCountryResolver.shared else { return }
        var hosts = Set<String>()
        for c in connections {
            for p in c.protocols {
                let h = PoolImporter.endpointHost(p.serverAddress)
                if !h.isEmpty, endpointCountries[h] == nil { hosts.insert(h) }
            }
        }
        for h in hosts {
            if let ip = await PoolImporter.firstIP(h), let cc = mmdb.country(forIP: ip) {
                endpointCountries[h] = cc
            }
        }
    }

    /// Flag emoji for a connection-endpoint host (empty until resolved).
    func endpointFlag(_ serverAddress: String) -> String {
        let cc = endpointCountries[PoolImporter.endpointHost(serverAddress)] ?? ""
        return PoolHostnameLabels.flagEmoji(cc)
    }

    /// Switch the active protocol config of a connection (per-protocol
    /// connect, like Android's ProtocolBadges switchConfig).
    func setActiveConfig(connectionID: String, configID: String) async {
        try? await connectionRepo.setActiveConfig(connectionID: connectionID, configID: configID)
        connections = (try? await connectionRepo.loadAll()) ?? connections
        // Android parity: switching the active protocol of the CURRENTLY
        // CONNECTED connection reconnects live with the new protocol — tap a
        // pill → tear down the old bridge, bring up the new one. Without this
        // the pill only flipped a stored flag and the tunnel kept running the
        // old protocol ("pill switch does nothing"). A protocol switch can
        // cross bridge types (WG/AWG/OVPN PTP ↔ IPSec NEVPNManager), so we do
        // a clean disconnect before reconnecting rather than reconfigure live.
        guard status.connected, status.connectionID == connectionID,
              let conn = connections.first(where: { $0.id == connectionID }) else { return }
        connecting = true
        defer { connecting = false }
        await tunnelManager.stopTunnel()
        resetSpeedTracking()
        do { try await tunnelManager.connect(conn, onDemand: settings.networkRulesEnabled,
                                             dnsOverride: resolvedDNS(for: conn),
                                             failoverOrder: settings.protocolFailoverOrder,
                                             killSwitch: settings.killSwitchEnabled,
                                             rules: rules) }
        catch { connectError = error.localizedDescription }
    }

    /// Remove one protocol config from a connection.
    func removeConfig(connectionID: String, configID: String) async {
        let name = connections.first(where: { $0.id == connectionID })?.name
        try? await connectionRepo.removeConfig(connectionID: connectionID, configID: configID)
        connections = (try? await connectionRepo.loadAll()) ?? connections
        // removeConfig deletes the whole connection when its last config goes.
        // Without this, the OS VPN profile (NETunnelProviderManager / NEVPNManager)
        // orphans in iOS Settings ▸ VPN — same cleanup the list-delete path does.
        if let name, !connections.contains(where: { $0.id == connectionID }) {
            // The IKEv2 personal-VPN slot is shared by ALL IPSec connections
            // (matched only by name). Don't remove it if another saved IPSec
            // connection still relies on it.
            let otherIPSecRemain = connections.contains { c in
                c.protocols.contains { $0.protocol == .ipsec }
            }
            await tunnelManager.removeOSConfigs(connectionName: name, otherIPSecConnectionsRemain: otherIPSecRemain)
        }
    }

    /// Persist a gateway enrollment (URL + API key) from a QR scan.
    func applyGatewayEnrollment(url: URL, apiKey: String) async {
        var s = settings
        s.gatewayURL = url.absoluteString
        s.apiKey = apiKey
        try? await settingsRepo.save(s)
        settings = s
    }

    /// Gateway client built from the current settings, or nil when the
    /// gateway isn't configured.
    var gatewayClient: GatewayAPIClient? {
        guard !settings.gatewayURL.isEmpty, !settings.apiKey.isEmpty,
              let url = URL(string: settings.gatewayURL) else { return nil }
        return GatewayAPIClient(gatewayURL: url, apiKey: settings.apiKey)
    }

    // MARK: - Network-rules executor (Session 4)

    /// Evaluate the rules against the current network and ACT — this is
    /// what makes the engine output live (was previously computed for
    /// display only). Honors the master toggle + manual-disconnect
    /// cooldown, and de-dupes so the same connect doesn't re-fire on
    /// every network tick.
    func evaluateAndApplyRules() async {
        // Frozen while manually paused — no rule-driven connect/disconnect.
        if isPaused { return }
        let result = rulesEngine.evaluate(
            rules: rules,
            state: networkState,
            masterEnabled: settings.networkRulesEnabled
        )
        guard let rule = result.matchedRule else { return }
        switch rule.action {
        case .noVpn:
            // Trusted network → no VPN: tear the active tunnel down NOW.
            // (This was briefly DEFERRED to iOS-on-demand to avoid a foreground↔
            // iOS flapping fight — but that fight came from a rule-ordering bug
            // [priority-sort vs array order] and a flapping NetworkMonitor, both
            // since fixed. iOS' own Disconnect rule is now correctly ordered
            // ahead of any Connect, so foreground + iOS agree and there's no
            // flap. Deferring meant toggling/adding a no-VPN rule did NOT
            // disconnect the live connection — the reported bug.)
            rotationTimer?.cancel(); rotationTimer = nil
            // INEXPRESSIBLE off-rules (glob SSID / BSSID) can't be a
            // NEOnDemandRule, so disarm so iOS won't auto-reconnect. EXPRESSIBLE
            // ones (ssidExact / networkType / any) stay armed — iOS' Disconnect
            // rule keeps them off here and background still works on other nets.
            if rule.matchType == .ssidPattern || rule.matchType == .bssid {
                await tunnelManager.disarmOnDemand()
            }
            if status.connected {
                await tunnelManager.stopTunnel()
                await poolRepo.setActivePoolID("")
                activePool = nil; activePoolMember = nil; nextRotationAt = 0
                resetSpeedTracking()
            }
            lastRuleTargetID = "disconnect"

        case .connection:
            let id = rule.targetId
            await ruleConnect(targetID: id) {
                guard let conn = self.connections.first(where: { $0.id == id }) else { return }
                self.selectedTargetID = id
                self.connecting = true
                defer { self.connecting = false }
                self.resetSpeedTracking()
                try? await self.tunnelManager.connect(conn, onDemand: self.settings.networkRulesEnabled,
                                                      dnsOverride: self.resolvedDNS(for: conn),
                                                      failoverOrder: self.settings.protocolFailoverOrder,
                                                      killSwitch: self.settings.killSwitchEnabled,
                                                      rules: self.rules, engineOrder: self.engineOrder(for: conn))
            }

        case .pool:
            let id = rule.targetId
            await ruleConnect(targetID: "pool:\(id)") {
                guard let pool = self.pools.first(where: { $0.id == id }) else { return }
                self.selectedTargetID = "pool:\(id)"
                await self.connectPool(pool)
            }

        case .connectActive:
            // Connect whatever the user currently has selected as active
            // (not a pinned target) — Android CONNECT_ACTIVE semantics.
            let key = selectedTargetID.isEmpty ? "active" : selectedTargetID
            await ruleConnect(targetID: key) {
                await self.connectSelected()
            }
        }
    }

    /// Call after ANY rule mutation (add / edit / toggle / reorder / delete):
    /// (1) apply the new rule set to the CURRENT network right away — so e.g.
    /// re-enabling a "this Wi-Fi → no VPN" rule disconnects the live tunnel, and
    /// deleting/reordering takes effect immediately instead of only on the next
    /// network change; (2) re-arm the persistent iOS on-demand profile so the
    /// background uses the updated rules too. Previously a rule toggle only ran
    /// the foreground engine (and move/delete ran nothing), and the on-demand
    /// profile kept the stale rules.
    func onRulesChanged() async {
        await evaluateAndApplyRules()
        await syncOnDemand()
    }

    /// Shared guard for rule-driven connects: respect manual cooldown +
    /// don't re-fire the same target that's already active.
    private func ruleConnect(targetID: String, _ body: () async -> Void) async {
        if let until = manualDisconnectUntil, Date() < until { return }
        if status.connected && status.connectionID == targetID { return }
        if lastRuleTargetID == targetID && status.connected { return }
        lastRuleTargetID = targetID
        await body()
    }

    private func resetSpeedTracking() {
        rxSpeed = 0; txSpeed = 0
        rxHistory = []; txHistory = []
        lastSampleRx = 0; lastSampleTx = 0
        lastSampleAt = nil
    }

    /// Feeds a fresh status sample into the throughput tracker. Called
    /// from the status observe loop. Derives bytes/sec from the delta
    /// against the previous sample and appends to the sparkline window.
    private func ingestSpeedSample(_ s: VpnStatus) {
        guard s.connected else { resetSpeedTracking(); return }
        let now = Date()
        if let last = lastSampleAt {
            let dt = now.timeIntervalSince(last)
            if dt > 0.1 {
                let dRx = max(0, Double(s.rxBytes - lastSampleRx)) / dt
                let dTx = max(0, Double(s.txBytes - lastSampleTx)) / dt
                rxSpeed = dRx
                txSpeed = dTx
                rxHistory = Array((rxHistory + [dRx]).suffix(historyWindow))
                txHistory = Array((txHistory + [dTx]).suffix(historyWindow))
            }
        }
        lastSampleRx = s.rxBytes
        lastSampleTx = s.txBytes
        lastSampleAt = now
    }

    private var lastWidgetReloadKey = ""

    /// Publish the rich home-screen-widget snapshot to the shared App Group
    /// and nudge WidgetKit. Writes on every status tick (a cheap UserDefaults
    /// encode) so a system-scheduled reload always finds fresh traffic, but
    /// only FORCES a reload on a real transition (connect / disconnect /
    /// pause / selection / protocol change) to respect WidgetKit's tight
    /// reload budget. The widget merges this with the live `TunnelStatsSnapshot`
    /// the tunnel writes, so it stays correct even when this app isn't running.
    func pushWidgetSnapshot() {
        let conn = selectedConnection
        let pool = selectedPool
        let dns = conn.map { resolvedDNS(for: $0) } ?? ""
        var seen = Set<VpnProtocol>()
        var available: [String] = []
        var targets: [WidgetSwitchTarget] = []
        for cfg in (conn?.protocols ?? []) where seen.insert(cfg.protocol).inserted {
            available.append(cfg.protocol.rawValue)
            // Only emit packet-tunnel protocols the widget can reconfigure
            // in-place (WG/AWG/OpenVPN); IPSec is handled by opening the app.
            if TunnelProviderConfig.isInPlaceSwitchable(cfg.protocol.rawValue) {
                // SECURITY: do NOT put the raw VPN config (WG/OpenVPN PrivateKeys)
                // into the App Group UserDefaults snapshot — UserDefaults is
                // unencrypted and recoverable from device backups. The widget's
                // protocol-switch intent re-reads the config from the Keychain
                // (App Group, ThisDeviceOnly, not backup-recoverable) at switch
                // time using snapshot.connectionId + this target's configId.
                targets.append(WidgetSwitchTarget(
                    protocolRaw: cfg.protocol.rawValue,
                    configId: cfg.id,
                    configContent: "",
                    serverAddress: cfg.serverAddress,
                    dnsOverride: dns
                ))
            }
        }
        let cc = status.activeMemberCountry.isEmpty ? status.serverCountryCode : status.activeMemberCountry
        let nowEpoch = Int64(Date().timeIntervalSince1970)
        let snap = WidgetSnapshot(
            connected: status.connected,
            paused: pausedUntil != nil,
            protocolRaw: status.activeProtocol?.rawValue ?? conn?.protocols.first?.protocol.rawValue ?? "",
            availableProtocols: pool == nil ? available : [],
            isPool: pool != nil,
            connectionName: conn?.name ?? status.connectionName,
            poolName: pool?.name ?? "",
            memberName: status.activeMemberName,
            countryCode: cc,
            serverEndpoint: status.serverEndpoint,
            localAddress: status.localAddress,
            rxBytes: status.rxBytes,
            txBytes: status.txBytes,
            rxSpeed: Int64(rxSpeed),
            txSpeed: Int64(txSpeed),
            rxHistory: rxHistory,
            txHistory: txHistory,
            connectedAtEpoch: status.connected ? nowEpoch - status.uptime : 0,
            updatedAtEpoch: nowEpoch,
            connectionId: conn?.id ?? "",
            killSwitch: settings.killSwitchEnabled,
            switchTargets: pool == nil ? targets : []
        )
        WidgetSnapshotStore.write(snap)
        let key = "\(status.connected)|\(pausedUntil != nil)|\(selectedTargetID)|\(snap.protocolRaw)"
        if key != lastWidgetReloadKey {
            lastWidgetReloadKey = key
            WidgetCenter.shared.reloadAllTimelines()
        }
    }

    func bootstrap() async {
        await networkMonitor.start()
        startHealthMonitor()
        // Background pool rotation handler (best-effort; foreground timer is
        // the primary path). Captures self weakly; runs on the OS task queue.
        BackgroundRotation.onRotate = { [weak self] in await self?.rotatePool() }

        // Initial load
        if let s = try? await settingsRepo.current() {
            self.settings = s
            // Keep the in-app language override in sync with the persisted
            // setting (first launch after upgrade may have it only here).
            if !s.appLanguage.isEmpty { LanguageManager.shared.set(s.appLanguage) }
        }
        // Seed the shadow engine's candidate set from the failover order.
        engineShadow.ensure(order: self.settings.protocolFailoverOrder)
        // Feed runtime-failover failures into the engine's adaptive (P4) stats.
        // Success is recorded by the status loop below, so this fires only for
        // protocols that failed to establish during a failover walk.
        tunnelManager.onConnectFailure = { [weak self] proto in
            guard let self else { return }
            self.engineShadow.recordOutcome(proto, success: false, iface: self.engineIface)
        }
        // Detect the user's pre-VPN country for the engine's network-aware reason.
        Task { self.detectedCountry = await SelfIPDetector.shared.country() }
        await crashReporter.start(
            optedIn: settings.crashReportsEnabled,
            appVersion: PrivycsCoreInfo.version
        )
        self.connections = (try? await connectionRepo.loadAll()) ?? []
        self.pools = (try? await poolRepo.loadAll()) ?? []
        self.rules = (try? await rulesRepo.loadAll()) ?? []
        // Resolve endpoint country flags in the background (DNS + IP→country).
        Task { await refreshEndpointCountries() }

        // If auto-tunnel rules are on, ask for location permission — iOS gives
        // out the Wi-Fi SSID only once location is granted, and without the
        // SSID neither the app nor iOS on-demand can match SSID-based rules.
        if settings.networkRulesEnabled {
            ssidProvider.requestPermissionIfNeeded()
        }

        // Restore active-pool selection across app restarts.
        let activeID = await poolRepo.activePoolID()
        if !activeID.isEmpty, let p = pools.first(where: { $0.id == activeID }) {
            self.activePool = p
            self.selectedTargetID = "pool:\(p.id)"
            self.activePoolMember = p.members.first(where: { $0.id == p.activeMemberID })
            self.nextRotationAt = p.rotation?.nextRotationAt ?? 0
        }

        // Arm persistent background on-demand if the master toggle is on
        // (so it works in doze even before a manual connect).
        await syncOnDemand()

        // Observe transitions
        Task {
            for await s in await settingsRepo.observe() {
                self.settings = s
                await crashReporter.start(
                    optedIn: s.crashReportsEnabled,
                    appVersion: PrivycsCoreInfo.version
                )
                // Keep the engine's candidate set in sync with the order.
                engineShadow.ensure(order: s.protocolFailoverOrder)
                // Master-toggle / rule changes re-arm or disarm on-demand.
                await syncOnDemand()
            }
        }
        Task {
            for await ns in await networkMonitor.observe() {
                self.networkState = ns
                // Network changed → the user may be in a different country now.
                await SelfIPDetector.shared.invalidate()
                Task { self.detectedCountry = await SelfIPDetector.shared.country() }
                await self.evaluateAndApplyRules()
            }
        }
        Task {
            for await st in await tunnelManager.observeStatus() {
                self.status = st
                self.ingestSpeedSample(st)
                self.pushWidgetSnapshot()
                // Profile sync (Settings → App): on first launch, reflect the
                // VPN that's ACTUALLY up in iOS so the app's selected/active
                // connection matches reality (e.g. one brought up by iOS
                // on-demand or a prior session). One-shot — never fight a live
                // user switch. Pool restore is handled separately in bootstrap.
                if !didReconcileActiveFromOS, st.connected, !st.connectionID.isEmpty {
                    didReconcileActiveFromOS = true
                    if !selectedTargetID.hasPrefix("pool:"),
                       selectedTargetID != st.connectionID,
                       connections.contains(where: { $0.id == st.connectionID }) {
                        selectedTargetID = st.connectionID
                        pushWidgetSnapshot()
                    }
                }
                // Shadow engine: map the status stream's edge to one observe.
                if st.connected && !engineConnectedLatch {
                    let awg = connections.first(where: { $0.id == st.connectionID })?
                        .protocols.contains { $0.protocol == .amneziawg } ?? false
                    engineShadow.observeConnect(st.activeProtocol?.rawValue ?? "", country: userCountry, awgAvailable: awg)
                    // Adaptive engine stats (P4): connected on this network.
                    engineShadow.recordOutcome(st.activeProtocol, success: true, iface: engineIface)
                } else if !st.connected && engineConnectedLatch {
                    engineShadow.observeDisconnect()
                }
                engineConnectedLatch = st.connected
            }
        }

        // Seed the widget once at launch so a freshly added widget shows the
        // restored selection/state before the first status tick arrives.
        pushWidgetSnapshot()
    }
}
