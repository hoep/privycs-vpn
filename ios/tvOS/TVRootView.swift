import SwiftUI
import PrivycsCore

/// Top-level router: not-enrolled → device-code/manual enrollment; enrolled →
/// the main connect screen.
struct TVRootView: View {
    @EnvironmentObject private var state: TVAppState
    // Observe the in-app language override so a Settings change re-renders the
    // whole tree immediately (mirrors the iOS RootView pattern).
    @ObservedObject private var lang = TVLanguageManager.shared

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
        // Theme (System/Dark/Light) is applied by setting the UIWindow's
        // overrideUserInterfaceStyle directly (TVAppState.applyTheme) — NOT via
        // .preferredColorScheme, which on tvOS doesn't reset the window override
        // back to .unspecified when switching from Dark/Light → System (it stayed
        // dark). TVColor is adaptive, so the window override flips the palette.
        // Re-render the whole tree when the in-app language changes so every
        // LocalizedStringKey re-resolves via the swizzled bundle — no relaunch.
        .id(lang.code)
        .onAppear { state.applyTheme(state.settings.theme) }
        .environment(\.locale, lang.code.isEmpty
            ? Locale.autoupdatingCurrent : Locale(identifier: lang.code))
        // NOTE: do NOT set a global .foregroundStyle here — it overrides the
        // system's prominent-button label contrast (made focused buttons white-on-
        // white). Each Text sets its own TVColor explicitly instead.
    }
}
