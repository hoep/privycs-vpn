import SwiftUI

/// `NavigationStack` on iOS/tvOS 16+, falling back to a `.stack`-styled
/// `NavigationView` on iOS 15 (where `NavigationStack` doesn't exist) so the app
/// reaches older iPads. Every call site uses navigation as a plain container —
/// there is no `path:` / `navigationDestination(for:)` anywhere in the app —
/// which is exactly what makes this a drop-in replacement.
struct AdaptiveNavStack<Content: View>: View {
    @ViewBuilder let content: () -> Content

    var body: some View {
        #if os(macOS)
        // macOS (14.0 floor) always has NavigationStack; the `.stack`
        // NavigationView style below is unavailable on macOS, so take the
        // modern path unconditionally.
        NavigationStack { content() }
        #else
        if #available(iOS 16, tvOS 16, *) {
            NavigationStack { content() }
        } else {
            NavigationView { content() }
                .navigationViewStyle(.stack)
        }
        #endif
    }
}
