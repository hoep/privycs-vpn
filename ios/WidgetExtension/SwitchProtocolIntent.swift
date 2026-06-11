import AppIntents
import NetworkExtension
import WidgetKit
import PrivycsCore

/// In-place protocol switch from the home-screen widget (WG/AWG/OpenVPN).
/// Reuses the app-resolved `WidgetSwitchTarget` (config + DNS already
/// resolved by the app) and the SHARED `TunnelProviderConfig.make`, so the
/// `providerConfiguration` the widget writes is byte-identical to what the
/// app writes — no logic duplicated, no drift. It reconfigures the active
/// connection's saved `NETunnelProviderManager` and (re)starts it; the NE
/// framework tears down the old protocol's session and brings up the new one.
///
/// IPSec is never a target here (the app omits it — IKEv2 + cert parsing
/// can't be reproduced in the widget), so an IPSec pill instead falls
/// through to the widget's open-app URL.
struct SwitchProtocolIntent: AppIntent {
    static var title: LocalizedStringResource = "Switch VPN Protocol"
    static var openAppWhenRun: Bool = false

    @Parameter(title: "Protocol") var protocolRaw: String

    init() {}
    init(protocolRaw: String) { self.protocolRaw = protocolRaw }

    func perform() async throws -> some IntentResult {
        // Diagnostic trace (lands in the shared App-Group log → visible in the
        // app's Logs screen) — the widget switch silently no-op'd on device and
        // we need the exact failure point. Logs identifiers only, never secrets.
        guard let snap = WidgetSnapshotStore.read() else {
            PrivycsLog.log("widget switch[\(protocolRaw)]: no snapshot")
            return .result()
        }
        guard let target = snap.switchTargets.first(where: { $0.protocolRaw == protocolRaw }) else {
            PrivycsLog.log("widget switch[\(protocolRaw)]: no target (targets=\(snap.switchTargets.map { $0.protocolRaw })) connId=\(snap.connectionId)")
            return .result()
        }
        // SECURITY: the snapshot carries NO config content (it would land in the
        // unencrypted App Group UserDefaults). Re-read the raw config from the
        // Keychain at switch time, keyed by the connection + config id.
        let configContent: String
        if !target.configContent.isEmpty {
            configContent = target.configContent
        } else {
            let key = KeychainKey.protocolConfig(connectionID: snap.connectionId, configID: target.configId)
            do {
                let stored = try await KeychainSecretStore().get(key)
                guard let stored, !stored.isEmpty else {
                    PrivycsLog.log("widget switch[\(protocolRaw)]: keychain MISS key=\(key) connId=\(snap.connectionId) cfgId=\(target.configId)")
                    return .result()
                }
                configContent = stored
                PrivycsLog.log("widget switch[\(protocolRaw)]: keychain hit \(stored.count)B")
            } catch {
                PrivycsLog.log("widget switch[\(protocolRaw)]: keychain ERROR \(error.localizedDescription) key=\(key)")
                return .result()
            }
        }
        let managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        let mgr = managers.first(where: { $0.localizedDescription == snap.connectionName })
            ?? managers.first(where: { $0.isEnabled })
            ?? managers.first
            ?? NETunnelProviderManager()
        PrivycsLog.log("widget switch[\(protocolRaw)]: managers=\(managers.count) chosen=\(mgr.localizedDescription ?? "<new>")")

        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = TunnelProviderConfig.bundleIdentifier
        proto.serverAddress = target.serverAddress
        proto.providerConfiguration = TunnelProviderConfig.make(
            protocolRaw: target.protocolRaw,
            configContent: configContent,
            connectionId: snap.connectionId,
            configId: target.configId,
            dnsOverride: target.dnsOverride,
            killSwitch: snap.killSwitch
        )
        mgr.protocolConfiguration = proto
        mgr.localizedDescription = snap.connectionName.isEmpty ? "Privycs VPN" : snap.connectionName
        mgr.isEnabled = true
        do {
            try await mgr.saveToPreferences()
            try await mgr.loadFromPreferences()
            try mgr.connection.startVPNTunnel()
            PrivycsLog.log("widget switch[\(protocolRaw)]: started OK")
        } catch {
            PrivycsLog.log("widget switch[\(protocolRaw)]: start ERROR \(error.localizedDescription)")
        }
        WidgetCenter.shared.reloadAllTimelines()
        return .result()
    }
}
