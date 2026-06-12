import SwiftUI
import PrivycsCore

/// tvOS settings — a STANDARD Form so the toggles render + focus like native tvOS
/// toggles. Global DNS override + kill switch apply to EVERY protocol; plus crash
/// reports, unlink, and version. No on-demand / network-rules / file import.
struct TVSettingsView: View {
    @EnvironmentObject private var state: TVAppState
    @Environment(\.dismiss) private var dismiss

    @State private var dns = ""
    @State private var killSwitch = false
    @State private var crashReports = true

    var body: some View {
        NavigationStack {
          ZStack {
            TVColor.background.ignoresSafeArea()   // opaque — the cover was see-through
            Form {
                Section {
                    TextField(String(localized: "tv.settings.dns_placeholder", defaultValue: "DNS, e.g. 1.1.1.1, 9.9.9.9"),
                              text: $dns)
                        .onChange(of: dns) { _, v in
                            Task { await state.saveSettings { $0.dnsOverride = v.trimmingCharacters(in: .whitespaces) } }
                        }
                    Toggle(String(localized: "tv.settings.kill_switch", defaultValue: "Kill Switch"), isOn: $killSwitch)
                        .onChange(of: killSwitch) { _, v in
                            Task { await state.saveSettings { $0.killSwitchEnabled = v } }
                        }
                } header: {
                    Text(String(localized: "tv.settings.connection", defaultValue: "Connection"))
                } footer: {
                    Text(String(localized: "tv.settings.dns_hint",
                                defaultValue: "DNS is applied to every protocol. Empty = use the server's DNS. The kill switch forces IPv6 through the tunnel — leave it off on Apple TV unless your gateway supports IPv6."))
                }

                Section {
                    Toggle(String(localized: "tv.settings.crash_reports", defaultValue: "Anonymous crash reports"),
                           isOn: $crashReports)
                        .onChange(of: crashReports) { _, v in
                            Task { await state.saveSettings { $0.crashReportsEnabled = v } }
                        }
                } header: {
                    Text(String(localized: "tv.settings.privacy", defaultValue: "Privacy"))
                }

                Section {
                    Button(role: .destructive) {
                        Task { await state.unenroll(); dismiss() }
                    } label: {
                        Label(String(localized: "tv.main.unlink"), systemImage: "xmark.circle")
                    }
                }

                Section {
                    if !state.settings.gatewayURL.isEmpty {
                        LabeledContent(String(localized: "tv.settings.gateway", defaultValue: "Gateway"),
                                       value: state.settings.gatewayURL)
                    }
                    LabeledContent(String(localized: "tv.settings.version", defaultValue: "Version"),
                                   value: PrivycsCoreInfo.version)
                } header: {
                    Text(String(localized: "tv.settings.about", defaultValue: "About"))
                }
            }
          }
          .navigationTitle(String(localized: "tv.settings.title", defaultValue: "Settings"))
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button(String(localized: "tv.settings.done", defaultValue: "Done")) { dismiss() }
                }
            }
        }
        .onAppear {
            dns = state.settings.dnsOverride
            killSwitch = state.settings.killSwitchEnabled
            crashReports = state.settings.crashReportsEnabled
        }
    }
}
