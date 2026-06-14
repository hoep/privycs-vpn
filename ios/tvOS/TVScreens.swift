import SwiftUI
import PrivycsCore

// MARK: — Connect

/// Connect screen — dial (focusable connect control) on the left; live traffic +
/// connection details on the right (per the design mockup).
struct TVConnectScreen: View {
    @EnvironmentObject private var state: TVAppState

    private var discProtocol: VpnProtocol? { state.selectionProtocol }

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
                if let name = state.selectionName {
                    Text(name).font(TVFont.sans(22, .semibold)).foregroundStyle(TVColor.onSurfaceVariant).lineLimit(1)
                }
                protocolPills
            }
            .frame(maxWidth: .infinity)
            .focusSection()

            // RIGHT — pool status (if any) + traffic + details + health
            VStack(spacing: 20) {
                if state.status.connected, let pool = state.activePool {
                    TVPoolStatusCard(
                        poolName: pool.name, policy: pool.policy,
                        memberName: state.activePoolMember?.name ?? "",
                        memberCountry: state.activePoolMember?.country ?? "",
                        nextRotationAt: state.nextRotationAt,
                        onRotateNow: { Task { await state.rotatePool() } }
                    )
                }
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
                let flag = PoolHostnameLabels.flagEmoji(state.endpointCountry)
                detailRow(loc("tv.detail.endpoint"),
                          flag.isEmpty ? state.status.serverEndpoint : "\(flag)  \(state.status.serverEndpoint)")
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
    @State private var showImport = false
    @State private var detailPoolID: String?
    private let cols = [GridItem(.flexible(), spacing: 18), GridItem(.flexible(), spacing: 18)]

    private func deleteButton(_ action: @escaping () async -> Void) -> some View {
        Button(role: .destructive) { Task { await action() } } label: {
            Image(systemName: "trash").font(.system(size: 24)).foregroundStyle(TVColor.error).padding(18)
        }
        .buttonStyle(.card)
    }

    /// Change the selection and, IF a tunnel is already up, reconnect with the new
    /// config (switching config while connected should switch the tunnel).
    private func selectAndMaybeReconnect(_ select: () -> Void) {
        select()
        if state.status.connected { Task { await state.connectSelected() } }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            // Action row — import (manual config / restore, no gateway) + refresh.
            HStack(spacing: 16) {
                Button { showImport = true } label: {
                    Label(loc("tv.import.add"), systemImage: "plus.rectangle.on.rectangle")
                        .font(TVFont.sans(22, .semibold)).foregroundStyle(TVColor.teal)
                        .lineLimit(1).minimumScaleFactor(0.7)
                        .padding(.vertical, 12).padding(.horizontal, 22)
                }
                .buttonStyle(.card)
                Button { Task { await state.refreshConfigs() } } label: {
                    Label(loc("tv.main.refresh"), systemImage: "arrow.clockwise")
                        .font(TVFont.sans(22, .semibold)).foregroundStyle(TVColor.onSurface)
                        .lineLimit(1).minimumScaleFactor(0.7)
                        .padding(.vertical, 12).padding(.horizontal, 22)
                }
                .buttonStyle(.card)
            }

            // Locally-imported (manual) connections. Select + delete are SIBLING
            // buttons — tvOS can't focus a button nested inside another button.
            if !state.savedConnections.isEmpty {
                Text(loc("tv.configs.imported")).font(TVFont.mono(15)).tracking(2)
                    .foregroundStyle(TVColor.onSurfaceVariant).padding(.top, 6)
                VStack(spacing: 14) {
                    ForEach(state.savedConnections) { conn in
                        HStack(spacing: 12) {
                            Button { selectAndMaybeReconnect { state.selectSaved(conn.id) } } label: { savedRow(conn) }.buttonStyle(.card)
                            deleteButton { await state.deleteSaved(conn.id) }
                        }
                    }
                }
            }

            // Pools — full rotation engine (parity with the phone). Select /
            // configure / delete are sibling buttons.
            if !state.pools.isEmpty {
                Text(loc("tv.configs.pools")).font(TVFont.mono(15)).tracking(2)
                    .foregroundStyle(TVColor.onSurfaceVariant).padding(.top, 6)
                VStack(spacing: 14) {
                    ForEach(state.pools) { pool in
                        HStack(spacing: 12) {
                            Button { selectAndMaybeReconnect { state.selectPool(pool.id) } } label: { poolRow(pool) }.buttonStyle(.card)
                            Button { detailPoolID = pool.id } label: {
                                Image(systemName: "gearshape").font(.system(size: 24)).foregroundStyle(TVColor.teal).padding(18)
                            }.buttonStyle(.card)
                            deleteButton { await state.deletePool(pool.id) }
                        }
                    }
                }
            }

            // Gateway-pulled configs.
            if state.loadingConfigs && state.remoteConfigs.isEmpty {
                ProgressView().frame(maxWidth: .infinity, alignment: .center).padding(40)
            } else if let err = state.configError, state.remoteConfigs.isEmpty {
                Text(err).font(TVFont.sans(18)).foregroundStyle(TVColor.error)
            } else if state.remoteConfigs.isEmpty && state.savedConnections.isEmpty && state.pools.isEmpty {
                Text("tv.main.no_configs", tableName: nil).font(TVFont.sans(19)).foregroundStyle(TVColor.onSurfaceVariant)
            } else if !state.remoteConfigs.isEmpty {
                if !state.savedConnections.isEmpty {
                    Text(loc("tv.configs.gateway")).font(TVFont.mono(15)).tracking(2)
                        .foregroundStyle(TVColor.onSurfaceVariant).padding(.top, 6)
                }
                LazyVGrid(columns: cols, spacing: 18) {
                    ForEach(state.remoteConfigs) { entry in
                        Button { selectAndMaybeReconnect { state.selectGateway(entry.id) } } label: { configRow(entry) }
                            .buttonStyle(.card)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .focusSection()
        .sheet(isPresented: $showImport) { TVImportView().environmentObject(state) }
        .sheet(isPresented: Binding(get: { detailPoolID != nil },
                                    set: { if !$0 { detailPoolID = nil } })) {
            if let id = detailPoolID { TVPoolDetailView(poolID: id).environmentObject(state) }
        }
    }

    private func savedRow(_ conn: SavedConnection) -> some View {
        let selected = state.selectedSavedID == conn.id
        let active = conn.resolvedActiveConfig()
        let proto = active?.protocol ?? conn.activeProtocol ?? .wireguard
        let server = active?.serverAddress ?? ""
        return HStack(spacing: 16) {
            Image(tvProtocolAsset(proto)).renderingMode(.template).resizable().scaledToFit()
                .frame(width: 36, height: 36).foregroundStyle(tvProtocolColor(proto))
            VStack(alignment: .leading, spacing: 3) {
                Text(conn.name).font(TVFont.sans(20, .semibold)).foregroundStyle(TVColor.onSurface).lineLimit(1)
                if !server.isEmpty {
                    Text(server).font(TVFont.mono(14)).foregroundStyle(TVColor.onSurfaceVariant).lineLimit(1)
                }
            }
            Spacer()
            Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                .foregroundStyle(selected ? TVColor.teal : TVColor.onSurfaceVariant)
        }
        .frame(maxWidth: .infinity)
        .padding(18)
    }

    private func poolRow(_ pool: Pool) -> some View {
        let selected = state.selectedPoolID == pool.id
        return HStack(spacing: 16) {
            Image(systemName: "square.stack.3d.up.fill").font(.system(size: 30)).foregroundStyle(TVColor.teal)
                .frame(width: 36, height: 36)
            VStack(alignment: .leading, spacing: 3) {
                Text(pool.name).font(TVFont.sans(20, .semibold)).foregroundStyle(TVColor.onSurface).lineLimit(1)
                Text("\(pool.members.count) \(loc("tv.configs.servers"))")
                    .font(TVFont.mono(14)).foregroundStyle(TVColor.onSurfaceVariant).lineLimit(1)
            }
            Spacer()
            Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                .foregroundStyle(selected ? TVColor.teal : TVColor.onSurfaceVariant)
        }
        .frame(maxWidth: .infinity)
        .padding(18)
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
        VStack(alignment: .leading, spacing: 16) {
            TVToggleRow(title: loc("tv.settings.autoconnect"),
                        description: loc("tv.rules.autoconnect_hint"),
                        isOn: $autoConnect) { v in Task { await state.setAutoConnect(v) } }

            TVSettingsBlock(title: loc("tv.settings.wifi_rules"), description: loc("tv.rules.wifi_hint")) {
                TextField(loc("tv.settings.add_ssid"), text: $newSSID)
                    .font(TVFont.sans(21))
                    .onSubmit { Task { await state.addSSID(newSSID); newSSID = "" } }
                ForEach(state.onDemandSSIDs, id: \.self) { ssid in
                    HStack(spacing: 14) {
                        Image(systemName: "wifi").foregroundStyle(TVColor.teal)
                        Text(ssid).font(TVFont.sans(20)).foregroundStyle(TVColor.onSurface)
                        Spacer()
                        TVActionButton(title: "", icon: "trash", action: { Task { await state.removeSSID(ssid) } }, role: .destructive)
                    }
                    .padding(.top, 4)
                }
            }
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
    @State private var crashReports = true
    @State private var healthMode = "auto"
    @State private var healthTarget = ""
    @State private var healthInterval = 0
    @State private var healthThreshold = 0
    @State private var language = ""
    @State private var theme = "system"
    @State private var showImport = false

    private var languageOptions: [(value: String, label: String)] {
        [("", loc("tv.lang.system")), ("en", "EN"), ("de", "DE"),
         ("es", "ES"), ("fr", "FR"), ("it", "IT"), ("pt", "PT")]
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            // ── DNS Override (field + canonical presets) ──
            TVSettingsBlock(title: "DNS", description: loc("tv.settings.dns_hint2")) {
                TextField(loc("tv.settings.dns_placeholder"), text: $dns)
                    .font(TVFont.mono(21))
                    .onChange(of: dns) { _, v in
                        let t = v.trimmingCharacters(in: .whitespaces)
                        Task { await state.saveSettings { $0.dnsOverride = t } }
                    }
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 12) {
                        dnsChip(loc("tv.settings.dns_default"), value: "")
                        ForEach(DnsPresets.providers) { p in dnsChip(p.label, value: p.serversJoined) }
                    }
                }
            }

            // ── Tunnel Health ──
            TVSettingsBlock(title: loc("tv.hc.section"), description: loc("tv.hc.hint")) {
                TVSegmented(options: [("auto", loc("tv.hc.auto")), ("always", loc("tv.hc.always")), ("off", loc("tv.hc.off"))],
                            selection: $healthMode) { _ in persistHealth() }
                if healthMode != "off" {
                    TextField(loc("tv.hc.target_ph"), text: $healthTarget)
                        .font(TVFont.mono(21))
                        .onChange(of: healthTarget) { _, _ in persistHealth() }
                    HStack(spacing: 14) {
                        labeledSeg(loc("tv.hc.interval"),
                                   [(0, loc("tv.hc.default")), (5, "5s"), (10, "10s"), (30, "30s"), (60, "60s")],
                                   $healthInterval)
                    }
                    labeledSeg(loc("tv.hc.threshold"),
                               [(0, loc("tv.hc.default")), (2, "2"), (3, "3"), (5, "5")],
                               $healthThreshold)
                }
            }

            // ── Appearance: Theme + Language ──
            TVSettingsBlock(title: loc("tv.settings.theme")) {
                TVSegmented(options: [("system", loc("tv.theme.system")), ("dark", loc("tv.theme.dark")), ("light", loc("tv.theme.light"))],
                            selection: $theme) { v in
                    state.applyTheme(v)
                    Task { await state.saveSettings { $0.theme = v } }
                }
            }
            TVSettingsBlock(title: loc("tv.settings.language")) {
                TVSegmented(options: languageOptions, selection: $language) { v in
                    Task { await state.saveSettings { $0.appLanguage = v } }
                    TVLanguageManager.shared.set(v)
                }
            }

            // ── Backup & Restore (LAN import) ──
            TVSetRow(title: loc("tv.settings.backup_title"), description: loc("tv.settings.backup_desc")) {
                TVActionButton(title: loc("tv.import.add"), icon: "square.and.arrow.down") { showImport = true }
            }

            // ── Privacy ──
            TVToggleRow(title: loc("tv.settings.crash_reports"), isOn: $crashReports) { v in
                Task { await state.saveSettings { $0.crashReportsEnabled = v } }
            }

            // ── Account + version ──
            TVSetRow(title: loc("tv.settings.version")) {
                Text(PrivycsCoreInfo.version).font(TVFont.mono(21)).foregroundStyle(TVColor.onSurface)
            }
            TVSetRow(title: "Apple TV", description: state.settings.gatewayURL) {
                TVActionButton(title: loc("tv.main.unlink"), icon: "xmark.circle", action: { Task { await state.unenroll() } }, role: .destructive)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .focusSection()
        .sheet(isPresented: $showImport) { TVImportView().environmentObject(state) }
        .onAppear {
            dns = state.settings.dnsOverride
            crashReports = state.settings.crashReportsEnabled
            healthMode = state.settings.tunnelHealthMode
            healthTarget = state.settings.tunnelHealthTarget
            healthInterval = state.settings.tunnelHealthPingIntervalSec
            healthThreshold = state.settings.tunnelHealthDeadThreshold
            language = state.settings.appLanguage
            theme = state.settings.theme
        }
    }

    private func dnsChip(_ label: String, value: String) -> some View {
        let on = dns.trimmingCharacters(in: .whitespaces) == value
        return Button { dns = value; Task { await state.saveSettings { $0.dnsOverride = value } } } label: {
            Text(label).font(TVFont.sans(18, .semibold))
                .foregroundStyle(on ? TVColor.teal : TVColor.onSurfaceVariant)
                .lineLimit(1)
                .padding(.vertical, 12).padding(.horizontal, 20)
        }
        .buttonStyle(.card)
    }

    @ViewBuilder private func labeledSeg(_ title: String, _ options: [(value: Int, label: String)], _ sel: Binding<Int>) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(TVFont.sans(17)).foregroundStyle(TVColor.onSurfaceVariant)
            TVSegmented(options: options, selection: sel) { _ in persistHealth() }
        }
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
