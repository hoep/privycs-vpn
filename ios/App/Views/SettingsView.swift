import SwiftUI
import PrivycsCore

struct SettingsView: View {
    @EnvironmentObject private var appState: AppState
    @State private var crashReportsEnabled = false
    @State private var theme = "system"
    @State private var dnsOverride = ""
    @State private var killSwitch = true
    @State private var appLanguage = ""

    private let languages: [(code: String, label: String)] = [
        ("", "System"), ("en", "English"), ("de", "Deutsch"),
        ("es", "Español"), ("fr", "Français"), ("it", "Italiano"), ("pt", "Português"),
    ]

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
                            .onSubmit { persistDNS(dnsOverride) }
                    }
                    NavigationLink {
                        GatewaySettingsView().environmentObject(appState)
                    } label: {
                        LabeledContent("Privycs Gateway",
                            value: appState.settings.gatewayURL.isEmpty ? "Not set" : "Configured")
                    }
                }

                Section("Appearance") {
                    Picker("Theme", selection: $theme) {
                        Text("System").tag("system")
                        Text("Dark").tag("dark")
                        Text("Light").tag("light")
                    }
                    .onChange(of: theme) { _, new in persistTheme(new) }

                    Picker("Language", selection: $appLanguage) {
                        ForEach(languages, id: \.code) { Text($0.label).tag($0.code) }
                    }
                    .onChange(of: appLanguage) { _, new in persistLanguage(new) }
                }

                Section("Automation") {
                    NavigationLink {
                        NetworkRulesView().environmentObject(appState)
                    } label: { Text("On-Demand & Network Rules") }
                }

                Section("Backup") {
                    NavigationLink {
                        BackupView().environmentObject(appState)
                    } label: { Text("Backup & Restore") }
                }

                Section("Pro") {
                    NavigationLink(destination: ProUpgradeView().environmentObject(appState)) {
                        Text("Upgrade to Pro")
                    }
                }

                Section("Diagnostics") {
                    NavigationLink { LogsView() } label: { Text("Logs") }
                    NavigationLink { OssLicensesView() } label: { Text("Open Source Licenses") }
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
                appLanguage = appState.settings.appLanguage
            }
        }
    }

    private func persistDNS(_ v: String) {
        var s = appState.settings
        s.dnsOverride = v
        appState.settings = s
        Task { try? await appState.settingsRepo.save(s) }
    }

    private func persistLanguage(_ v: String) {
        var s = appState.settings
        s.appLanguage = v
        appState.settings = s
        Task { try? await appState.settingsRepo.save(s) }
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
