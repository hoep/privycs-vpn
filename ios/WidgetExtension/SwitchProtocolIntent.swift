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
        guard let snap = WidgetSnapshotStore.read(),
              let target = snap.switchTargets.first(where: { $0.protocolRaw == protocolRaw })
        else {
            return .result()
        }
        // SECURITY: the snapshot intentionally carries NO config content (it lives
        // in the unencrypted App Group UserDefaults). Re-read the raw config from
        // the Keychain (App Group, ThisDeviceOnly) at switch time, keyed by the
        // connection + config id the snapshot carries. Without it there is no
        // config to start, so bail (the app can perform the switch instead).
        let configContent: String
        if !target.configContent.isEmpty {
            // Defensive: honour an inline value if a future writer ever sets one.
            configContent = target.configContent
        } else {
            let key = KeychainKey.protocolConfig(connectionID: snap.connectionId, configID: target.configId)
            guard let stored = try? await KeychainSecretStore().get(key), !stored.isEmpty else {
                return .result()
            }
            configContent = stored
        }
        let managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        let mgr = managers.first(where: { $0.localizedDescription == snap.connectionName })
            ?? managers.first(where: { $0.isEnabled })
            ?? managers.first
            ?? NETunnelProviderManager()

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
        // Leave any existing on-demand rules untouched — the app re-syncs them
        // on next foreground; the widget only swaps the active protocol.
        try? await mgr.saveToPreferences()
        try? await mgr.loadFromPreferences()
        try? mgr.connection.startVPNTunnel()
        WidgetCenter.shared.reloadAllTimelines()
        return .result()
    }
}
