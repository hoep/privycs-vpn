#if canImport(Network)
import Foundation
import Network
// NOTE: NEHotspotNetwork (SSID/BSSID) lives in WiFiInfo.swift — importing
// NetworkExtension here makes Network's NWPath/NWInterface ambiguous because
// NetworkExtension re-exports Network.

/// Live Netzwerk-State-Observer via NWPathMonitor. Produziert
/// `NetworkState` snapshots in einem AsyncStream. Mirror der
/// Android `NetworkMonitor` (ConnectivityManager.NetworkCallback)
/// Semantik.
///
/// **SSID-Limit auf iOS**: CNCopyCurrentNetworkInfo gibt SSID nur
/// frei wenn die App **Location-Permission** hat UND eine der
/// erlaubten Entitlements (com.apple.developer.networking.HotspotConfiguration
/// oder access_wifi_information) gesetzt ist. Ohne das → empty SSID
/// + NetworkRules.match fängt das mit `SSIDMode.all` ab.
public actor NetworkMonitor {

    private let monitor: NWPathMonitor
    private let queue: DispatchQueue
    private var continuations: [UUID: AsyncStream<NetworkState>.Continuation] = [:]
    private var latestState: NetworkState = .none
    private var running = false

    public init(queue: DispatchQueue = .global(qos: .utility)) {
        self.monitor = NWPathMonitor()
        self.queue = queue
    }

    /// Startet das Monitoring. Idempotent. Hat keine Side-Effects
    /// vor erstem `observe()`.
    public func start() {
        guard !running else { return }
        running = true
        monitor.pathUpdateHandler = { [weak self] path in
            guard let self else { return }
            let state = NetworkMonitor.toState(path)
            Task { await self.publishEnriched(state) }
        }
        monitor.start(queue: queue)
    }

    public func stop() {
        running = false
        monitor.cancel()
    }

    public func currentState() -> NetworkState {
        latestState
    }

    /// AsyncStream — erstes Element ist die aktuelle State, danach
    /// jeder transition. Mehrere Observers OK; jeder kriegt seinen
    /// eigenen Stream.
    public func observe() -> AsyncStream<NetworkState> {
        let id = UUID()
        return AsyncStream<NetworkState>(bufferingPolicy: .bufferingNewest(8)) { continuation in
            Task {
                await self.registerContinuation(id: id, continuation: continuation)
                continuation.yield(self.latestState)
            }
            continuation.onTermination = { _ in
                Task { await self.unregisterContinuation(id: id) }
            }
        }
    }

    // MARK: — Path translation

    static func toState(_ path: NWPath) -> NetworkState {
        if path.status != .satisfied {
            return .none
        }
        let networkType: NetworkType
        if path.usesInterfaceType(.wifi) {
            networkType = .wifi
        } else if path.usesInterfaceType(.cellular) {
            networkType = .mobile
        } else if path.usesInterfaceType(.wiredEthernet) {
            networkType = .ethernet
        } else {
            networkType = .none
        }
        // SSID kommt aus CNCopyCurrentNetworkInfo, hier nicht
        // populiert — Caller (App-Layer) hydratiert wenn Permission
        // verfügbar. Background-NetworkRule-Eval auf iOS arbeitet
        // ohne SSID, das ist OK weil iOS' OnDemandRule-Mechanismus
        // SSID-aware ist und parallel läuft.
        return NetworkState(networkType: networkType, ssid: "")
    }

    /// Enrich a Wi-Fi state with SSID/BSSID (Access-WiFi-Information +
    /// foreground required on iOS) before publishing. Non-Wi-Fi states
    /// publish as-is. This makes SSID_EXACT / SSID_PATTERN / BSSID rules
    /// actually match (Android parity).
    private func publishEnriched(_ base: NetworkState) async {
        guard base.networkType == .wifi else { publish(base); return }
        let wifi = await WiFiInfo.current()
        var ssid = wifi.ssid
        var bssid = wifi.bssid
        // iOS hands out the SSID only in the foreground + with location
        // permission; a transient EMPTY read while we're still on Wi-Fi must
        // NOT publish a different state — that empty↔SSID flip-flop (plus the
        // missing dedup below) was the relentless "state flappt" flapping that
        // drove the rule engine in circles. Keep the last known SSID/BSSID
        // when the fresh read comes back empty.
        if ssid.isEmpty, latestState.networkType == .wifi, !latestState.ssid.isEmpty {
            ssid = latestState.ssid
            bssid = latestState.bssid
        }
        publish(NetworkState(networkType: .wifi, ssid: ssid, bssid: bssid))
    }

    // MARK: — Private continuation registry

    private func publish(_ state: NetworkState) {
        // DEDUP: NWPathMonitor fires pathUpdateHandler frequently (signal
        // changes, DNS, VPN path churn) even when the effective state is
        // unchanged. Publishing every time re-ran the whole rule engine and —
        // with the VPN itself changing the path — self-sustained a flap loop.
        // Only emit on a real transition.
        guard state != latestState else { return }
        latestState = state
        for (_, c) in continuations {
            c.yield(state)
        }
    }

    private func registerContinuation(
        id: UUID,
        continuation: AsyncStream<NetworkState>.Continuation
    ) {
        continuations[id] = continuation
    }

    private func unregisterContinuation(id: UUID) {
        continuations.removeValue(forKey: id)
    }
}

#endif
