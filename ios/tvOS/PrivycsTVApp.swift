import SwiftUI
import PrivycsCore

/// Apple TV (tvOS) entry point. Mirrors the iOS `PrivycsVPNApp` but with a
/// living-room-shaped, focus-navigable UI: device-code enrollment → focusable
/// server list → one big connect/disconnect control. No QR scan, no file
/// import, no network-rules engine, no per-app VPN, no IPSec/OpenVPN (tvOS
/// supports WireGuard + AmneziaWG only — see `TVTunnelController`).
@main
struct PrivycsTVApp: App {

    @StateObject private var state = TVAppState()
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            TVRootView()
                .environmentObject(state)
                .task { await state.bootstrap() }
                .onChange(of: scenePhase) { _, phase in
                    if phase == .active { state.refreshStatus() }
                }
        }
    }
}
