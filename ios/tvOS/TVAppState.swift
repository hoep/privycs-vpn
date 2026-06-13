import Foundation
import PrivycsCore

/// Top-level tvOS app state. Reuses `PrivycsCore` verbatim — the same
/// `SettingsRepository` (gateway URL + Keychain-stored token),
/// `GatewayAPIClient` (config pull), and the live `TunnelStatsStore` the
/// packet-tunnel extension publishes — so the TV behaves like the phone app's
/// gateway-pull path with a living-room UI on top.
///
/// Holds the gateway `(url, token)` pair (from device-code enrollment or the
/// manual fallback), the pulled remote-config list, the current selection, and
/// a thin connect/disconnect surface over `TVTunnelController`.
@MainActor
final class TVAppState: ObservableObject {

    // Reused PrivycsCore singletons (subset the TV needs).
    let settingsRepo = SettingsRepository()
    let connectionRepo = ConnectionRepository()
    let tunnel = TVTunnelController()

    /// Public site base the device-code endpoints live under. Constant for the
    /// TV apps (the TV learns the *gateway* URL only after enrollment).
    // The device-code API (start/poll, public/no-auth) is served by the GATEWAY
    // host — www.privycs.com's nginx does NOT route /api/v1/tv/* to it (returns
    // 405); gateway.privycs.com does (200). The user-facing verification_uri
    // (app.privycs.com/link) comes back IN the /start response, so only this base
    // needs to point at the gateway.
    private let enrollmentBaseURL = URL(string: "https://gateway.privycs.com")!

    @Published var settings: AppSettings = .default

    /// Gateway-pulled configs the user can connect to. Each is imported into a
    /// transient `SavedConnection` on connect.
    @Published var remoteConfigs: [RemoteConfigEntry] = []
    /// `id` of the selected `RemoteConfigEntry` (empty = none picked yet).
    @Published var selectedConfigID: Int?

    @Published var loadingConfigs = false
    @Published var configError: String?

    /// Live tunnel status, mirrored from the controller for view convenience.
    @Published var status: VpnStatus = .disconnected
    @Published var connecting = false

    /// Live throughput (bytes/sec) + rolling history for the sparkline — derived
    /// from successive status samples, same as the phone app's ingestSpeedSample.
    @Published var rxSpeed: Double = 0
    @Published var txSpeed: Double = 0
    @Published var rxHistory: [Double] = []
    @Published var txHistory: [Double] = []
    @Published var health: TVHealthLevel = .none
    private var lastSampleRx: Int64 = 0
    private var lastSampleTx: Int64 = 0
    private var lastSampleAt: Date?
    private let historyWindow = 32

    private var statusTask: Task<Void, Never>?

    // MARK: — Derived

    /// True once enrollment produced a `(gatewayURL, token)` pair — routes the
    /// UI from the enroll screen to the main screen.
    var isEnrolled: Bool {
        !settings.gatewayURL.isEmpty && !settings.apiKey.isEmpty
    }

    /// Gateway client from current settings, or nil when not yet enrolled.
    var gatewayClient: GatewayAPIClient? {
        guard isEnrolled, let url = URL(string: settings.gatewayURL) else { return nil }
        return GatewayAPIClient(gatewayURL: url, apiKey: settings.apiKey)
    }

    /// A fresh device-code enrollment client.
    func makeEnrollmentClient() -> TVDeviceEnrollment {
        TVDeviceEnrollment(baseURL: enrollmentBaseURL)
    }

    var selectedConfig: RemoteConfigEntry? {
        guard let id = selectedConfigID else { return remoteConfigs.first }
        return remoteConfigs.first(where: { $0.id == id }) ?? remoteConfigs.first
    }

    // MARK: — Lifecycle

    func bootstrap() async {
        if let s = try? await settingsRepo.current() {
            settings = s
        }
        // Apply the saved in-app language override (empty = follow the system).
        TVLanguageManager.shared.set(settings.appLanguage)
        // tvOS: default the kill switch OFF (one-time). Its IPv6 ::/0 injection
        // forces all v6 into the tunnel; on tvOS the v6 data plane is unreliable,
        // so that blackholes IPv6 and breaks DNS/internet (Apple TV prefers v6).
        // The user can re-enable it in Settings (that choice then persists).
        let d = UserDefaults.standard
        if !d.bool(forKey: "tvKillSwitchDefaulted") {
            d.set(true, forKey: "tvKillSwitchDefaulted")
            if settings.killSwitchEnabled {
                await saveSettings { $0.killSwitchEnabled = false }
            }
        }
        loadSSIDs()
        // Observe live tunnel status from the controller.
        observeStatus()
        // Auto-pull the config list if we're already enrolled.
        if isEnrolled {
            await refreshConfigs()
            // Always-on autostart: connect on launch if armed and not already up.
            if settings.autoConnectOnStart, !status.connected, selectedConfig != nil {
                await connectSelected()
            }
        }
    }

    /// Always-on toggle: arm/disarm auto-connect (an OS on-demand connect rule).
    func setAutoConnect(_ on: Bool) async {
        await saveSettings { $0.autoConnectOnStart = on }
        if on { await connectSelected() }   // (re)connect with the on-demand rule armed
        else { await disconnect() }         // disarms on-demand so it stays off
    }

    // MARK: — WiFi-specific on-demand (SSID list)

    /// WiFi names the always-on rule restricts to. Empty = connect on any network.
    /// Stored in the App Group (tvOS-local; not part of the shared rules engine).
    @Published var onDemandSSIDs: [String] = []
    private let ssidKey = "tv_ondemand_ssids"
    private var ssidStore: UserDefaults? { UserDefaults(suiteName: "group.com.privycs.vpn") }

    private func loadSSIDs() {
        onDemandSSIDs = ssidStore?.stringArray(forKey: ssidKey) ?? []
    }
    private func persistSSIDs() {
        ssidStore?.set(onDemandSSIDs, forKey: ssidKey)
    }
    /// Add a WiFi name + re-arm if always-on is active.
    func addSSID(_ raw: String) async {
        let s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !s.isEmpty, !onDemandSSIDs.contains(s) else { return }
        onDemandSSIDs.append(s)
        persistSSIDs()
        if settings.autoConnectOnStart { await connectSelected() }
    }
    func removeSSID(_ s: String) async {
        onDemandSSIDs.removeAll { $0 == s }
        persistSSIDs()
        if settings.autoConnectOnStart { await connectSelected() }
    }

    func refreshStatus() {
        tunnel.refreshStatus()
        status = tunnel.status
    }

    private func observeStatus() {
        statusTask?.cancel()
        statusTask = Task { [weak self] in
            // The controller is @Published; poll its status into ours so the
            // views observe a single object. 1s cadence matches the PTP stats.
            while !Task.isCancelled {
                guard let self else { return }
                self.status = self.tunnel.status
                self.health = self.tunnel.health
                self.ingestSpeedSample(self.tunnel.status)
                try? await Task.sleep(nanoseconds: 1_000_000_000)
            }
        }
    }

    /// Derive live rx/tx speed + sparkline history from byte deltas between
    /// status samples (mirrors AppState.ingestSpeedSample on the phone).
    private func ingestSpeedSample(_ s: VpnStatus) {
        guard s.connected else {
            rxSpeed = 0; txSpeed = 0; rxHistory = []; txHistory = []
            lastSampleAt = nil; lastSampleRx = 0; lastSampleTx = 0
            return
        }
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

    // MARK: — Enrollment

    /// Persist a successful enrollment `(gatewayURL, token)` and pull configs.
    /// Used by BOTH the device-code success path and the manual fallback.
    func applyEnrollment(gatewayURL: String, token: String) async {
        var s = settings
        s.gatewayURL = gatewayURL
        s.apiKey = token
        try? await settingsRepo.save(s)
        settings = s
        await refreshConfigs()
    }

    /// Persist a settings change (DNS override, kill switch, crash reports …).
    func saveSettings(_ mutate: (inout AppSettings) -> Void) async {
        var s = settings
        mutate(&s)
        try? await settingsRepo.save(s)
        settings = s
    }

    /// Clear the stored gateway credentials (un-link this TV locally).
    func unenroll() async {
        var s = settings
        s.gatewayURL = ""
        s.apiKey = ""
        try? await settingsRepo.save(s)
        settings = s
        remoteConfigs = []
        selectedConfigID = nil
    }

    // MARK: — Config pull

    func refreshConfigs() async {
        guard let client = gatewayClient else { return }
        loadingConfigs = true
        configError = nil
        defer { loadingConfigs = false }
        do {
            let configs = try await client.listMyConfigs()
            remoteConfigs = configs
            if selectedConfigID == nil { selectedConfigID = configs.first?.id }
        } catch {
            configError = error.localizedDescription
        }
    }

    // MARK: — Connect / disconnect

    func toggle() async {
        if status.connected || tunnel.status.connected {
            await disconnect()
        } else {
            await connectSelected()
        }
    }

    func connectSelected() async {
        guard let entry = selectedConfig, let client = gatewayClient else { return }
        connecting = true
        defer { connecting = false }
        configError = nil
        do {
            // Download + render the .conf for this entry (WG/AWG JSON → wg-quick).
            let content = try await client.fetchConfig(entry: entry)
            let proto = ConfigImport.detectProtocol(filename: "\(entry.name).conf", content: content)
            let cfg = ProtocolConfig(
                id: "tv-\(entry.id)",
                protocol: proto,
                filename: "\(entry.name).conf",
                configContent: content,
                serverAddress: ConfigImport.extractServerAddress(content, proto)
            )
            let connection = SavedConnection(
                id: "tv-\(entry.id)",
                name: entry.name,
                protocols: [cfg],
                activeConfigID: cfg.id,
                dnsOverride: settings.dnsOverride
            )
            // killSwitch is HARD-OFF on tvOS: the WG "kill switch" injects ::/0 into
            // AllowedIPs (forces all IPv6 through the tunnel), but tvOS's v6 data
            // plane is unreliable → it blackholes IPv6 and kills internet/DNS.
            await tunnel.connect(connection,
                                 dnsOverride: settings.dnsOverride,
                                 killSwitch: false,
                                 onDemand: settings.autoConnectOnStart,
                                 ssids: onDemandSSIDs)
            if let err = tunnel.lastError { configError = err }
            status = tunnel.status
        } catch {
            configError = error.localizedDescription
        }
    }

    func disconnect() async {
        connecting = true
        defer { connecting = false }
        await tunnel.disconnect()
        status = tunnel.status
    }
}
