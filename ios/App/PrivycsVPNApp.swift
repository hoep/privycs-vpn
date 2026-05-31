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

    var colorScheme: ColorScheme? {
        switch settings.theme {
        case "dark": return .dark
        case "light": return .light
        default: return nil  // system default
        }
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
            }
        }
    }
}
