import SwiftUI
import PrivycsCore

@main
struct PrivycsVPNApp: App {

    @StateObject private var appState = AppState()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(appState)
                .task {
                    await appState.bootstrap()
                }
                .preferredColorScheme(appState.colorScheme)
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
    let entitlementRepo = EntitlementRepository()
    let networkMonitor = NetworkMonitor()
    let crashReporter = CrashReporter()
    let tunnelManager = VPNTunnelManager()

    @Published var settings: AppSettings = .default
    @Published var connections: [SavedConnection] = []
    @Published var pools: [Pool] = []
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
        if let pool = selectedPool {
            await connectPool(pool)
        } else if let conn = selectedConnection {
            connecting = true
            defer { connecting = false }
            resetSpeedTracking()
            do { try await tunnelManager.connect(conn, onDemand: settings.networkRulesEnabled, dnsOverride: resolvedDNS(for: conn)) }
            catch { connectError = error.localizedDescription }
        }
    }

    func disconnect() async {
        connecting = true
        defer { connecting = false }
        manualDisconnectUntil = Date().addingTimeInterval(30)
        lastRuleTargetID = nil
        rotationTimer?.cancel(); rotationTimer = nil
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
        guard let (member, updated) = rotator.pick(from: pool, userCountry: userCountry) else {
            connectError = "Pool has no reachable members"
            return
        }
        connecting = true
        defer { connecting = false }
        resetSpeedTracking()
        // Persist active member + next-rotation timestamp.
        try? await poolRepo.save(updated)
        await poolRepo.setActivePoolID(pool.id)
        activePool = updated
        activePoolMember = member
        nextRotationAt = updated.rotation?.nextRotationAt ?? 0

        do {
            let synth = synthConnection(for: member, pool: updated)
            try await tunnelManager.connect(synth, onDemand: settings.networkRulesEnabled, dnsOverride: resolvedDNS(for: synth))
            scheduleRotationIfNeeded(updated)
        } catch {
            connectError = error.localizedDescription
        }
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

    /// Foreground rotation scheduler — while the app is open and a
    /// rotating pool is connected, fire a rotation at nextRotationAt.
    /// Background/Doze-surviving rotation (BGTaskScheduler) is a known
    /// follow-up; iOS NE background budget is tight.
    private func scheduleRotationIfNeeded(_ pool: Pool) {
        rotationTimer?.cancel()
        guard let rot = pool.rotation, rot.intervalSeconds > 0 else { return }
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
        guard let (member, updated) = rotator.pick(from: pool, userCountry: userCountry) else { return }
        try? await poolRepo.save(updated)
        activePool = updated
        activePoolMember = member
        nextRotationAt = updated.rotation?.nextRotationAt ?? 0
        resetSpeedTracking()
        let synth = synthConnection(for: member, pool: updated)
        try? await tunnelManager.connect(synth, onDemand: settings.networkRulesEnabled, dnsOverride: resolvedDNS(for: synth))
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
        do { try await tunnelManager.connect(conn, onDemand: settings.networkRulesEnabled, dnsOverride: resolvedDNS(for: conn)) }
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
                try? await self.tunnelManager.connect(conn, onDemand: self.settings.networkRulesEnabled, dnsOverride: self.resolvedDNS(for: conn))
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

        // Initial load
        if let s = try? await settingsRepo.current() {
            self.settings = s
        }
        await crashReporter.start(
            optedIn: settings.crashReportsEnabled,
            appVersion: PrivycsCoreInfo.version
        )
        self.connections = (try? await connectionRepo.loadAll()) ?? []
        self.pools = (try? await poolRepo.loadAll()) ?? []
        self.rules = (try? await rulesRepo.loadAll()) ?? []

        // Restore active-pool selection across app restarts.
        let activeID = await poolRepo.activePoolID()
        if !activeID.isEmpty, let p = pools.first(where: { $0.id == activeID }) {
            self.activePool = p
            self.selectedTargetID = "pool:\(p.id)"
            self.activePoolMember = p.members.first(where: { $0.id == p.activeMemberID })
            self.nextRotationAt = p.rotation?.nextRotationAt ?? 0
        }

        // Observe transitions
        Task {
            for await s in await settingsRepo.observe() {
                self.settings = s
                await crashReporter.start(
                    optedIn: s.crashReportsEnabled,
                    appVersion: PrivycsCoreInfo.version
                )
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
