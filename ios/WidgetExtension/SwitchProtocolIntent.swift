import AppIntents
import WidgetKit
import PrivycsCore

/// Protocol switch from the home-screen widget (WG/AWG/OpenVPN pills).
///
/// The widget extension CANNOT reliably reconfigure + start a packet-tunnel:
/// even with the Keychain entitlement + single-active-VPN handling, the start
/// failed with "configuration type is wrong" (device-log confirmed). So the
/// pill hands the switch to the APP — which has the proven setActiveConfig path
/// (stop → deactivate others → reconfigure → start) and full Keychain access —
/// by recording the request (non-secret identifiers) in the shared App Group
/// and opening the app. `AppState.consumePendingProtocolSwitch()` applies it on
/// foreground. (IPSec pills already open the app, so this is consistent.)
struct SwitchProtocolIntent: AppIntent {
    static var title: LocalizedStringResource = "Switch VPN Protocol"
    static var openAppWhenRun: Bool = true

    @Parameter(title: "Protocol") var protocolRaw: String

    init() {}
    init(protocolRaw: String) { self.protocolRaw = protocolRaw }

    func perform() async throws -> some IntentResult {
        guard let snap = WidgetSnapshotStore.read(),
              let target = snap.switchTargets.first(where: { $0.protocolRaw == protocolRaw })
        else {
            return .result()
        }
        if let d = UserDefaults(suiteName: "group.com.privycs.vpn") {
            d.set("\(snap.connectionId)|\(target.configId)", forKey: "pendingProtocolSwitch")
        }
        PrivycsLog.log("widget switch[\(protocolRaw)]: handed to app (conn=\(snap.connectionId) cfg=\(target.configId))")
        return .result()
    }
}
