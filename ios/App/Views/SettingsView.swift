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
    /// Smart Decision Engine "Automatic protocol selection" (v1.0.9). A local
    /// display preference (UserDefaults) that hides the manual failover order
    /// and shows the live decision panel. The engine observes in shadow mode
    /// regardless; this toggle only controls the Settings UI — kept out of the
    /// cross-platform Codable AppSettings to avoid a strict-decoder migration.
    @AppStorage("auto_protocol_selection") private var autoProtocolSelection = false

    private let languages: [(code: String, label: String)] = [
        ("", "System"), ("en", "English"), ("de", "Deutsch"),
        ("es", "Español"), ("fr", "Français"), ("it", "Italiano"), ("pt", "Português"),
    ]

    var body: some View {
        AdaptiveNavStack {
            Form {
                Section {
                    Toggle("Kill Switch", isOn: $killSwitch)
                        .onChange(of: killSwitch) { new in persistKillSwitch(new) }
                    Toggle("Anonymous crash reports", isOn: $crashReportsEnabled)
                        .onChange(of: crashReportsEnabled) { new in
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
                    Toggle("Automatic protocol selection", isOn: $autoProtocolSelection)
                    if autoProtocolSelection {
                        NavigationLink {
                            EngineDecisionsView().environmentObject(appState)
                        } label: { Text("Engine decisions") }
                    } else {
                        NavigationLink {
                            ProtocolFailoverView().environmentObject(appState)
                        } label: { Text("Protocol Failover Order") }
                    }
                    NavigationLink {
                        GatewaySettingsView().environmentObject(appState)
                    } label: {
                        LabeledRow("Privycs Gateway") {
                            // Text(LocalizedStringKey) localizes; the value:
                            // String overload was shown verbatim (English).
                            Text(appState.settings.gatewayURL.isEmpty ? "Not set" : "Configured")
                        }
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
                    .onChange(of: tunnelHealthMode) { _ in persistHealth() }
                    if tunnelHealthMode != "off" {
                        LabeledRow("Ping target") {
                            TextField("1.1.1.1", text: $tunnelHealthTarget)
                                .multilineTextAlignment(.trailing)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                                // Persist on every change (not just on Return)
                                // — typing then navigating away used to drop it.
                                .onChange(of: tunnelHealthTarget) { _ in persistHealth() }
                                .onSubmit { persistHealth() }
                        }
                        Picker("Check interval", selection: $tunnelHealthInterval) {
                            Text("Default (10s)").tag(0)
                            Text("5 seconds").tag(5)
                            Text("10 seconds").tag(10)
                            Text("30 seconds").tag(30)
                            Text("60 seconds").tag(60)
                        }
                        .onChange(of: tunnelHealthInterval) { _ in persistHealth() }
                        Picker("Mark dead after", selection: $tunnelHealthThreshold) {
                            Text("Default (3)").tag(0)
                            Text("2 misses").tag(2)
                            Text("3 misses").tag(3)
                            Text("5 misses").tag(5)
                        }
                        .onChange(of: tunnelHealthThreshold) { _ in persistHealth() }
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
                    .onChange(of: theme) { new in persistTheme(new) }

                    Picker("Language", selection: $appLanguage) {
                        ForEach(languages, id: \.code) { Text($0.label).tag($0.code) }
                    }
                    .onChange(of: appLanguage) { new in persistLanguage(new) }
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
                    NavigationLink {
                        SendToTVView().environmentObject(appState)
                    } label: { Text("Send to Apple TV") }
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
                    LabeledRow("Version", value: versionString)
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

    /// "1.0.9 (65)" — marketing version + CFBundleVersion build number, read
    /// from the app bundle (CFBundleShortVersionString = MARKETING_VERSION) so
    /// it never goes stale against a hard-coded constant. Falls back to the
    /// PrivycsCore constant only if the Info.plist value is missing.
    private var versionString: String {
        let info = Bundle.main.infoDictionary
        let marketing = (info?["CFBundleShortVersionString"] as? String) ?? PrivycsCoreInfo.version
        let build = info?["CFBundleVersion"] as? String ?? ""
        return build.isEmpty ? marketing : "\(marketing) (\(build))"
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
