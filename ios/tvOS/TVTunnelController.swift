import Foundation
import NetworkExtension
import PrivycsCore

/// Lean tvOS packet-tunnel controller — the living-room subset of the iOS
/// `VPNTunnelManager`. tvOS supports **WireGuard + AmneziaWG only**:
///   • OpenVPN — OpenVPNAdapter (OpenVPN3) does not build for tvOS, so the
///     tvOS tunnel doesn't link it (see `Tunnel/OpenVPNBridge` guard).
///   • IPSec — tvOS has no `NEVPNManager` Personal VPN, so IKEv2 is impossible.
/// Both rejected protocols surface a clear, localized error rather than
/// silently failing.
///
/// No on-demand rules, no kill-switch-rule translation, no pools rotation
/// engine here — a TV is a single fixed network with one connection at a time.
/// The kill-switch (IPv6-leak block) still flows through `providerConfiguration`
/// so the WG/AWG bridges apply it exactly as on iOS.
@MainActor
final class TVTunnelController: ObservableObject {

    @Published var status: VpnStatus = .disconnected
    @Published var lastError: String?

    private var observer: NSObjectProtocol?
    private var managers: [NETunnelProviderManager] = []
    private var pollTask: Task<Void, Never>?

    // Active-target metadata recorded on connect (the system only tells us
    // connected-state; identity + stats we track ourselves, like iOS).
    private var activeConnectionName = ""
    private var activeConnectionID = ""
    private var activeProtocol: VpnProtocol?

    init() {
        // Re-derive status only from the ALREADY-CACHED managers on each system
        // VPN status change — never reload from preferences in the observer
        // (that leaks Mach ports → crash; same lesson as iOS beta.22).
        observer = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange, object: nil, queue: .main
        ) { [weak self] _ in
            self?.refreshStatus()
        }
        Task { await reloadManagersAndRefresh() }
    }

    deinit {
        if let observer { NotificationCenter.default.removeObserver(observer) }
    }

    // MARK: — Connect / disconnect

    /// Connect a single (WG/AWG) connection. Rejects IPSec/OpenVPN with a
    /// localized error.
    func connect(_ connection: SavedConnection, dnsOverride: String, killSwitch: Bool) async {
        lastError = nil
        guard let config = connection.resolvedActiveConfig() else {
            lastError = String(localized: "tv.error.no_config",
                               defaultValue: "No protocol config selected")
            return
        }
        guard config.protocol == .wireguard || config.protocol == .amneziawg else {
            // The catalog value carries a `%@` placeholder for the protocol
            // name — look up the localized template, then substitute. (Passing
            // the name via `defaultValue:` interpolation would NOT fill the
            // catalog's `%@`; it would surface a literal "%@" at runtime.)
            let template = String(localized: "tv.error.unsupported_protocol")
            lastError = String(format: template, config.protocol.displayName)
            return
        }
        activeConnectionName = connection.name
        activeConnectionID = connection.id
        activeProtocol = config.protocol
        do {
            try await connectViaPTP(connection: connection, config: config,
                                    dnsOverride: dnsOverride, killSwitch: killSwitch)
            await reloadManagersAndRefresh()
            startPolling()
        } catch {
            lastError = error.localizedDescription
            PrivycsLog.log("tvOS connect failed: \(error.localizedDescription)")
        }
    }

    func disconnect() async {
        pollTask?.cancel(); pollTask = nil
        let mgrs = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        for m in mgrs { m.connection.stopVPNTunnel() }
        TunnelStatsStore.clear()
        activeProtocol = nil
        await reloadManagersAndRefresh()
    }

    // MARK: — Status observation

    func refreshStatus() {
        let livePTP = managers.first { m in
            switch m.connection.status {
            case .connected, .connecting, .reasserting: return true
            default: return false
            }
        }
        guard let m = livePTP else { status = .disconnected; return }
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
        status = VpnStatus(
            connected: connected,
            connectionName: connName,
            connectionID: connID,
            activeProtocol: proto,
            uptime: uptime,
            rxBytes: snap?.rxBytes ?? 0,
            txBytes: snap?.txBytes ?? 0,
            localAddress: snap?.localAddress ?? "",
            serverEndpoint: snap?.serverEndpoint ?? "",
            error: snap?.lastError ?? ""
        )
    }

    // MARK: — Private

    private func reloadManagersAndRefresh() async {
        managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        refreshStatus()
    }

    private func startPolling() {
        pollTask?.cancel()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                await MainActor.run { self?.refreshStatus() }
                try? await Task.sleep(nanoseconds: 1_000_000_000)
            }
        }
    }

    private func connectViaPTP(connection: SavedConnection, config: ProtocolConfig,
                               dnsOverride: String, killSwitch: Bool) async throws {
        let mgrs = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        let mgr = mgrs.first { $0.localizedDescription == connection.name } ?? NETunnelProviderManager()
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = TunnelProviderConfig.bundleIdentifier
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
        mgr.isOnDemandEnabled = false   // no on-demand on TV (single fixed network)
        try await mgr.saveToPreferences()
        try await mgr.loadFromPreferences()
        try await startTunnelRetrying(mgr)
    }

    /// The first `startVPNTunnel()` after a fresh `saveToPreferences()` often
    /// throws `.configurationStale`/`.configurationInvalid`; reload + retry up
    /// to 8× (same fix as iOS `VPNTunnelManager`).
    private func startTunnelRetrying(_ mgr: NEVPNManager, attempt: Int = 0) async throws {
        do {
            try mgr.connection.startVPNTunnel()
        } catch let err as NEVPNError where
            (err.code == .configurationStale || err.code == .configurationInvalid) && attempt < 8 {
            try await mgr.loadFromPreferences()
            try await startTunnelRetrying(mgr, attempt: attempt + 1)
        }
    }
}
