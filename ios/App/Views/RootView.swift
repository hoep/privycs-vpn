import SwiftUI

/// Top-level container for the 5 screens (Connect / Configs / Add / Settings /
/// Help). Adaptive:
///   • compact width (iPhone)  → bottom TabView (Android bottom-nav parity)
///   • regular width (iPad)    → NavigationSplitView: the same items as a left
///     sidebar, the selected screen in the detail column.
struct RootView: View {
    @EnvironmentObject private var appState: AppState
    @ObservedObject private var lang = LanguageManager.shared
    @Environment(\.horizontalSizeClass) private var hSize
    @State private var tab: Tab = .connect

    enum Tab: Hashable, CaseIterable {
        case connect, connections, add, settings, help

        var titleKey: LocalizedStringKey {
            switch self {
            case .connect:     return "tab.connect"
            case .connections: return "tab.configs"
            case .add:         return "tab.add"
            case .settings:    return "tab.settings"
            case .help:        return "tab.help"
            }
        }
        var systemImage: String {
            switch self {
            case .connect:     return "shield.checkered"
            case .connections: return "list.bullet"
            case .add:         return "plus.circle"
            case .settings:    return "gear"
            case .help:        return "questionmark.circle"
            }
        }
    }

    var body: some View {
        Group {
            if hSize == .regular {
                splitView
            } else {
                tabView
            }
        }
        // Re-render the whole tree (incl. tab bar / sidebar) when the in-app
        // language changes so every LocalizedStringKey re-resolves via the
        // swizzled bundle — no restart needed.
        .id(lang.code)
        .environment(\.locale, lang.code.isEmpty
            ? Locale.autoupdatingCurrent : Locale(identifier: lang.code))
    }

    // MARK: iPhone — bottom tabs

    private var tabView: some View {
        TabView(selection: $tab) {
            ForEach(Tab.allCases, id: \.self) { t in
                screen(for: t)
                    .tabItem { Label(t.titleKey, systemImage: t.systemImage) }
                    .tag(t)
            }
        }
    }

    // MARK: iPad — sidebar + detail

    private var splitView: some View {
        NavigationSplitView {
            List(Tab.allCases, id: \.self, selection: sidebarSelection) { t in
                Label(t.titleKey, systemImage: t.systemImage)
            }
            .navigationTitle("app.title")
            #if os(iOS)
            .listStyle(.sidebar)
            #endif
        } detail: {
            screen(for: tab)
        }
    }

    /// Single-selection sidebar binding that never clears to nil (a screen is
    /// always shown in the detail column).
    private var sidebarSelection: Binding<Tab?> {
        Binding(get: { tab }, set: { if let v = $0 { tab = v } })
    }

    @ViewBuilder private func screen(for tab: Tab) -> some View {
        switch tab {
        case .connect:     ConnectionView()
        case .connections: ConnectionsView()
        case .add:         AddConnectionView()
        case .settings:    SettingsView()
        case .help:        HelpView()
        }
    }
}
