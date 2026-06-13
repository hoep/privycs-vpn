import SwiftUI
import PrivycsCore

/// Top-level router: not-enrolled → device-code/manual enrollment; enrolled →
/// the main connect screen.
struct TVRootView: View {
    @EnvironmentObject private var state: TVAppState

    var body: some View {
        ZStack {
            // Sleek depth (HIG: TVs are viewed at distance — use gradients + glow,
            // not a flat fill): dark/teal base + a soft teal glow toward the top.
            LinearGradient(colors: [TVColor.backgroundTop, TVColor.background],
                           startPoint: .top, endPoint: .bottom)
                .ignoresSafeArea()
            RadialGradient(colors: [TVColor.teal.opacity(0.16), .clear],
                           center: .init(x: 0.5, y: 0.0), startRadius: 0, endRadius: 1100)
                .ignoresSafeArea()
            Group {
                if state.isEnrolled {
                    TVMainView()
                } else {
                    TVEnrollView()
                }
            }
        }
        .tint(TVColor.teal)
        // NOTE: do NOT set a global .foregroundStyle here — it overrides the
        // system's prominent-button label contrast (made focused buttons white-on-
        // white). Each Text sets its own TVColor explicitly instead.
    }
}
