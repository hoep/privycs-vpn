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
        savedConnections = (try? await connectionRepo.loadAll()) ?? []
        pools = (try? await poolRepo.loadAll()) ?? []
        let activePoolID = await poolRepo.activePoolID()
        if !activePoolID.isEmpty, let p = pools.first(where: { $0.id == activePoolID }) {
            activePool = p
            nextRotationAt = p.rotation?.nextRotationAt ?? 0
            selectedPoolID = p.id
        }
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
        // Pool — run the rotation engine (same as the phone).
        if let pool = selectedPool {
            await connectPool(pool)
            return
        }
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

    func disconnect() async {
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
    func connectPool(_ pool: Pool) async {
        connecting = true
        defer { connecting = false }
        configError = nil
        let unreachable = await poolHealth.unreachableMembers(pool: pool.id)
        var tried = Set<String>()
        var lastError: String?

        for _ in 0..<3 {
            guard let (member, updated) = rotator.pick(
                from: pool, userCountry: "", excludingMemberIDs: unreachable.union(tried)
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
    func rotatePool() async {
        guard let pool = activePool else { return }
        let unreachable = await poolHealth.unreachableMembers(pool: pool.id)
        guard let (member, updated) = rotator.pick(from: pool, userCountry: "", excludingMemberIDs: unreachable) else { return }
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
        if ext == "zip" { return await importPoolZip(base64) }
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
    func importPoolZip(_ base64: String) async -> TVImportResult {
        guard let zip = Data(base64Encoded: base64) else { return .failure("Invalid ZIP data") }
        let configs = PoolImporter.extractZip(zip)
        guard !configs.isEmpty else { return .failure("No config files found in the ZIP.") }
        var members = PoolImporter.makeMembers(configs)
        members = await PoolImporter.enrichCountries(members)
        let pool = Pool(id: UUID().uuidString, name: "Imported Pool", policy: .roundRobin,
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
