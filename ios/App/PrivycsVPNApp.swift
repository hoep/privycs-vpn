import SwiftUI
import PrivycsCore

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
                .task {
                    await appState.bootstrap()
                }
                .preferredColorScheme(appState.colorScheme)
                .onChange(of: scenePhase) { _, phase in
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
    /// Mode A: once the user manually disconnects, persistent on-demand stays
    /// disarmed until they manually connect again (so it can't auto-reconnect).
    private var userDisconnectedManually = false
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
        guard selectedTargetID.hasPrefix("pool:") else { return nil }
        let id = String(selectedTargetID.dropFirst("pool:".count))
        return pools.first(where: { $0.id == id })
    }

    /// Display label for the active picker selection.
    var selectedLabel: String {
        if let p = selectedPool { return p.name }
        if let c = selectedConnection { return c.name }
        return "No connection"
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
        connectError = nil
        // A manual connect cancels any active pause + re-arms on-demand (A).
        pausedUntil = nil; pauseTimer?.cancel(); pauseTimer = nil
        userDisconnectedManually = false
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
                                                 rules: rules) }
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

    /// Mode-A persistent on-demand (WireGuard-app model): when the auto-tunnel
    /// master toggle is on and the rules say THIS connection should be up on
    /// the current network, START it once with on-demand enabled. iOS then
    /// persists `isOnDemandEnabled=true` on the saved manager and keeps
    /// connecting/disconnecting per the (faithfully-translated) rules — and
    /// enforces the block-until-connect kill switch — through suspension/doze/
    /// app-death. (A never-started "armed" profile is inert on iOS, which is
    /// why the previous arm-without-start approach failed.)
    ///
    /// The start is GATED on the engine verdict for the current network so we
    /// don't blindly connect on networks the rules don't cover (e.g. an
    /// allowlist "connect only on SSID X"): only `.connectActive` or a
    /// `.connection` rule targeting the selected connection triggers a start.
    /// `.noVpn`, `.pool`, and no-match leave it alone — but the saved on-demand
    /// profile still carries the faithful rules, so iOS connects later when a
    /// connect-rule network appears. Disarms when the master toggle is off.
    /// Single (non-pool, non-IPSec) connections only.
    func syncOnDemand() async {
        guard settings.networkRulesEnabled else {
            await tunnelManager.disarmOnDemand()
            return
        }
        guard !userDisconnectedManually, !isPaused, !status.connected, !connecting,
              selectedPool == nil, let conn = selectedConnection,
              (conn.resolvedActiveConfig(globalOrder: settings.protocolFailoverOrder)?.protocol ?? .wireguard) != .ipsec
        else { return }
        // Only auto-start when the rules say THIS connection on THIS network.
        // No match ⇒ leave it alone (engine parity); the saved on-demand
        // profile still carries the faithful rules for iOS to act on later.
        let result = rulesEngine.evaluate(rules: rules, state: networkState, masterEnabled: true)
        guard let matched = result.matchedRule else { return }
        let shouldStart: Bool
        switch matched.action {
        case .connectActive: shouldStart = true
        case .connection:    shouldStart = (matched.targetId == conn.id)
        case .pool, .noVpn:  shouldStart = false
        }
        guard shouldStart else { return }
        await connectSelected()   // start once → iOS owns the lifecycle (incl. doze)
    }

    /// Pick a target from the Connect-screen dropdown. Always allowed —
    /// even while connected: switch live by tearing down the old tunnel and
    /// connecting the newly chosen connection/pool.
    func selectTarget(_ id: String) async {
        let wasConnected = status.connected
        selectedTargetID = id
        guard wasConnected else { return }
        await teardownTunnel(armManualCooldown: false)
        await connectSelected()
    }

    func disconnect() async {
        connecting = true
        defer { connecting = false }
        // A manual disconnect cancels any active pause.
        pausedUntil = nil; pauseTimer?.cancel(); pauseTimer = nil
        // Mode A: stay disarmed until the user manually connects again.
        userDisconnectedManually = true
        await teardownTunnel(armManualCooldown: true)
        await tunnelManager.disarmOnDemand()
    }

    // MARK: - Manual pause / resume (Android ManualPauseSheet parity)

    /// True while a manual pause is in effect.
    var isPaused: Bool {
        guard let until = pausedUntil else { return false }
        return Date() < until
    }

    /// Pause the VPN: tear the tunnel down and freeze automation until
    /// `seconds` elapse (nil = until the user resumes). On-demand is turned
    /// off by the teardown so iOS doesn't immediately reconnect.
    func pause(seconds: TimeInterval?) async {
        pausedUntil = seconds.map { Date().addingTimeInterval($0) } ?? .distantFuture
        schedulePauseExpiry()
        connecting = true
        defer { connecting = false }
        await teardownTunnel(armManualCooldown: false)
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
        await tunnelManager.disconnect()
        await poolRepo.setActivePoolID("")
        activePool = nil
        activePoolMember = nil
        nextRotationAt = 0
        resetSpeedTracking()
    }

    // MARK: - Pool connect + rotation (Session 3)

    /// Detected user country for Geo-Nearest. Locale region is a
    /// reasonable proxy until a bundled MMDB + self-IP lookup lands.
    private var userCountry: String {
        if #available(iOS 16, *) { return Locale.current.region?.identifier ?? "" }
        return Locale.current.regionCode ?? ""
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
                    lastError = "\(member.name) did not pass traffic"
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
        connectError = lastError ?? "Pool has no reachable members"
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
    func importConnection(name: String, filename: String, content: String, intoConnectionID: String? = nil) async {
        let proto = ConfigImport.detectProtocol(filename: filename, content: content)
        let cfg = ProtocolConfig(
            id: UUID().uuidString,
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
        await tunnelManager.disconnect()
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
        try? await connectionRepo.removeConfig(connectionID: connectionID, configID: configID)
        connections = (try? await connectionRepo.loadAll()) ?? connections
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
            if status.connected {
                // Rule-driven disconnect is NOT a manual disconnect — do
                // not arm the manual cooldown (that would block the next
                // rule connect). Disconnect directly.
                rotationTimer?.cancel(); rotationTimer = nil
                await tunnelManager.disconnect()
                await poolRepo.setActivePoolID("")
                activePool = nil; activePoolMember = nil; nextRotationAt = 0
                resetSpeedTracking()
                lastRuleTargetID = "disconnect"
            }

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
                                                      rules: self.rules)
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
        await crashReporter.start(
            optedIn: settings.crashReportsEnabled,
            appVersion: PrivycsCoreInfo.version
        )
        self.connections = (try? await connectionRepo.loadAll()) ?? []
        self.pools = (try? await poolRepo.loadAll()) ?? []
        self.rules = (try? await rulesRepo.loadAll()) ?? []
        // Resolve endpoint country flags in the background (DNS + IP→country).
        Task { await refreshEndpointCountries() }

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
                // Master-toggle / rule changes re-arm or disarm on-demand.
                await syncOnDemand()
            }
        }
        Task {
            for await ns in await networkMonitor.observe() {
                self.networkState = ns
                await self.evaluateAndApplyRules()
            }
        }
        Task {
            for await st in await tunnelManager.observeStatus() {
                self.status = st
                self.ingestSpeedSample(st)
            }
        }
    }
}
