import SwiftUI
import PrivycsCore

struct SettingsView: View {
    @EnvironmentObject private var appState: AppState
    @State private var crashReportsEnabled = false
    @State private var theme = "system"
    @State private var dnsOverride = ""
    @State private var killSwitch = true
    @State private var appLanguage = ""
    @State private var tunnelHealthMode = "auto"
    @State private var tunnelHealthTarget = ""
    @State private var tunnelHealthInterval = 0
    @State private var tunnelHealthThreshold = 0

    private let languages: [(code: String, label: String)] = [
        ("", "System"), ("en", "English"), ("de", "Deutsch"),
        ("es", "Español"), ("fr", "Français"), ("it", "Italiano"), ("pt", "Português"),
    ]

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Toggle("Kill Switch", isOn: $killSwitch)
                        .onChange(of: killSwitch) { _, new in persistKillSwitch(new) }
                    Toggle("Anonymous crash reports", isOn: $crashReportsEnabled)
                        .onChange(of: crashReportsEnabled) { _, new in
                            persistCrashReports(new)
                        }
                } header: {
                    Text("Privacy")
                } footer: {
                    Text("Kill Switch routes IPv6 into the tunnel so a v4-only server can't leak your real IPv6 address. Turning it off allows IPv6 traffic outside the VPN.")
                }

                Section {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("DNS Override").font(.subheadline)
                        DnsField(value: $dnsOverride, onCommit: { persistDNS(dnsOverride) })
                    }
                    NavigationLink {
                        ProtocolFailoverView().environmentObject(appState)
                    } label: { Text("Protocol Failover Order") }
                    NavigationLink {
                        GatewaySettingsView().environmentObject(appState)
                    } label: {
                        LabeledContent("Privycs Gateway",
                            value: appState.settings.gatewayURL.isEmpty ? "Not set" : "Configured")
                    }
                } header: {
                    Text("Connection")
                } footer: {
                    Text("DNS servers used while connected (WireGuard / AmneziaWG / OpenVPN). Per-connection and per-pool overrides take precedence.")
                }

                Section {
                    Picker("Health monitoring", selection: $tunnelHealthMode) {
                        Text("Auto").tag("auto")
                        Text("Always on").tag("always")
                        Text("Off").tag("off")
                    }
                    .onChange(of: tunnelHealthMode) { _, _ in persistHealth() }
                    if tunnelHealthMode != "off" {
                        LabeledContent("Ping target") {
                            TextField("1.1.1.1", text: $tunnelHealthTarget)
                                .multilineTextAlignment(.trailing)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                                .onSubmit { persistHealth() }
                        }
                        Picker("Check interval", selection: $tunnelHealthInterval) {
                            Text("Default (10s)").tag(0)
                            Text("5 seconds").tag(5)
                            Text("10 seconds").tag(10)
                            Text("30 seconds").tag(30)
                            Text("60 seconds").tag(60)
                        }
                        .onChange(of: tunnelHealthInterval) { _, _ in persistHealth() }
                        Picker("Mark dead after", selection: $tunnelHealthThreshold) {
                            Text("Default (3)").tag(0)
                            Text("2 misses").tag(2)
                            Text("3 misses").tag(3)
                            Text("5 misses").tag(5)
                        }
                        .onChange(of: tunnelHealthThreshold) { _, _ in persistHealth() }
                    }
                } header: {
                    Text("Tunnel Health")
                } footer: {
                    Text("Probes a reachable target through the tunnel and shows a health pill on the Connect screen. Foreground-only on iOS.")
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
                    LabeledContent("Version", value: versionString)
                    Link("Privacy Policy", destination: URL(string: "https://www.privycs.com/docs/ios-client-privacy")!)
                    Link("Help", destination: URL(string: "https://www.privycs.com/docs/ios-client")!)
                }
            }
            .navigationTitle("Settings")
            .task {
                crashReportsEnabled = appState.settings.crashReportsEnabled
                theme = appState.settings.theme
                dnsOverride = appState.settings.dnsOverride
                killSwitch = appState.settings.killSwitchEnabled
                appLanguage = appState.settings.appLanguage
                tunnelHealthMode = appState.settings.tunnelHealthMode
                tunnelHealthTarget = appState.settings.tunnelHealthTarget
                tunnelHealthInterval = appState.settings.tunnelHealthPingIntervalSec
                tunnelHealthThreshold = appState.settings.tunnelHealthDeadThreshold
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
        // Apply immediately (swaps the .lproj + re-renders the whole UI).
        LanguageManager.shared.set(v)
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

    /// "1.0.8 (34)" — marketing version + CFBundleVersion build number.
    private var versionString: String {
        let build = Bundle.main.infoDictionary?["CFBundleVersion"] as? String ?? ""
        return build.isEmpty ? PrivycsCoreInfo.version : "\(PrivycsCoreInfo.version) (\(build))"
    }

    private func persistKillSwitch(_ v: Bool) {
        var s = appState.settings
        s.killSwitchEnabled = v
        appState.settings = s
        Task { try? await appState.settingsRepo.save(s) }
    }

    private func persistHealth() {
        var s = appState.settings
        s.tunnelHealthMode = tunnelHealthMode
        s.tunnelHealthTarget = tunnelHealthTarget.trimmingCharacters(in: .whitespaces)
        s.tunnelHealthPingIntervalSec = tunnelHealthInterval
        s.tunnelHealthDeadThreshold = tunnelHealthThreshold
        appState.settings = s   // immediate — the running monitor reads settings live
        Task { try? await appState.settingsRepo.save(s) }
    }
}
