import SwiftUI
import PrivycsCore

struct SettingsView: View {
    @EnvironmentObject private var appState: AppState
    @State private var crashReportsEnabled = false
    @State private var theme = "system"
    @State private var dnsOverride = ""
    @State private var killSwitch = true

    var body: some View {
        NavigationStack {
            Form {
                Section("Privacy") {
                    Toggle("Kill Switch (always on)", isOn: $killSwitch)
                        .disabled(true) // iOS-Pflicht — wir lassen ihn nicht ausschalten
                    Toggle("Anonymous crash reports", isOn: $crashReportsEnabled)
                        .onChange(of: crashReportsEnabled) { _, new in
                            persistCrashReports(new)
                        }
                }

                Section("Connection") {
                    LabeledContent("DNS Override") {
                        TextField("1.1.1.1", text: $dnsOverride)
                            .multilineTextAlignment(.trailing)
                            .textInputAutocapitalization(.never)
                    }
                }

                Section("Appearance") {
                    Picker("Theme", selection: $theme) {
                        Text("System").tag("system")
                        Text("Dark").tag("dark")
                        Text("Light").tag("light")
                    }
                    .onChange(of: theme) { _, new in
                        persistTheme(new)
                    }
                }

                Section("Network Rules") {
                    NavigationLink(destination: Text("Network Rules screen — Phase 3")) {
                        Text("On-Demand & Network Rules")
                    }
                }

                Section("Pro") {
                    NavigationLink(destination: ProUpgradeView()) {
                        Text("Upgrade to Pro")
                    }
                }

                Section("About") {
                    LabeledContent("Version", value: PrivycsCoreInfo.version)
                    Link("Privacy Policy", destination: URL(string: "https://www.privycs.com/privacy")!)
                    Link("Help", destination: URL(string: "https://www.privycs.com/docs")!)
                }
            }
            .navigationTitle("Settings")
            .task {
                crashReportsEnabled = appState.settings.crashReportsEnabled
                theme = appState.settings.theme
                dnsOverride = appState.settings.dnsOverride
                killSwitch = appState.settings.killSwitchEnabled
            }
        }
    }

    private func persistCrashReports(_ v: Bool) {
        var s = appState.settings
        s.crashReportsEnabled = v
        Task { try? await appState.settingsRepo.save(s) }
    }

    private func persistTheme(_ v: String) {
        var s = appState.settings
        s.theme = v
        Task { try? await appState.settingsRepo.save(s) }
    }
}
