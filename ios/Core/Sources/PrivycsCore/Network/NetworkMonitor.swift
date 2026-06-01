#if canImport(Network)
import Foundation
import Network

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
            Task { await self.publish(state) }
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

    // MARK: — Private continuation registry

    private func publish(_ state: NetworkState) {
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
