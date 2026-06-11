import SwiftUI
import PrivycsCore

/// Top-level router: not-enrolled → device-code/manual enrollment; enrolled →
/// the main connect screen.
struct TVRootView: View {
    @EnvironmentObject private var state: TVAppState

    var body: some View {
        ZStack {
            TVColor.background.ignoresSafeArea()
            Group {
                if state.isEnrolled {
                    TVMainView()
                } else {
                    TVEnrollView()
                }
            }
        }
        .tint(TVColor.teal)
        .preferredColorScheme(.dark)
        .foregroundStyle(TVColor.onSurface)
    }
}
