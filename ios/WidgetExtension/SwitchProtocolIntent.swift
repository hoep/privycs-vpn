import AppIntents
import NetworkExtension
import WidgetKit
import PrivycsCore

/// In-place protocol switch from the home-screen widget (WG/AWG/OpenVPN pills).
///
/// Mirrors the APP's switch (setActiveConfig → stopTunnel → connect): you must
/// STOP the currently-active tunnel and WAIT for teardown BEFORE starting the new
/// protocol — otherwise startVPNTunnel() on an already-connected manager is a
/// no-op ("started OK" but nothing switches). Then start with a reload+retry loop
/// (the post-save config is briefly stale → "configuration type is wrong").
/// Config is read from the Keychain (App Group); the snapshot carries no secret.
struct SwitchProtocolIntent: AppIntent {
    static var title: LocalizedStringResource = "Switch VPN Protocol"
    static var openAppWhenRun: Bool = false

    @Parameter(title: "Protocol") var protocolRaw: String

    init() {}
    init(protocolRaw: String) { self.protocolRaw = protocolRaw }

    private func waitUntilDisconnected(_ conn: NEVPNConnection, _ tag: String) async {
        for _ in 0..<25 {   // ~5s
            let s = conn.status
            if s == .disconnected || s == .invalid { return }
            try? await Task.sleep(nanoseconds: 200_000_000)
        }
        PrivycsLog.log("widget switch[\(protocolRaw)]: \(tag) still \(conn.status.rawValue) after wait")
    }

    func perform() async throws -> some IntentResult {
        guard let snap = WidgetSnapshotStore.read() else {
            PrivycsLog.log("widget switch[\(protocolRaw)]: no snapshot"); return .result()
        }
        guard let target = snap.switchTargets.first(where: { $0.protocolRaw == protocolRaw }) else {
            PrivycsLog.log("widget switch[\(protocolRaw)]: no target"); return .result()
        }
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

        // 1) Stop whatever is currently up + disarm, so the new protocol can take
        //    over (no-op start otherwise). Then WAIT for actual teardown.
        let ike = NEVPNManager.shared()
        try? await ike.loadFromPreferences()
        PrivycsLog.log("widget switch[\(protocolRaw)]: pre ike=\(ike.connection.status.rawValue) mgr=\(mgr.connection.status.rawValue) managers=\(managers.count)")
        if ike.isEnabled || ike.isOnDemandEnabled || ike.connection.status == .connected || ike.connection.status == .connecting {
            ike.connection.stopVPNTunnel(); ike.isOnDemandEnabled = false; ike.isEnabled = false
            try? await ike.saveToPreferences()
        }
        for other in managers where other !== mgr {
            if other.isEnabled || other.connection.status == .connected || other.connection.status == .connecting {
                other.connection.stopVPNTunnel(); other.isOnDemandEnabled = false; other.isEnabled = false
                try? await other.saveToPreferences()
            }
        }
        // CRITICAL: disarm on-demand on the target manager BEFORE stopping it.
        // Otherwise iOS re-triggers the OLD protocol's on-demand and restarts it
        // ("WireGuard starting…" right after tapping OpenVPN) — fighting the switch.
        if mgr.isOnDemandEnabled {
            mgr.isOnDemandEnabled = false
            try? await mgr.saveToPreferences()
        }
        if mgr.connection.status == .connected || mgr.connection.status == .connecting || mgr.connection.status == .reasserting {
            mgr.connection.stopVPNTunnel()
        }
        await waitUntilDisconnected(ike.connection, "ike")
        await waitUntilDisconnected(mgr.connection, "mgr")

        // 2) Reconfigure the target manager to the new protocol + save. on-demand
        //    stays OFF for the manual switch (the app re-syncs it on next foreground).
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
        mgr.isOnDemandEnabled = false
        try? await mgr.saveToPreferences()

        // 3) Start with reload+retry (post-save stale/invalid config recovery).
        var started = false
        for attempt in 0..<8 {
            do { try mgr.connection.startVPNTunnel(); started = true; break }
            catch {
                PrivycsLog.log("widget switch[\(protocolRaw)]: start attempt \(attempt) err=\(error.localizedDescription) — reloading")
                try? await mgr.loadFromPreferences()
            }
        }
        PrivycsLog.log("widget switch[\(protocolRaw)]: \(started ? "started" : "FAILED") post-mgr=\(mgr.connection.status.rawValue)")
        WidgetCenter.shared.reloadAllTimelines()
        return .result()
    }
}
