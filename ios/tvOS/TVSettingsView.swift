import SwiftUI
import PrivycsCore

/// tvOS settings — the subset that makes sense in the living room: a GLOBAL DNS
/// override + kill switch applied to EVERY protocol (WG/AWG/OpenVPN) at connect,
/// crash reports, unlink, and version. No on-demand / network-rules / file import
/// (not applicable on a TV).
struct TVSettingsView: View {
    @EnvironmentObject private var state: TVAppState
    @Environment(\.dismiss) private var dismiss

    @State private var dns = ""
    @State private var killSwitch = false
    @State private var crashReports = true

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                HStack {
                    Text("tv.settings.title", tableName: nil)
                        .font(.system(size: 34, weight: .bold)).foregroundStyle(TVColor.onSurface)
                    Spacer()
                    Button { dismiss() } label: {
                        Label(String(localized: "tv.settings.done", defaultValue: "Done"),
                              systemImage: "checkmark")
                    }
                }

                // Connection — applies to ALL protocols.
                card {
                    Text("tv.settings.connection", tableName: nil)
                        .font(.system(size: 22, weight: .bold)).foregroundStyle(TVColor.onSurface)
                    Text(String(localized: "tv.settings.dns_hint",
                                defaultValue: "Custom DNS server(s), comma-separated. Applied to every protocol. Empty = use the server's DNS."))
                        .font(.system(size: 16)).foregroundStyle(TVColor.onSurfaceVariant)
                    TextField(String(localized: "tv.settings.dns_placeholder", defaultValue: "e.g. 1.1.1.1, 9.9.9.9"),
                              text: $dns)
                        .font(.system(size: 20))
                        .onChange(of: dns) { _, v in
                            Task { await state.saveSettings { $0.dnsOverride = v.trimmingCharacters(in: .whitespaces) } }
                        }
                    Toggle(isOn: $killSwitch) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("tv.settings.kill_switch", tableName: nil)
                                .font(.system(size: 20, weight: .medium)).foregroundStyle(TVColor.onSurface)
                            Text(String(localized: "tv.settings.kill_switch_hint",
                                        defaultValue: "Block traffic if the tunnel drops."))
                                .font(.system(size: 15)).foregroundStyle(TVColor.onSurfaceVariant)
                        }
                    }
                    .tint(TVColor.teal)
                    .onChange(of: killSwitch) { _, v in
                        Task { await state.saveSettings { $0.killSwitchEnabled = v } }
                    }
                }

                // Privacy
                card {
                    Toggle(isOn: $crashReports) {
                        Text("tv.settings.crash_reports", tableName: nil)
                            .font(.system(size: 20, weight: .medium)).foregroundStyle(TVColor.onSurface)
                    }
                    .tint(TVColor.teal)
                    .onChange(of: crashReports) { _, v in
                        Task { await state.saveSettings { $0.crashReportsEnabled = v } }
                    }
                }

                // Account / device
                card {
                    if !state.settings.gatewayURL.isEmpty {
                        labelRow(String(localized: "tv.settings.gateway", defaultValue: "Gateway"),
                                 state.settings.gatewayURL)
                    }
                    Button(role: .destructive) {
                        Task { await state.unenroll(); dismiss() }
                    } label: {
                        Label(String(localized: "tv.main.unlink"), systemImage: "xmark.circle")
                            .font(.system(size: 20, weight: .semibold))
                    }
                }

                // About
                card {
                    labelRow(String(localized: "tv.settings.version", defaultValue: "Version"),
                             PrivycsCoreInfo.version)
                }
            }
            .padding(.horizontal, 100)
            .padding(.vertical, 60)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .background(TVColor.background.ignoresSafeArea())
        .onAppear {
            dns = state.settings.dnsOverride
            killSwitch = state.settings.killSwitchEnabled
            crashReports = state.settings.crashReportsEnabled
        }
    }

    @ViewBuilder private func card<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 16) { content() }
            .padding(28)
            .frame(maxWidth: 1000, alignment: .leading)
            .background(TVColor.surface, in: RoundedRectangle(cornerRadius: 20))
            .overlay(RoundedRectangle(cornerRadius: 20).stroke(TVColor.outline, lineWidth: 1))
    }

    private func labelRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).foregroundStyle(TVColor.onSurfaceVariant)
            Spacer()
            Text(value).foregroundStyle(TVColor.onSurface).lineLimit(1)
        }
        .font(.system(size: 19, weight: .medium))
    }
}
