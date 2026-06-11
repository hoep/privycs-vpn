import AppIntents
import NetworkExtension
import WidgetKit
import PrivycsCore

/// In-place protocol switch from the home-screen widget — WG/AWG/OpenVPN AND
/// IPSec. The widget extension CANNOT reconfigure a tunnel (the config is
/// app-owned — a reconfigure+start silently runs the OLD protocol). So the app
/// pre-creates a READY-TO-START manager per protocol (PTP: one
/// NETunnelProviderManager each, named "<conn> · <Protocol>"; IPSec: the shared
/// NEVPNManager slot loaded with the connection's IKEv2 profile). The widget
/// then only STOPS the current tunnel and STARTS the target — no reconfigure,
/// exactly like the working ToggleVPNIntent. If the target manager doesn't exist
/// yet (that protocol was never connected in-app), it logs + bails.
struct SwitchProtocolIntent: AppIntent {
    static var title: LocalizedStringResource = "Switch VPN Protocol"
    static var openAppWhenRun: Bool = false

    @Parameter(title: "Protocol") var protocolRaw: String

    init() {}
    init(protocolRaw: String) { self.protocolRaw = protocolRaw }

    private func waitDown(_ conn: NEVPNConnection) async {
        for _ in 0..<20 {   // ~4s
            if conn.status == .disconnected || conn.status == .invalid { return }
            try? await Task.sleep(nanoseconds: 200_000_000)
        }
    }

    private func startWithRetry(_ mgr: NEVPNManager) async -> Bool {
        for _ in 0..<8 {
            do { try mgr.connection.startVPNTunnel(); return true }
            catch { try? await mgr.loadFromPreferences() }
        }
        return false
    }

    func perform() async throws -> some IntentResult {
        guard let snap = WidgetSnapshotStore.read() else {
            PrivycsLog.log("widget switch[\(protocolRaw)]: no snapshot"); return .result()
        }
        let managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        let ike = NEVPNManager.shared()
        try? await ike.loadFromPreferences()

        // Disarm + stop every PTP manager + the IPSec slot that ISN'T the target,
        // so nothing (incl. on-demand) fights the switch. Identify the target.
        let targetName = TunnelProviderConfig.ptpManagerName(
            connectionName: snap.connectionName, protocolRaw: protocolRaw)
        let target: NETunnelProviderManager? = (protocolRaw == "ipsec")
            ? nil : managers.first(where: { $0.localizedDescription == targetName })

        if protocolRaw != "ipsec" && target == nil {
            PrivycsLog.log("widget switch[\(protocolRaw)]: manager '\(targetName)' not found — connect it once in-app first")
            return .result()
        }
        if protocolRaw == "ipsec" && !(ike.protocolConfiguration != nil && ike.localizedDescription == snap.connectionName) {
            PrivycsLog.log("widget switch[ipsec]: slot not configured for '\(snap.connectionName)' — connect IPSec once in-app first")
            return .result()
        }

        for m in managers where m !== target {
            if m.isEnabled || m.connection.status == .connected || m.connection.status == .connecting || m.connection.status == .reasserting {
                m.isOnDemandEnabled = false; m.isEnabled = false
                try? await m.saveToPreferences()
                m.connection.stopVPNTunnel()
            }
        }
        if protocolRaw != "ipsec" {
            // stopping IPSec slot too
            if ike.isEnabled || ike.connection.status == .connected || ike.connection.status == .connecting || ike.connection.status == .reasserting {
                ike.isOnDemandEnabled = false; ike.isEnabled = false
                try? await ike.saveToPreferences()
                ike.connection.stopVPNTunnel()
            }
            await waitDown(ike.connection)
        }

        // Start the target — it is ALREADY configured by the app; just enable + start.
        let started: Bool
        if protocolRaw == "ipsec" {
            await waitDown(managers.first(where: { $0.connection.status != .disconnected })?.connection ?? ike.connection)
            ike.isOnDemandEnabled = false; ike.isEnabled = true
            try? await ike.saveToPreferences()
            started = await startWithRetry(ike)
            PrivycsLog.log("widget switch[ipsec]: \(started ? "started" : "FAILED") post=\(ike.connection.status.rawValue)")
        } else if let t = target {
            await waitDown(t.connection)
            if !t.isEnabled { t.isEnabled = true; try? await t.saveToPreferences() }
            started = await startWithRetry(t)
            PrivycsLog.log("widget switch[\(protocolRaw)]: target='\(targetName)' \(started ? "started" : "FAILED") post=\(t.connection.status.rawValue)")
        } else {
            started = false
        }
        // Sync the widget UI immediately: the active-protocol pill highlight comes
        // from the snapshot's protocolRaw, which only the app writes — so without
        // this the pill wouldn't update until the app is next opened. Write the new
        // active protocol back ourselves, then reload.
        if started, var s = WidgetSnapshotStore.read() {
            s.protocolRaw = protocolRaw
            s.connected = true
            s.updatedAtEpoch = Int64(Date().timeIntervalSince1970)
            WidgetSnapshotStore.write(s)
        }
        WidgetCenter.shared.reloadAllTimelines()
        return .result()
    }
}
