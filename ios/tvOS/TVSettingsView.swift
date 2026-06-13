import SwiftUI
import PrivycsCore

/// tvOS settings — a STANDARD Form so the toggles render + focus like native tvOS
/// toggles. Global DNS override + kill switch apply to EVERY protocol; plus crash
/// reports, unlink, and version. No on-demand / network-rules / file import.
struct TVSettingsView: View {
    @EnvironmentObject private var state: TVAppState
    @Environment(\.dismiss) private var dismiss

    @State private var dns = ""
    @State private var crashReports = true
    @State private var autoConnect = false

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
                    Toggle(String(localized: "tv.settings.autoconnect", defaultValue: "Connect automatically (Always-on)"),
                           isOn: $autoConnect)
                        .onChange(of: autoConnect) { _, v in
                            Task { await state.setAutoConnect(v) }
                        }
                    // No kill-switch toggle on tvOS: it only forces IPv6 through the
                    // tunnel, which tvOS's v6 data plane can't carry → it blackholes
                    // v6 and kills internet/DNS. Always off here.
                } header: {
                    Text(String(localized: "tv.settings.connection", defaultValue: "Connection"))
                } footer: {
                    Text(String(localized: "tv.settings.dns_hint",
                                defaultValue: "Always-on keeps the VPN connected automatically on any network (WiFi or Ethernet), even after a reboot. DNS is applied to every protocol; empty = the server's DNS."))
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
            crashReports = state.settings.crashReportsEnabled
            autoConnect = state.settings.autoConnectOnStart
        }
    }
}
