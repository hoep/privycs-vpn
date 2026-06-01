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
        guard let conn = selectedConnection else { return }
        connecting = true
        defer { connecting = false }
        resetSpeedTracking()
        try? await tunnelManager.connect(conn)
    }

    func disconnect() async {
        connecting = true
        defer { connecting = false }
        await tunnelManager.disconnect()
        resetSpeedTracking()
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
