import AppIntents
import NetworkExtension
import WidgetKit
import PrivycsCore

/// In-place protocol switch from the home-screen widget (WG/AWG/OpenVPN pills).
/// Reconfigures the connection's saved NETunnelProviderManager and (re)starts it
/// in the widget process — no app launch.
///
/// Mirrors the APP's connectViaPTP start sequence EXACTLY: deactivate other VPNs
/// (single active), reconfigure, save, then start with a RELOAD+RETRY loop. The
/// retry is essential — right after saveToPreferences the system briefly reports
/// a stale/invalid config (surfaces as "configuration type is wrong"); the app
/// recovers by reloading + retrying, the widget previously started only once and
/// so failed. Config is read from the Keychain (App Group) — never the snapshot.
struct SwitchProtocolIntent: AppIntent {
    static var title: LocalizedStringResource = "Switch VPN Protocol"
    static var openAppWhenRun: Bool = false

    @Parameter(title: "Protocol") var protocolRaw: String

    init() {}
    init(protocolRaw: String) { self.protocolRaw = protocolRaw }

    func perform() async throws -> some IntentResult {
        guard let snap = WidgetSnapshotStore.read() else {
            PrivycsLog.log("widget switch[\(protocolRaw)]: no snapshot"); return .result()
        }
        guard let target = snap.switchTargets.first(where: { $0.protocolRaw == protocolRaw }) else {
            PrivycsLog.log("widget switch[\(protocolRaw)]: no target"); return .result()
        }
        // Config from the Keychain (the snapshot carries no secret).
        let configContent: String
        if !target.configContent.isEmpty {
            configContent = target.configContent
        } else {
            let key = KeychainKey.protocolConfig(connectionID: snap.connectionId, configID: target.configId)
            guard let stored = try? await KeychainSecretStore().get(key), !stored.isEmpty else {
                PrivycsLog.log("widget switch[\(protocolRaw)]: keychain MISS"); return .result()
            }
            configContent = stored
        }

        let managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        let mgr = managers.first(where: { $0.localizedDescription == snap.connectionName })
            ?? managers.first(where: { $0.isEnabled })
            ?? managers.first
            ?? NETunnelProviderManager()

        // Single active VPN: drop the IPSec personal VPN + every other PTP manager
        // so this packet-tunnel can become the sole active one.
        let ike = NEVPNManager.shared()
        try? await ike.loadFromPreferences()
        if ike.isEnabled || ike.isOnDemandEnabled
            || ike.connection.status == .connected || ike.connection.status == .connecting {
            ike.connection.stopVPNTunnel(); ike.isOnDemandEnabled = false; ike.isEnabled = false
            try? await ike.saveToPreferences()
        }
        for other in managers where other !== mgr {
            if other.isEnabled || other.connection.status == .connected || other.connection.status == .connecting {
                other.connection.stopVPNTunnel(); other.isOnDemandEnabled = false; other.isEnabled = false
                try? await other.saveToPreferences()
            }
        }

        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = TunnelProviderConfig.bundleIdentifier
        proto.serverAddress = target.serverAddress
        proto.providerConfiguration = TunnelProviderConfig.make(
            protocolRaw: target.protocolRaw, configContent: configContent,
            connectionId: snap.connectionId, configId: target.configId,
            dnsOverride: target.dnsOverride, killSwitch: snap.killSwitch
        )
        mgr.protocolConfiguration = proto
        mgr.localizedDescription = snap.connectionName.isEmpty ? "Privycs VPN" : snap.connectionName
        mgr.isEnabled = true
        try? await mgr.saveToPreferences()

        // start + reload/retry (mirror VPNTunnelManager.startTunnelRetrying). The
        // post-save "configuration type is wrong"/stale/invalid is transient and
        // clears after a reload — the app relies on exactly this.
        var started = false
        for attempt in 0..<8 {
            do {
                try mgr.connection.startVPNTunnel()
                started = true
                break
            } catch {
                PrivycsLog.log("widget switch[\(protocolRaw)]: start attempt \(attempt) err=\(error.localizedDescription) — reloading")
                try? await mgr.loadFromPreferences()
            }
        }
        PrivycsLog.log("widget switch[\(protocolRaw)]: \(started ? "started OK" : "FAILED after retries")")
        WidgetCenter.shared.reloadAllTimelines()
        return .result()
    }
}
