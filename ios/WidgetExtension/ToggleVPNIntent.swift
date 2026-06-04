import AppIntents
import NetworkExtension
import WidgetKit

/// In-place connect/disconnect from the home-screen widget (iOS 17
/// interactive widget). Runs in the widget extension's process — which
/// carries the network-extension entitlement — so it drives the saved
/// `NETunnelProviderManager` directly, without launching the app. This is
/// the Android-parity "tap the disc = toggle" behavior.
///
/// Best-effort by design: it reconnects/disconnects the LAST-USED tunnel
/// manager (the one the app configured on its last connect). If nothing is
/// configured yet (the user has never connected from the app), it no-ops —
/// a body tap on the widget then opens the app to set things up. The richer
/// pool/pause/network-rule routing stays in the app; the widget toggle is
/// the simple "bring the last thing up / take it down" control.
struct ToggleVPNIntent: AppIntent {
    static var title: LocalizedStringResource = "Toggle VPN"
    static var description = IntentDescription("Connect or disconnect the Privycs VPN tunnel.")
    /// Stay on the home screen — do not bring the app to the foreground.
    static var openAppWhenRun: Bool = false

    func perform() async throws -> some IntentResult {
        let managers = (try? await NETunnelProviderManager.loadAllFromPreferences()) ?? []
        // Prefer the enabled (last-used) manager; fall back to the first saved.
        guard let mgr = managers.first(where: { $0.isEnabled }) ?? managers.first else {
            return .result()
        }
        switch mgr.connection.status {
        case .connected, .connecting, .reasserting:
            mgr.connection.stopVPNTunnel()
        default:
            // A manager the app saved is normally enabled; guard the edge case
            // so startVPNTunnel doesn't throw on a disabled profile.
            if !mgr.isEnabled {
                mgr.isEnabled = true
                try? await mgr.saveToPreferences()
                try? await mgr.loadFromPreferences()
            }
            try? mgr.connection.startVPNTunnel()
        }
        WidgetCenter.shared.reloadAllTimelines()
        return .result()
    }
}
