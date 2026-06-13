import SwiftUI
import PrivycsCore

// MARK: — Connect

/// Connect screen — dial (focusable connect control) on the left; live traffic +
/// connection details on the right (per the design mockup).
struct TVConnectScreen: View {
    @EnvironmentObject private var state: TVAppState

    private var discProtocol: VpnProtocol? {
        state.status.connected ? state.status.activeProtocol : state.selectedConfig?.protocol
    }

    var body: some View {
        HStack(alignment: .center, spacing: 44) {
            // LEFT — dial (DISPLAY-ONLY) + a real, reachable Connect button below.
            // The dial must NOT be the button: a custom control with blur/shadow
            // won't take focus on tvOS. A simple-content .card button does.
            VStack(spacing: 22) {
                TVConnectDisc(connected: state.status.connected,
                              connecting: state.connecting, activeProtocol: discProtocol)
                Button { Task { await state.toggle() } } label: {
                    HStack(spacing: 14) {
                        if state.connecting { ProgressView() }
                        else { Image(systemName: state.status.connected ? "stop.fill" : "bolt.fill").font(.system(size: 28)) }
                        Text(state.status.connected ? loc("tv.action.disconnect")
                                                    : loc("tv.action.connect"))
                            .font(TVFont.sans(30, .bold)).lineLimit(1).minimumScaleFactor(0.7)
                    }
                    .foregroundStyle(state.status.connected ? TVColor.onSurface : TVColor.teal)
                    .frame(minWidth: 380).padding(.vertical, 18).padding(.horizontal, 36)
                }
                .buttonStyle(.card)
                if state.status.connected, state.status.uptime > 0 {
                    Text(TVFormat.uptime(state.status.uptime))
                        .font(TVFont.sans(40, .semibold)).monospacedDigit()
                        .foregroundStyle(TVColor.onSurface)
                }
                if let sel = state.selectedConfig {
                    Text(sel.name).font(TVFont.sans(22, .semibold)).foregroundStyle(TVColor.onSurfaceVariant).lineLimit(1)
                }
                protocolPills
            }
            .frame(maxWidth: .infinity)
            .focusSection()

            // RIGHT — traffic + details + health
            VStack(spacing: 20) {
                HStack(spacing: 20) {
                    trafficCard(loc("tv.stats.download"), "arrow.down",
                                state.status.rxBytes, state.rxSpeed, state.rxHistory,
                                Color(red: 0, green: 0.80, blue: 0.67))
                    trafficCard(loc("tv.stats.upload"), "arrow.up",
                                state.status.txBytes, state.txSpeed, state.txHistory,
                                Color(red: 0.37, green: 0.70, blue: 0.96))
                }
                detailsCard
                if state.health != .none, state.settings.tunnelHealthMode != "off" {
                    TVHealthPill(level: state.health)
                }
            }
            .frame(maxWidth: .infinity)
        }
        .frame(maxWidth: .infinity)
    }

    // Protocol pills — distinct protocols across the pulled configs (with counts),
    // like the mockup. Tapping one switches to that protocol's first server (and
    // reconnects if currently connected). Uses the REAL protocol logos.
    @ViewBuilder private var protocolPills: some View {
        let groups = Dictionary(grouping: state.remoteConfigs, by: { $0.protocol })
        let protos = groups.keys.sorted { $0.rawValue < $1.rawValue }
        if !protos.isEmpty {
            HStack(spacing: 14) {
                ForEach(protos, id: \.self) { p in
                    let active = discProtocol == p
                    Button {
                        if let first = groups[p]?.first {
                            state.selectedConfigID = first.id
                            if state.status.connected { Task { await state.connectSelected() } }
                        }
                    } label: {
                        HStack(spacing: 10) {
                            Image(tvProtocolAsset(p)).renderingMode(.template).resizable().scaledToFit()
                                .frame(width: 26, height: 26)
                            Text(p.displayName).font(TVFont.sans(19, .semibold))
                            Text("\(groups[p]?.count ?? 0)").font(TVFont.mono(17, .semibold))
                        }
                        .foregroundStyle(active ? TVColor.teal : TVColor.onSurfaceVariant)
                        .padding(.horizontal, 18).padding(.vertical, 12)
                    }
                    .buttonStyle(.card)
                }
            }
            .padding(.top, 8)
        }
    }

    private func trafficCard(_ title: String, _ icon: String, _ total: Int64, _ speed: Double,
                             _ history: [Double], _ tint: Color) -> some View {
        VStack(spacing: 10) {
            HStack(spacing: 10) {
                Image(systemName: icon).foregroundStyle(tint)
                Text(title).font(TVFont.sans(20, .semibold)).foregroundStyle(TVColor.onSurfaceVariant)
            }
            Text(TVFormat.bytes(total)).font(TVFont.sans(38, .bold)).monospacedDigit().foregroundStyle(TVColor.onSurface)
            Text(TVFormat.speed(speed)).font(TVFont.mono(16)).foregroundStyle(tint)
            TVSpeedSparkline(samples: history, tint: tint).frame(height: 40)
        }
        .padding(26)
        .frame(maxWidth: .infinity)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 22))
        .overlay(RoundedRectangle(cornerRadius: 22).stroke(TVColor.outline.opacity(0.5), lineWidth: 1))
    }

    private var detailsCard: some View {
        VStack(spacing: 0) {
            if !state.status.localAddress.isEmpty {
                detailRow(loc("tv.detail.vpn_ip"), state.status.localAddress)
            }
            if !state.status.serverEndpoint.isEmpty {
                if !state.status.localAddress.isEmpty { Divider().background(TVColor.outline) }
                detailRow(loc("tv.detail.endpoint"), state.status.serverEndpoint)
            }
            if !state.status.lastHandshake.isEmpty {
                Divider().background(TVColor.outline)
                detailRow(loc("tv.detail.handshake"), state.status.lastHandshake)
            }
        }
        .padding(.horizontal, 26)
        .frame(maxWidth: .infinity)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 22))
        .overlay(RoundedRectangle(cornerRadius: 22).stroke(TVColor.outline.opacity(0.5), lineWidth: 1))
    }

    private func detailRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).font(TVFont.sans(19)).foregroundStyle(TVColor.onSurfaceVariant)
            Spacer()
            Text(value.isEmpty ? "—" : value).font(TVFont.mono(19)).foregroundStyle(TVColor.onSurface).lineLimit(1)
        }
        .padding(.vertical, 16)
    }
}

// MARK: — Configs (server list)

struct TVConfigsScreen: View {
    @EnvironmentObject private var state: TVAppState
    private let cols = [GridItem(.flexible(), spacing: 18), GridItem(.flexible(), spacing: 18)]

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            if state.loadingConfigs && state.remoteConfigs.isEmpty {
                ProgressView().frame(maxWidth: .infinity, alignment: .center).padding(40)
            } else if let err = state.configError, state.remoteConfigs.isEmpty {
                Text(err).font(TVFont.sans(18)).foregroundStyle(TVColor.error)
            } else if state.remoteConfigs.isEmpty {
                Text("tv.main.no_configs", tableName: nil).font(TVFont.sans(19)).foregroundStyle(TVColor.onSurfaceVariant)
            } else {
                LazyVGrid(columns: cols, spacing: 18) {
                    ForEach(state.remoteConfigs) { entry in
                        Button { state.selectedConfigID = entry.id } label: { configRow(entry) }
                            .buttonStyle(.card)
                    }
                }
            }
            Button { Task { await state.refreshConfigs() } } label: {
                Label(loc("tv.main.refresh"), systemImage: "arrow.clockwise")
                    .font(TVFont.sans(24, .semibold)).foregroundStyle(TVColor.onSurface)
                    .lineLimit(1).minimumScaleFactor(0.7)
                    .padding(.vertical, 12).padding(.horizontal, 22)
            }
            .buttonStyle(.card)
            .padding(.top, 6)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .focusSection()
    }

    private func configRow(_ entry: RemoteConfigEntry) -> some View {
        let selected = state.selectedConfig?.id == entry.id
        let server = entry.interfaceName.isEmpty ? (entry.peerName.isEmpty ? entry.serverAddress : entry.peerName) : entry.interfaceName
        return HStack(spacing: 16) {
            Image(tvProtocolAsset(entry.protocol)).renderingMode(.template).resizable().scaledToFit()
                .frame(width: 36, height: 36).foregroundStyle(tvProtocolColor(entry.protocol))
            VStack(alignment: .leading, spacing: 3) {
                Text(entry.name).font(TVFont.sans(20, .semibold)).foregroundStyle(TVColor.onSurface).lineLimit(1)
                if !server.isEmpty {
                    Text(server).font(TVFont.mono(14)).foregroundStyle(TVColor.onSurfaceVariant).lineLimit(1)
                }
            }
            Spacer()
            Text(entry.protocol.displayName).font(TVFont.mono(13, .semibold))
                .padding(.horizontal, 11).padding(.vertical, 5)
                .background(tvProtocolColor(entry.protocol).opacity(0.18), in: RoundedRectangle(cornerRadius: 8))
                .foregroundStyle(tvProtocolColor(entry.protocol))
            Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                .foregroundStyle(selected ? TVColor.teal : TVColor.onSurfaceVariant)
        }
        .padding(18)
    }
}

// MARK: — Rules (always-on + WiFi SSIDs)

struct TVRulesScreen: View {
    @EnvironmentObject private var state: TVAppState
    @State private var autoConnect = false
    @State private var newSSID = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 22) {
            Toggle(isOn: $autoConnect) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(loc("tv.settings.autoconnect"))
                        .font(TVFont.sans(22, .semibold)).foregroundStyle(TVColor.onSurface)
                    Text(loc("tv.rules.autoconnect_hint"))
                        .font(TVFont.sans(16)).foregroundStyle(TVColor.onSurfaceVariant)
                }
            }
            .tint(TVColor.teal)
            .onChange(of: autoConnect) { _, v in Task { await state.setAutoConnect(v) } }
            .padding(22)
            .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 20))

            VStack(alignment: .leading, spacing: 14) {
                Text(loc("tv.settings.wifi_rules"))
                    .font(TVFont.sans(22, .semibold)).foregroundStyle(TVColor.onSurface)
                Text(loc("tv.rules.wifi_hint"))
                    .font(TVFont.sans(16)).foregroundStyle(TVColor.onSurfaceVariant)
                TextField(loc("tv.settings.add_ssid"), text: $newSSID)
                    .font(TVFont.sans(20))
                    .onSubmit { Task { await state.addSSID(newSSID); newSSID = "" } }
                ForEach(state.onDemandSSIDs, id: \.self) { ssid in
                    HStack {
                        Image(systemName: "wifi").foregroundStyle(TVColor.teal)
                        Text(ssid).font(TVFont.sans(19)).foregroundStyle(TVColor.onSurface)
                        Spacer()
                        Button(role: .destructive) { Task { await state.removeSSID(ssid) } } label: {
                            Image(systemName: "minus.circle.fill")
                        }.buttonStyle(.card)
                    }
                }
            }
            .padding(22)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 20))
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .focusSection()
        .onAppear { autoConnect = state.settings.autoConnectOnStart }
    }
}

// MARK: — Settings

struct TVSettingsScreen: View {
    @EnvironmentObject private var state: TVAppState
    @State private var dns = ""
    @State private var dnsPreset = ""        // DnsProvider.id, "" = server default / custom
    @State private var crashReports = true
    @State private var healthMode = "auto"
    @State private var healthTarget = ""
    @State private var healthInterval = 0
    @State private var healthThreshold = 0
    @State private var language = ""

    private let languages: [(code: String, label: String)] = [
        ("", ""), ("en", "English"), ("de", "Deutsch"),
        ("es", "Español"), ("fr", "Français"), ("it", "Italiano"), ("pt", "Português"),
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: 22) {
            // ── DNS (free-text + canonical presets) ──
            card {
                Text(loc("tv.settings.connection"))
                    .font(TVFont.sans(22, .semibold)).foregroundStyle(TVColor.onSurface)
                TextField(loc("tv.settings.dns_placeholder"), text: $dns)
                    .font(TVFont.mono(20))
                    .onChange(of: dns) { _, v in
                        let t = v.trimmingCharacters(in: .whitespaces)
                        Task { await state.saveSettings { $0.dnsOverride = t } }
                    }
                Picker(loc("tv.settings.dns_preset"), selection: $dnsPreset) {
                    Text(loc("tv.settings.dns_default")).tag("")
                    ForEach(DnsPresets.providers) { p in Text(p.label).tag(p.id) }
                }
                .onChange(of: dnsPreset) { _, id in
                    let v = DnsPresets.providers.first { $0.id == id }?.serversJoined ?? ""
                    if v != dns { dns = v }
                }
                Text(loc("tv.settings.dns_hint2"))
                    .font(TVFont.sans(15)).foregroundStyle(TVColor.onSurfaceVariant)
            }

            // ── Tunnel Health ──
            card {
                Text(loc("tv.hc.section"))
                    .font(TVFont.sans(22, .semibold)).foregroundStyle(TVColor.onSurface)
                Picker(loc("tv.hc.mode"), selection: $healthMode) {
                    Text(loc("tv.hc.auto")).tag("auto")
                    Text(loc("tv.hc.always")).tag("always")
                    Text(loc("tv.hc.off")).tag("off")
                }
                .onChange(of: healthMode) { _, _ in persistHealth() }
                if healthMode != "off" {
                    TextField(loc("tv.hc.target_ph"), text: $healthTarget)
                        .font(TVFont.mono(20))
                        .onChange(of: healthTarget) { _, _ in persistHealth() }
                    Picker(loc("tv.hc.interval"), selection: $healthInterval) {
                        Text(loc("tv.hc.int_default")).tag(0)
                        Text(loc("tv.hc.int_5")).tag(5)
                        Text(loc("tv.hc.int_10")).tag(10)
                        Text(loc("tv.hc.int_30")).tag(30)
                        Text(loc("tv.hc.int_60")).tag(60)
                    }
                    .onChange(of: healthInterval) { _, _ in persistHealth() }
                    Picker(loc("tv.hc.threshold"), selection: $healthThreshold) {
                        Text(loc("tv.hc.thr_default")).tag(0)
                        Text(loc("tv.hc.thr_2")).tag(2)
                        Text(loc("tv.hc.thr_3")).tag(3)
                        Text(loc("tv.hc.thr_5")).tag(5)
                    }
                    .onChange(of: healthThreshold) { _, _ in persistHealth() }
                }
                Text(loc("tv.hc.hint"))
                    .font(TVFont.sans(15)).foregroundStyle(TVColor.onSurfaceVariant)
            }

            // ── Appearance / Language ──
            card {
                Text(loc("tv.settings.appearance"))
                    .font(TVFont.sans(22, .semibold)).foregroundStyle(TVColor.onSurface)
                Picker(loc("tv.settings.language"), selection: $language) {
                    Text(loc("tv.lang.system")).tag("")
                    ForEach(languages.dropFirst(), id: \.code) { Text($0.label).tag($0.code) }
                }
                .onChange(of: language) { _, v in
                    Task { await state.saveSettings { $0.appLanguage = v } }
                    TVLanguageManager.shared.set(v)
                }
            }

            // ── Privacy ──
            Toggle(isOn: $crashReports) {
                Text(loc("tv.settings.crash_reports"))
                    .font(TVFont.sans(20)).foregroundStyle(TVColor.onSurface)
            }
            .tint(TVColor.teal)
            .onChange(of: crashReports) { _, v in Task { await state.saveSettings { $0.crashReportsEnabled = v } } }
            .padding(22).background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 20))

            HStack {
                Text(loc("tv.settings.version")).font(TVFont.sans(19)).foregroundStyle(TVColor.onSurfaceVariant)
                Spacer()
                Text(PrivycsCoreInfo.version).font(TVFont.mono(19)).foregroundStyle(TVColor.onSurface)
            }
            .padding(.horizontal, 22).padding(.vertical, 18)
            .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 20))

            Button(role: .destructive) { Task { await state.unenroll() } } label: {
                Label(loc("tv.main.unlink"), systemImage: "xmark.circle")
                    .font(TVFont.sans(24, .semibold))
                    .lineLimit(1).minimumScaleFactor(0.7)
                    .padding(.vertical, 12).padding(.horizontal, 22)
            }
            .buttonStyle(.card)
            .padding(.top, 4)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .focusSection()
        .onAppear {
            dns = state.settings.dnsOverride
            dnsPreset = DnsPresets.detect(dns)?.id ?? ""
            crashReports = state.settings.crashReportsEnabled
            healthMode = state.settings.tunnelHealthMode
            healthTarget = state.settings.tunnelHealthTarget
            healthInterval = state.settings.tunnelHealthPingIntervalSec
            healthThreshold = state.settings.tunnelHealthDeadThreshold
            language = state.settings.appLanguage
        }
    }

    @ViewBuilder private func card<C: View>(@ViewBuilder _ content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: 14) { content() }
            .padding(22).frame(maxWidth: .infinity, alignment: .leading)
            .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 20))
    }

    private func persistHealth() {
        let mode = healthMode, target = healthTarget.trimmingCharacters(in: .whitespaces)
        let interval = healthInterval, threshold = healthThreshold
        Task {
            await state.saveSettings {
                $0.tunnelHealthMode = mode
                $0.tunnelHealthTarget = target
                $0.tunnelHealthPingIntervalSec = interval
                $0.tunnelHealthDeadThreshold = threshold
            }
        }
    }
}
