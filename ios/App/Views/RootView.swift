import SwiftUI

/// Tab-Bar-Container — die 5 Top-Level-Screens analog Android-
/// Bottom-Nav (Connect / Configs / Add / Settings / Help).
struct RootView: View {
    @EnvironmentObject private var appState: AppState
    @State private var tab: Tab = .connect

    enum Tab: Hashable {
        case connect, connections, add, settings, help
    }

    var body: some View {
        TabView(selection: $tab) {
            ConnectionView()
                .tabItem { Label("Connect", systemImage: "shield.checkered") }
                .tag(Tab.connect)

            ConnectionsView()
                .tabItem { Label("Configs", systemImage: "list.bullet") }
                .tag(Tab.connections)

            AddConnectionView()
                .tabItem { Label("Add", systemImage: "plus.circle") }
                .tag(Tab.add)

            SettingsView()
                .tabItem { Label("Settings", systemImage: "gear") }
                .tag(Tab.settings)

            HelpView()
                .tabItem { Label("Help", systemImage: "questionmark.circle") }
                .tag(Tab.help)
        }
    }
}
