import Foundation
import UIKit
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

    /// Locally-imported connections (manual `.conf`, no gateway). Persisted via
    /// `connectionRepo`, shown alongside the gateway-pulled list.
    @Published var savedConnections: [SavedConnection] = []
    /// `id` of the selected saved connection. Mutually exclusive with
    /// `selectedConfigID` (only one selection source is active at a time).
    @Published var selectedSavedID: String?

    // Pools — full parity with the phone: the shared PrivycsCore PoolRepository +
    // PoolRotator run the same rotation engine on tvOS. Caveat: tvOS has no
    // BGTaskScheduler, so rotation advances on a foreground timer while the app is
    // open + connected; the tunnel itself stays up in the background regardless.
    let poolRepo = PoolRepository()
    let poolHealth = PoolHealthStore()
    private let rotator = PoolRotator()
    @Published var pools: [Pool] = []
    @Published var activePool: Pool?
    @Published var activePoolMember: PoolMember?
    @Published var nextRotationAt: Int64 = 0
    @Published var selectedPoolID: String?
    private var rotationTimer: Task<Void, Never>?
    /// User's pre-VPN country (ISO alpha-2) from the IP→MMDB SelfIPDetector — needed
    /// for the Geo-Nearest pool policy. Empty made the picker fall back to a RANDOM
    /// member (e.g. picking LAX from Austria).
    private var userCountry = ""

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
    /// Epoch ms at which the last accepted sample's counters were taken (producer clock).
    private var lastSampleAtEpochMs: Int64 = 0
    /// Consecutive samples whose counters sat below the baseline.
    private var regressionStreak = 0
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
        // A selected pool or saved (manual) connection wins — mutually exclusive.
        if selectedPoolID != nil || selectedSavedID != nil { return nil }
        guard let id = selectedConfigID else { return remoteConfigs.first }
        return remoteConfigs.first(where: { $0.id == id }) ?? remoteConfigs.first
    }

    var selectedSaved: SavedConnection? {
        if selectedPoolID != nil { return nil }
        guard let id = selectedSavedID else { return nil }
        return savedConnections.first { $0.id == id }
    }

    var selectedPool: Pool? {
        guard let id = selectedPoolID else { return nil }
        return pools.first { $0.id == id }
    }

    /// Whether anything is selected/connectable at all (pool / saved / gateway).
    var hasSelection: Bool { selectedPool != nil || selectedSaved != nil || selectedConfig != nil }

    /// Protocol for the dial — live when connected, else the current selection
    /// (pool > saved manual connection > gateway entry).
    var selectionProtocol: VpnProtocol? {
        if status.connected { return status.activeProtocol }
        if let p = selectedPool {
            return activePoolMember?.protocol ?? rotator.filterEligible(pool: p).first?.protocol ?? p.members.first?.protocol
        }
        if let s = selectedSaved { return s.resolvedActiveConfig()?.protocol ?? s.activeProtocol }
        return selectedConfig?.protocol
    }
    var selectionName: String? { selectedPool?.name ?? selectedSaved?.name ?? selectedConfig?.name }

    /// Select a pool, clearing the single-connection selections.
    func selectPool(_ id: String) { selectedPoolID = id; selectedSavedID = nil; selectedConfigID = nil }
    /// Select a saved (manual) connection, clearing pool + gateway selection.
    func selectSaved(_ id: String) { selectedSavedID = id; selectedPoolID = nil; selectedConfigID = nil }
    /// Select a gateway config, clearing pool + saved selection.
    func selectGateway(_ id: Int) { selectedConfigID = id; selectedPoolID = nil; selectedSavedID = nil }

    // MARK: — Lifecycle

    func bootstrap() async {
        if let s = try? await settingsRepo.current() {
            settings = s
        }
        // Apply the saved in-app language override (empty = follow the system).
        TVLanguageManager.shared.set(settings.appLanguage)
        // Apply the saved Theme to the window (after settings load — TVRootView's
        // onAppear ran before this with the default).
        applyTheme(settings.theme)
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
        // Detect the user's country (public IP → bundled MMDB) for Geo-Nearest —
        // fire-and-forget so it doesn't block launch (connectPool re-checks it).
        Task { [weak self] in self?.userCountry = await SelfIPDetector.shared.country() }
        savedConnections = (try? await connectionRepo.loadAll()) ?? []
        pools = (try? await poolRepo.loadAll()) ?? []
        // Show last-good gateway configs immediately; a live pull refreshes them
        // (and won't blank them if it fails through an active tunnel).
        remoteConfigs = loadCachedConfigs()
        // Observe live tunnel status from the controller (also reads the current
        // OS tunnel state — e.g. one kept up by the on-demand rule across an upgrade).
        observeStatus()
        refreshStatus()
        // Restore a previously-active pool: recover the current member (so the
        // Connect card shows the exit point) and RE-ARM the rotation timer — after
        // an upgrade the OS reconnects the tunnel but the app process is fresh, so
        // the timer was never started and the member was unknown.
        await resumePoolIfActive()
        // Auto-pull the config list if we're already enrolled.
        if isEnrolled {
            await refreshConfigs()
            // Always-on autostart: connect on launch if armed and not already up.
            // hasSelection covers pools too (selectedConfig is nil when a pool is picked).
            if settings.autoConnectOnStart, !status.connected, hasSelection {
                await connectSelected()
            }
        }
    }

    /// Restore runtime state for a pool that was active before this launch: bring
    /// back the current member for the UI and re-arm rotation (recomputing a stale
    /// next-rotation so it doesn't fire the instant the app opens).
    private func resumePoolIfActive() async {
        let id = await poolRepo.activePoolID()
        guard !id.isEmpty, let p = pools.first(where: { $0.id == id }) else { return }
        selectedPoolID = p.id
        let lastID = p.rotation?.lastUsedMemberID ?? ""
        let memID = lastID.isEmpty ? p.activeMemberID : lastID
        activePoolMember = p.members.first { $0.id == memID } ?? p.members.first
        guard let rot = p.rotation, rot.intervalSeconds > 0 else {
            activePool = p
            nextRotationAt = p.rotation?.nextRotationAt ?? 0
            return
        }
        var pp = p
        var r = rot
        let now = Int64(Date().timeIntervalSince1970)
        if r.nextRotationAt <= now {
            r.nextRotationAt = now + Int64(r.intervalSeconds)
            pp.rotation = r
            try? await poolRepo.save(pp)
            pools = (try? await poolRepo.loadAll()) ?? pools
        }
        activePool = pp
        nextRotationAt = pp.rotation?.nextRotationAt ?? 0
        scheduleRotationIfNeeded(pp)
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

    /// Apply the in-app Theme by overriding the UIWindow's interface style. tvOS's
    /// `.preferredColorScheme` doesn't reset the window back to .unspecified when
    /// going Dark/Light → System (it stayed dark), so we set the override directly.
    func applyTheme(_ theme: String) {
        let style: UIUserInterfaceStyle = theme == "dark" ? .dark : (theme == "light" ? .light : .unspecified)
        for scene in UIApplication.shared.connectedScenes {
            guard let ws = scene as? UIWindowScene else { continue }
            for w in ws.windows { w.overrideUserInterfaceStyle = style }
        }
    }

    /// ISO country code of the current exit point — the pool member's country when
    /// in a pool, else resolved from the endpoint host via the bundled MMDB. Drives
    /// the flag on the Connect details + pool card.
    @Published var endpointCountry = ""
    private var lastEndpointResolved = ""

    private func refreshEndpointCountry() {
        guard status.connected else { endpointCountry = ""; lastEndpointResolved = ""; return }
        if let cc = activePoolMember?.country, !cc.isEmpty { endpointCountry = cc; return }
        let ep = status.serverEndpoint
        guard !ep.isEmpty, ep != lastEndpointResolved else { return }
        lastEndpointResolved = ep
        let host = PoolImporter.endpointHost(ep)
        Task.detached { [weak self] in
            guard let ip = await PoolImporter.firstIP(host),
                  let cc = MmdbCountryResolver.shared?.country(forIP: ip) else { return }
            await MainActor.run { self?.endpointCountry = cc }
        }
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
                self.refreshEndpointCountry()
                try? await Task.sleep(nanoseconds: 1_000_000_000)
            }
        }
    }

    /// Derive live rx/tx speed + sparkline history from byte deltas between
    /// status samples (mirrors AppState.ingestSpeedSample on the phone).
    ///
    /// Timed against `countersAtEpochMs` — when the extension SAMPLED the counters
    /// — not against our read clock. tvOS stacks THREE free-running 1s timers (the
    /// extension's snapshot writer, TVTunnelController's poller, this loop), so a
    /// tick landing between writes re-reads identical byte totals; billing that
    /// zero delta to a full second showed 0 B/s, and the next tick then billed two
    /// producer intervals to one reader interval. Skipping unchanged snapshots and
    /// dividing by the true producer-side interval makes the readout independent of
    /// where the timers happen to line up.
    private func ingestSpeedSample(_ s: VpnStatus) {
        guard s.connected else {
            rxSpeed = 0; txSpeed = 0; rxHistory = []; txHistory = []
            lastSampleAtEpochMs = 0; lastSampleRx = 0; lastSampleTx = 0
            regressionStreak = 0
            return
        }

        if s.countersAtEpochMs > 0, s.countersAtEpochMs == lastSampleAtEpochMs { return }

        let nowMs = s.countersAtEpochMs > 0
            ? s.countersAtEpochMs
            : Int64(Date().timeIntervalSince1970 * 1000)

        if lastSampleAtEpochMs > 0 {
            let dt = Double(nowMs - lastSampleAtEpochMs) / 1000.0
            // A transient dip lasts one tick and the counter returns to its true
            // value — re-baselining to the dip would bill the recovery's catch-up
            // bytes to one interval and show a spike. A GENUINE reset (tunnel
            // restarted, protocol switched) persists; ignoring that forever would
            // peg the readout at 0 until the next disconnect. Tell them apart by
            // whether it sticks.
            if s.rxBytes < lastSampleRx || s.txBytes < lastSampleTx {
                regressionStreak += 1
                if regressionStreak >= 3 {
                    lastSampleRx = s.rxBytes
                    lastSampleTx = s.txBytes
                    lastSampleAtEpochMs = nowMs
                    regressionStreak = 0
                }
                rxSpeed = 0; txSpeed = 0
                rxHistory = Array((rxHistory + [0]).suffix(historyWindow))
                txHistory = Array((txHistory + [0]).suffix(historyWindow))
                return
            }
            regressionStreak = 0
            if dt > 0.1 {
                let dRx = Double(s.rxBytes - lastSampleRx) / dt
                let dTx = Double(s.txBytes - lastSampleTx) / dt
                rxSpeed = dRx
                txSpeed = dTx
                rxHistory = Array((rxHistory + [dRx]).suffix(historyWindow))
                txHistory = Array((txHistory + [dTx]).suffix(historyWindow))
            }
        }
        lastSampleRx = s.rxBytes
        lastSampleTx = s.txBytes
        lastSampleAtEpochMs = nowMs
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

    private let configCacheKey = "tv_cached_gateway_configs"

    private func cacheRemoteConfigs(_ c: [RemoteConfigEntry]) {
        if let d = try? JSONEncoder().encode(c) { UserDefaults.standard.set(d, forKey: configCacheKey) }
    }
    func loadCachedConfigs() -> [RemoteConfigEntry] {
        guard let d = UserDefaults.standard.data(forKey: configCacheKey),
              let c = try? JSONDecoder().decode([RemoteConfigEntry].self, from: d) else { return [] }
        return c
    }

    func refreshConfigs() async {
        guard let client = gatewayClient else { return }
        loadingConfigs = true
        configError = nil
        defer { loadingConfigs = false }
        do {
            let configs = try await client.listMyConfigs()
            if !configs.isEmpty {
                remoteConfigs = configs
                cacheRemoteConfigs(configs)
                if selectedConfigID == nil { selectedConfigID = configs.first?.id }
            } else if !status.connected {
                // Empty AND not behind a tunnel → trust it (gateway genuinely has
                // none). While connected, the pull goes THROUGH the tunnel and an
                // empty/failed result is almost always transient — keep the cache.
                remoteConfigs = []
                cacheRemoteConfigs([])
            }
        } catch {
            // Pull failed (e.g. gateway unreachable through an active VPN tunnel) —
            // keep the cached configs visible instead of blanking the list.
            configError = error.localizedDescription
        }
    }

    // MARK: — Connect / disconnect

    // Serialize ALL tunnel mutations (connect/disconnect/pool/rotate) so rapid
    // switches don't launch overlapping operations that race on the single
    // NETunnelProviderManager — that made "hin- und herschalten" unreliable.
    private var opChain: Task<Void, Never>?
    private func runExclusive(_ op: @escaping () async -> Void) async {
        let prev = opChain
        let t = Task { @MainActor in
            await prev?.value
            await op()
        }
        opChain = t
        await t.value
    }

    func toggle() async {
        if status.connected || tunnel.status.connected {
            await disconnect()
        } else {
            await connectSelected()
        }
    }

    func connectSelected() async { await runExclusive { await self.doConnectSelected() } }

    private func doConnectSelected() async {
        // Pool — run the rotation engine (same as the phone). The Connect screen
        // shows the pool card because doConnectPool sets activePool.
        if let pool = selectedPool {
            await doConnectPool(pool)
            return
        }
        // Normal (single) connection — NOT a pool. Clear any pool runtime state so
        // the Connect screen shows the plain view (no stale pool card / rotation).
        clearPoolState()
        await poolRepo.setActivePoolID("")
        // Manual (locally-imported) connection — already has its config, no fetch.
        if let saved = selectedSaved {
            connecting = true
            defer { connecting = false }
            configError = nil
            let dns = saved.dnsOverride.isEmpty ? settings.dnsOverride : saved.dnsOverride
            await tunnel.connect(saved, dnsOverride: dns, killSwitch: false,
                                 onDemand: settings.autoConnectOnStart, ssids: onDemandSSIDs)
            if let err = tunnel.lastError { configError = err }
            status = tunnel.status
            return
        }
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

    private func clearPoolState() {
        rotationTimer?.cancel(); rotationTimer = nil
        activePool = nil; activePoolMember = nil; nextRotationAt = 0
    }

    func disconnect() async { await runExclusive { await self.doDisconnect() } }

    private func doDisconnect() async {
        connecting = true
        defer { connecting = false }
        rotationTimer?.cancel(); rotationTimer = nil
        await tunnel.disconnect()
        activePool = nil; activePoolMember = nil; nextRotationAt = 0
        await poolRepo.setActivePoolID("")
        status = tunnel.status
    }

    // MARK: — Pool engine (shared PrivycsCore PoolRotator) — phone parity

    /// Connect a pool: pick an eligible member (round-robin / geo), bring up the
    /// tunnel, verify it passes traffic, and arm the rotation timer. Up to 3
    /// attempts, skipping members marked unreachable. Mirrors AppState.connectPool.
    func connectPool(_ pool: Pool) async { await runExclusive { await self.doConnectPool(pool) } }

    private func doConnectPool(_ pool: Pool) async {
        connecting = true
        defer { connecting = false }
        configError = nil
        if userCountry.isEmpty { userCountry = await SelfIPDetector.shared.country() }
        let unreachable = await poolHealth.unreachableMembers(pool: pool.id)
        var tried = Set<String>()
        var lastError: String?

        for _ in 0..<3 {
            guard let (member, updated) = rotator.pick(
                from: pool, userCountry: userCountry, excludingMemberIDs: unreachable.union(tried)
            ) else { break }
            tried.insert(member.id)
            try? await poolRepo.save(updated)
            await poolRepo.setActivePoolID(pool.id)
            activePool = updated
            activePoolMember = member
            nextRotationAt = updated.rotation?.nextRotationAt ?? 0

            let synth = synthConnection(for: member, pool: updated)
            await tunnel.connect(synth,
                                 dnsOverride: synth.dnsOverride.isEmpty ? settings.dnsOverride : synth.dnsOverride,
                                 killSwitch: false,
                                 onDemand: settings.autoConnectOnStart, ssids: onDemandSSIDs)
            if let err = tunnel.lastError {
                lastError = err
                await poolHealth.markUnreachable(pool: pool.id, member: member.id)
                continue
            }
            // Post-up traffic probe (WG/AWG expose rx via the App Group snapshot).
            try? await Task.sleep(nanoseconds: 5_000_000_000)
            let snap = TunnelStatsStore.read()
            if snap?.connected != true || (snap?.rxBytes ?? 0) == 0 {
                await poolHealth.markUnreachable(pool: pool.id, member: member.id)
                lastError = "\(member.name): no traffic"
                continue
            }
            scheduleRotationIfNeeded(updated)
            status = tunnel.status
            configError = nil
            return
        }
        configError = lastError ?? "Pool has no reachable members"
        status = tunnel.status
    }

    /// Rotate to the next member of the active pool (manual or timer-driven).
    func rotatePool() async { await runExclusive { await self.doRotatePool() } }

    private func doRotatePool() async {
        guard let pool = activePool else { return }
        let unreachable = await poolHealth.unreachableMembers(pool: pool.id)
        guard let (member, updated) = rotator.pick(from: pool, userCountry: userCountry, excludingMemberIDs: unreachable) else { return }
        try? await poolRepo.save(updated)
        activePool = updated
        activePoolMember = member
        nextRotationAt = updated.rotation?.nextRotationAt ?? 0
        let synth = synthConnection(for: member, pool: updated)
        await tunnel.connect(synth,
                             dnsOverride: synth.dnsOverride.isEmpty ? settings.dnsOverride : synth.dnsOverride,
                             killSwitch: false,
                             onDemand: settings.autoConnectOnStart, ssids: onDemandSSIDs)
        status = tunnel.status
    }

    /// Foreground rotation timer. tvOS has no BGTaskScheduler, so rotation only
    /// advances while the app is open + connected; the tunnel stays up regardless.
    private func scheduleRotationIfNeeded(_ pool: Pool) {
        rotationTimer?.cancel()
        guard let rot = pool.rotation, rot.intervalSeconds > 0 else { return }
        rotationTimer = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 5_000_000_000)
                guard let self else { return }
                let now = Int64(Date().timeIntervalSince1970)
                if self.status.connected, self.activePool != nil, self.nextRotationAt > 0, now >= self.nextRotationAt {
                    await self.rotatePool()
                }
            }
        }
    }

    /// Wrap one pool member as a transient SavedConnection (reuse the connect path).
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

    /// Persist edits to a pool (rotation/policy/DNS). When `reconnect` is set and
    /// this pool is the one currently connected, re-establish it so the change
    /// (e.g. DNS) takes effect — but ONLY if a tunnel was already up.
    func savePool(_ pool: Pool, reconnect: Bool = false) async {
        try? await poolRepo.save(pool)
        pools = (try? await poolRepo.loadAll()) ?? pools
        if activePool?.id == pool.id { activePool = pool }
        if reconnect, status.connected, activePool?.id == pool.id {
            await connectPool(pool)
        }
    }

    func deletePool(_ id: String) async {
        try? await poolRepo.delete(id)
        pools = (try? await poolRepo.loadAll()) ?? pools
        if selectedPoolID == id { selectedPoolID = nil }
        if activePool?.id == id {
            rotationTimer?.cancel()
            activePool = nil; activePoolMember = nil; nextRotationAt = 0
        }
    }

    // MARK: — Local import (manual config + backup restore over the LAN)

    enum TVImportResult {
        case config(name: String, proto: VpnProtocol)
        case backup(connections: Int)
        case pool(count: Int, skipped: Int)
        case unsupported(VpnProtocol)
        case failure(String)
    }

    /// Route a payload received from the local-network import server.
    @discardableResult
    func handleImport(_ payload: TVImportPayload) async -> TVImportResult {
        switch payload.kind {
        case .config:  return await importConfig(name: payload.name, content: payload.content)
        case .backup:  return await importBackup(payload.content, passphrase: payload.passphrase)
        case .pool:    return await importPool(payload.content)
        case .poolzip: return await importPoolZip(payload.content)
        case .file:    return await importFile(name: payload.name, base64: payload.content)
        }
    }

    /// Import an uploaded file (base64) — routed by extension. ZIP → pool; a raw
    /// config (.conf/.ovpn/.sswan/.mobileconfig) → a single connection. tvOS runs
    /// WG/AWG only, so OpenVPN/IPSec configs come back as `.unsupported`.
    func importFile(name: String, base64: String) async -> TVImportResult {
        let ext = (name as NSString).pathExtension.lowercased()
        if ext == "zip" {
            let base = (name as NSString).deletingPathExtension
            return await importPoolZip(base64, name: base.isEmpty ? "Imported Pool" : base)
        }
        guard let data = Data(base64Encoded: base64), let text = String(data: data, encoding: .utf8) else {
            return .failure("Invalid file data")
        }
        return await importConfigFile(filename: name, content: text)
    }

    /// Import a full Pool sent by the iPhone app (JSON of the Pool model) and run
    /// it through the SAME rotation engine as the phone.
    func importPool(_ json: String) async -> TVImportResult {
        guard let data = json.data(using: .utf8),
              let pool = try? JSONDecoder().decode(Pool.self, from: data) else {
            return .failure("Invalid pool data")
        }
        return await storePool(pool)
    }

    /// Import a pool from an uploaded ZIP (base64 from the browser upload form).
    func importPoolZip(_ base64: String, name: String = "Imported Pool") async -> TVImportResult {
        guard let zip = Data(base64Encoded: base64) else { return .failure("Invalid ZIP data") }
        let configs = PoolImporter.extractZip(zip)
        guard !configs.isEmpty else { return .failure("No config files found in the ZIP.") }
        var members = PoolImporter.makeMembers(configs)
        members = await PoolImporter.enrichCountries(members)
        let pool = Pool(id: UUID().uuidString, name: name, policy: .roundRobin,
                        members: members, rotation: PoolRotation(),
                        activeMemberID: members.first?.id ?? "")
        return await storePool(pool)
    }

    /// Persist a pool (filtered to tvOS-runnable WG/AWG members) and select it.
    private func storePool(_ pool: Pool) async -> TVImportResult {
        var p = pool
        let total = pool.members.count
        p.members = pool.members.filter { $0.config.protocol == .wireguard || $0.config.protocol == .amneziawg }
        guard !p.members.isEmpty else { return .pool(count: 0, skipped: total) }
        if !p.members.contains(where: { $0.id == p.activeMemberID }) { p.activeMemberID = p.members.first?.id ?? "" }
        do {
            try await poolRepo.save(p)
            pools = (try? await poolRepo.loadAll()) ?? pools
            selectPool(p.id)
            // If a tunnel is already up, switch to the just-imported pool so the
            // Connect screen reflects it (fire-and-forget; serialized via runExclusive).
            if status.connected { Task { await connectSelected() } }
            return .pool(count: p.members.count, skipped: total - p.members.count)
        } catch {
            return .failure(error.localizedDescription)
        }
    }

    /// Import a raw `.conf` (pasted text) as a saved connection.
    func importConfig(name rawName: String, content: String) async -> TVImportResult {
        let n = rawName.trimmingCharacters(in: .whitespacesAndNewlines)
        return await importConfigFile(filename: n.isEmpty ? "config.conf" : "\(n).conf", content: content)
    }

    /// Shared import: detect the protocol FROM THE FILENAME + content (so .ovpn/
    /// .sswan are classified correctly), save, select. tvOS runs WireGuard +
    /// AmneziaWG only — anything else comes back as `.unsupported`.
    private func importConfigFile(filename: String, content: String) async -> TVImportResult {
        let proto = ConfigImport.detectProtocol(filename: filename, content: content)
        guard proto == .wireguard || proto == .amneziawg else { return .unsupported(proto) }
        let base = (filename as NSString).deletingPathExtension
        let name = base.isEmpty ? ConfigImport.deriveConnectionName(content) : base
        let conn = ConfigImport.makeConnection(name: name, filename: filename, content: content)
        do {
            try await connectionRepo.save(conn)
            savedConnections = (try? await connectionRepo.loadAll()) ?? savedConnections
            selectSaved(conn.id)
            if status.connected { Task { await connectSelected() } }
            return .config(name: conn.name, proto: proto)
        } catch {
            return .failure(error.localizedDescription)
        }
    }

    /// Restore an encrypted backup blob (same cross-platform AES-256-GCM envelope
    /// as the phone/Android/desktop apps). tvOS has no pools/rules engine, so it
    /// restores connections + settings only.
    func importBackup(_ blob: String, passphrase: String) async -> TVImportResult {
        guard let data = blob.data(using: .utf8) else { return .failure("Invalid backup data") }
        do {
            let payload = try BackupManager.decrypt(data, password: passphrase)
            for c in payload.connections.connections { try? await connectionRepo.save(c) }
            if let pf = payload.pools { for p in pf.pools { try? await poolRepo.save(p) } }
            try? await settingsRepo.save(payload.settings)
            settings = payload.settings
            TVLanguageManager.shared.set(settings.appLanguage)
            savedConnections = (try? await connectionRepo.loadAll()) ?? savedConnections
            pools = (try? await poolRepo.loadAll()) ?? pools
            return .backup(connections: payload.connections.connections.count)
        } catch {
            let msg = (error as? BackupManager.BackupError)?.errorDescription ?? error.localizedDescription
            return .failure(msg)
        }
    }

    func deleteSaved(_ id: String) async {
        try? await connectionRepo.delete(id)
        savedConnections = (try? await connectionRepo.loadAll()) ?? savedConnections
        if selectedSavedID == id { selectedSavedID = nil }
    }
}
