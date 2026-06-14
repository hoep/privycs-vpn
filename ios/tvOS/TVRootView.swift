import SwiftUI
import PrivycsCore

/// Top-level router: not-enrolled → device-code/manual enrollment; enrolled →
/// the main connect screen.
struct TVRootView: View {
    @EnvironmentObject private var state: TVAppState
    // Observe the in-app language override so a Settings change re-renders the
    // whole tree immediately (mirrors the iOS RootView pattern).
    @ObservedObject private var lang = TVLanguageManager.shared

    private var themeScheme: ColorScheme? {
        switch state.settings.theme {
        case "dark":  return .dark
        case "light": return .light
        default:      return nil   // follow the system
        }
    }

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
        // Theme override (System/Dark/Light) — TVColor is adaptive, so this flips
        // the whole palette + gradients.
        .preferredColorScheme(themeScheme)
        // Re-render the whole tree when the in-app language changes so every
        // LocalizedStringKey re-resolves via the swizzled bundle — no relaunch.
        .id(lang.code)
        .environment(\.locale, lang.code.isEmpty
            ? Locale.autoupdatingCurrent : Locale(identifier: lang.code))
        // NOTE: do NOT set a global .foregroundStyle here — it overrides the
        // system's prominent-button label contrast (made focused buttons white-on-
        // white). Each Text sets its own TVColor explicitly instead.
    }
}
