import SwiftUI
import PrivycsCore

/// Main Connect screen — port of the Android ConnectScreen.
/// Big glow-ring connect button, status pill, target picker
/// (connections + pools), multi-config protocol picker, live transfer
/// stats with sparklines, connection-detail rows, tunnel-health pill.
struct ConnectionView: View {
    @EnvironmentObject private var appState: AppState
    @State private var showPicker = false
    @State private var showConfigSheet = false
    @State private var showPauseSheet = false

    private var status: VpnStatus { appState.status }

    private var pillState: StatusPill.State {
        if status.connected { return .connected }
        if appState.connecting { return .connecting }
        return .disconnected
    }

    var body: some View {
        NavigationStack {
            ZStack {
                PrivycsColor.background.ignoresSafeArea()
                ScrollView {
                    VStack(spacing: 22) {
                        if appState.connections.isEmpty && appState.pools.isEmpty {
                            welcomeView
                        } else {
                            StatusPill(state: pillState)
                                .padding(.top, 8)

                            targetPicker

                            ConnectButton(
                                connected: status.connected,
                                connecting: appState.connecting,
                                activeProtocol: status.activeProtocol,
                                onTap: { Task { await appState.toggleConnection() } }
                            )
                            .padding(.vertical, 4)

                            pauseSection

                            if status.connected {
                                // Uptime — monospace line right under the button,
                                // analog Android ConnectScreen (not buried in the
                                // detail panel).
                                if status.uptime > 0 {
                                    Text(formatUptime(status.uptime))
                                        .font(.system(size: 16, weight: .medium).monospacedDigit())
                                        .foregroundStyle(PrivycsColor.onSurface)
                                }
                                if let pool = appState.activePool {
                                    PoolIndicatorCard(
                                        poolName: pool.name,
                                        policy: pool.policy,
                                        memberName: appState.activePoolMember?.name ?? "",
                                        memberCountry: appState.activePoolMember?.country ?? "",
                                        nextRotationAt: appState.nextRotationAt,
                                        onRotateNow: { Task { await appState.rotatePool() } }
                                    )
                                }
                                if hasMultipleConfigs { protocolBadgeRow }
                                locationLine
                                statsRow
                                connectionDetails
                                // Real tunnel-health pill — driven by the
                                // reachability monitor (TunnelHealthService),
                                // nil while probing/disabled so it stays hidden.
                                if let health = appState.tunnelHealth {
                                    TunnelHealthPill(health: health)
                                }
                            }

                            if let err = appState.connectError ?? (status.error.isEmpty ? nil : status.error) {
                                errorCard(err)
                            }
                        }
                        Spacer(minLength: 12)
                    }
                    .padding(.horizontal, 20)
                    .frame(maxWidth: .infinity)
                }
            }
            .navigationTitle("app.title")
            .navigationBarTitleDisplayMode(.inline)
        }
        .sheet(isPresented: $showConfigSheet) {
            MultiConfigPickerSheet()
                .environmentObject(appState)
        }
        .sheet(isPresented: $showPauseSheet) {
            ManualPauseSheet().environmentObject(appState)
        }
    }

    // MARK: Pause / resume

    /// When connected: a Pause button. When paused (tunnel down, timer
    /// running): a banner with a live countdown + Resume. (Android parity.)
    @ViewBuilder private var pauseSection: some View {
        if appState.isPaused {
            VStack(spacing: 8) {
                TimelineView(.periodic(from: .now, by: 1)) { _ in
                    Label(pausedLabel, systemImage: "pause.circle.fill")
                        .font(.system(size: 14, weight: .medium))
                        .foregroundStyle(PrivycsColor.warning)
                }
                Button { Task { await appState.resume() } } label: {
                    Label("Resume now", systemImage: "play.fill")
                        .font(.system(size: 14, weight: .semibold))
                }
                .buttonStyle(.borderedProminent)
                .tint(PrivycsColor.teal)
            }
            .padding(12)
            .frame(maxWidth: .infinity)
            .background(RoundedRectangle(cornerRadius: 12).fill(PrivycsColor.surface))
        } else if status.connected {
            Button { showPauseSheet = true } label: {
                Label("Pause", systemImage: "pause.circle")
                    .font(.system(size: 14, weight: .medium))
            }
            .buttonStyle(.bordered)
            .tint(.secondary)
        }
    }

    /// Countdown label for a timed pause; "Paused" for an indefinite one.
    private var pausedLabel: String {
        guard let until = appState.pausedUntil, until != .distantFuture else { return "Paused" }
        let secs = max(0, Int(until.timeIntervalSinceNow))
        if secs >= 3600 { return "Paused — resumes in \(secs / 3600)h \((secs % 3600) / 60)m" }
        if secs >= 60 { return "Paused — resumes in \(secs / 60)m \(secs % 60)s" }
        return "Paused — resumes in \(secs)s"
    }

    // MARK: Target picker (connections + pools)

    private var targetPicker: some View {
        Button {
            showPicker.toggle()
        } label: {
            HStack(spacing: 10) {
                if let p = appState.selectedPool {
                    Image(systemName: "circle.grid.3x3.fill").foregroundStyle(PrivycsColor.teal)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(p.name).font(.system(size: 15, weight: .semibold))
                        Text("\(p.policy.displayName) · \(p.members.count) servers")
                            .font(.system(size: 11)).foregroundStyle(.secondary)
                    }
                } else if let c = appState.selectedConnection {
                    Image(systemName: "shield.lefthalf.filled").foregroundStyle(PrivycsColor.teal)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(c.name).font(.system(size: 15, weight: .semibold))
                        if let proto = activeProto(c) {
                            Text(proto.displayName).font(.system(size: 11)).foregroundStyle(.secondary)
                        }
                    }
                } else {
                    Text("Select connection").font(.system(size: 15))
                }
                Spacer()
                Image(systemName: "chevron.up.chevron.down")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)
            .background(RoundedRectangle(cornerRadius: 12).fill(PrivycsColor.surface))
            .overlay(RoundedRectangle(cornerRadius: 12).stroke(PrivycsColor.outline.opacity(0.4), lineWidth: 0.5))
        }
        .buttonStyle(.plain)
        .foregroundStyle(PrivycsColor.onSurface)
        .disabled(status.connected || appState.connecting)
        .popover(isPresented: $showPicker, arrowEdge: .top) {
            pickerList
                .frame(minWidth: 300, minHeight: 220)
                .presentationCompactAdaptation(.popover)
        }
    }

    private var pickerList: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                if !appState.connections.isEmpty {
                    pickerHeader("Connections")
                    ForEach(appState.connections) { c in
                        pickerRow(
                            title: c.name,
                            subtitle: activeProto(c)?.displayName ?? "",
                            selected: appState.selectedTargetID == c.id
                                || (appState.selectedTargetID.isEmpty && appState.connections.first?.id == c.id),
                            accent: activeProto(c)?.brandColor ?? PrivycsColor.teal
                        ) {
                            appState.selectedTargetID = c.id
                            showPicker = false
                        }
                    }
                }
                if !appState.pools.isEmpty {
                    Divider().padding(.vertical, 4)
                    pickerHeader("Pools")
                    ForEach(appState.pools) { p in
                        pickerRow(
                            title: p.name,
                            subtitle: "\(p.policy.displayName) · \(p.members.count)",
                            selected: appState.selectedTargetID == "pool:\(p.id)",
                            accent: PrivycsColor.teal
                        ) {
                            appState.selectedTargetID = "pool:\(p.id)"
                            showPicker = false
                        }
                    }
                }
            }
            .padding(.vertical, 8)
        }
    }

    private func pickerHeader(_ t: String) -> some View {
        Text(t.uppercased())
            .font(.system(size: 11, weight: .semibold))
            .foregroundStyle(.secondary)
            .padding(.horizontal, 16)
            .padding(.top, 6).padding(.bottom, 2)
    }

    private func pickerRow(title: String, subtitle: String, selected: Bool, accent: Color, tap: @escaping () -> Void) -> some View {
        Button(action: tap) {
            HStack(spacing: 10) {
                Circle()
                    .fill(selected ? accent : Color.clear)
                    .overlay(Circle().stroke(accent.opacity(0.5), lineWidth: 1))
                    .frame(width: 10, height: 10)
                VStack(alignment: .leading, spacing: 1) {
                    Text(title).font(.system(size: 14, weight: .medium))
                    if !subtitle.isEmpty {
                        Text(subtitle).font(.system(size: 11)).foregroundStyle(.secondary)
                    }
                }
                Spacer()
            }
            .padding(.horizontal, 16).padding(.vertical, 9)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .foregroundStyle(PrivycsColor.onSurface)
    }

    // MARK: Multi-config protocol badge row

    private var hasMultipleConfigs: Bool {
        (appState.selectedConnection?.protocols.count ?? 0) > 1
    }

    private var protocolBadgeRow: some View {
        Button {
            showConfigSheet = true
        } label: {
            HStack(spacing: 6) {
                ForEach(uniqueProtocols, id: \.self) { p in
                    ProtocolBadge(proto: p)
                }
                Image(systemName: "chevron.right").font(.system(size: 10)).foregroundStyle(.secondary)
            }
        }
        .buttonStyle(.plain)
    }

    private var uniqueProtocols: [VpnProtocol] {
        guard let c = appState.selectedConnection else { return [] }
        var seen = Set<VpnProtocol>()
        return c.protocols.compactMap { seen.insert($0.protocol).inserted ? $0.protocol : nil }
    }

    // MARK: Stats

    // Flag + city, country line (Android parity). Pool path uses the
    // broadcast member country/name; single-connection path falls back to
    // the server country code + connection name (often "<cc>-<city3>-…").
    @ViewBuilder private var locationLine: some View {
        let cc = status.activeMemberCountry.isEmpty ? status.serverCountryCode : status.activeMemberCountry
        let labelName = status.activeMemberName.isEmpty ? status.connectionName : status.activeMemberName
        let flag = PoolHostnameLabels.flagEmoji(cc)
        let city = PoolHostnameLabels.cityFromHostname(labelName)
        let country = PoolHostnameLabels.countryNameFromCode(cc)
        let loc: String = {
            var s = flag.isEmpty ? "" : flag + "  "
            if !city.isEmpty && !country.isEmpty { s += "\(city), \(country)" }
            else if !city.isEmpty { s += city }
            else if !country.isEmpty { s += country }
            return s.trimmingCharacters(in: .whitespaces)
        }()
        if !loc.isEmpty {
            Text(loc).font(.system(size: 13)).foregroundStyle(.secondary)
        }
    }

    private var statsRow: some View {
        HStack(spacing: 12) {
            TransferStatsCard(
                title: "Download", icon: "arrow.down",
                totalBytes: status.rxBytes, speedBytesPerSec: appState.rxSpeed,
                history: appState.rxHistory, tint: Color(hex: 0x22C55E)   // green-500 (Android parity)
            )
            TransferStatsCard(
                title: "Upload", icon: "arrow.up",
                totalBytes: status.txBytes, speedBytesPerSec: appState.txSpeed,
                history: appState.txHistory, tint: Color(hex: 0x3B82F6)   // blue-500 (Android parity)
            )
        }
    }

    // Detail panel — analog Android ConnectionDetails: exactly VPN IP /
    // Endpoint / Last handshake, each conditional on a non-blank value,
    // monospace, comma-separated values split onto separate lines.
    // (Dropped the iOS-only "Protocol" + "Uptime" rows — Protocol is
    // already shown by the badges, uptime now has its own line.)
    private var connectionDetails: some View {
        VStack(spacing: 0) {
            if !status.localAddress.isEmpty {
                detailRow("VPN IP", value: status.localAddress)
            }
            if !status.serverEndpoint.isEmpty {
                detailRow("Endpoint", value: status.serverEndpoint)
            }
            if !status.lastHandshake.isEmpty {
                detailRow("Last handshake", value: status.lastHandshake)
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 6)
        .background(RoundedRectangle(cornerRadius: 12).fill(PrivycsColor.surface))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(PrivycsColor.outline.opacity(0.4), lineWidth: 0.5))
    }

    private func detailRow(_ label: String, value: String) -> some View {
        // Split comma-separated values (e.g. "10.0.0.2/32, fd00::2/128")
        // onto separate right-aligned lines — Android DetailRow behavior.
        let parts = value.split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        return HStack(alignment: .top) {
            Text(label).font(.system(size: 13)).foregroundStyle(.secondary)
            Spacer()
            VStack(alignment: .trailing, spacing: 2) {
                ForEach(parts, id: \.self) { part in
                    Text(part).font(.system(size: 13)).fontDesign(.monospaced)
                        .foregroundStyle(PrivycsColor.onSurface)
                        .lineLimit(1).truncationMode(.middle)
                }
            }
        }
        .padding(.vertical, 8)
    }

    private func errorCard(_ msg: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(PrivycsColor.error)
            Text(msg).font(.system(size: 13)).foregroundStyle(PrivycsColor.onSurface)
            Spacer()
        }
        .padding(14)
        .background(RoundedRectangle(cornerRadius: 12).fill(PrivycsColor.error.opacity(0.12)))
    }

    private var welcomeView: some View {
        VStack(spacing: 16) {
            Image("ic_privycs_logo")
                .resizable()
                .scaledToFit()
                .frame(width: 96, height: 96)
            Text("Welcome to Privycs VPN")
                .font(.system(size: 20, weight: .semibold))
                .foregroundStyle(PrivycsColor.onSurface)
            Text("Import a configuration to get started.")
                .font(.system(size: 14)).foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .padding(.top, 80)
    }

    // MARK: helpers

    private func activeProto(_ c: SavedConnection) -> VpnProtocol? {
        if let cfg = c.protocols.first(where: { $0.id == c.activeConfigID }) {
            return cfg.protocol
        }
        return c.protocols.first?.protocol
    }

    private func formatUptime(_ secs: Int64) -> String {
        let h = secs / 3600, m = (secs % 3600) / 60, s = secs % 60
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, s)
                     : String(format: "%02d:%02d", m, s)
    }
}

/// Bottom-sheet to pick the active ProtocolConfig when a connection
/// holds more than one (multi-config-per-protocol). Port of Android's
/// MultiConfigPickerSheet.
struct MultiConfigPickerSheet: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            List {
                if let c = appState.selectedConnection {
                    ForEach(c.protocols) { cfg in
                        HStack(spacing: 10) {
                            ProtocolBadge(proto: cfg.protocol)
                            VStack(alignment: .leading, spacing: 1) {
                                Text(cfg.nickname.isEmpty ? cfg.filename : cfg.nickname)
                                    .font(.system(size: 14, weight: .medium))
                                if !cfg.serverAddress.isEmpty {
                                    Text(cfg.serverAddress).font(.system(size: 11))
                                        .foregroundStyle(.secondary)
                                }
                            }
                            Spacer()
                            if cfg.id == c.activeConfigID {
                                Image(systemName: "checkmark.circle.fill")
                                    .foregroundStyle(PrivycsColor.teal)
                            }
                        }
                        .contentShape(Rectangle())
                        .onTapGesture {
                            Task { await selectConfig(cfg, in: c) }
                        }
                    }
                }
            }
            .navigationTitle("Choose protocol")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }

    private func selectConfig(_ cfg: ProtocolConfig, in c: SavedConnection) async {
        // Route through setActiveConfig so that picking a protocol on the
        // CONNECTED connection reconnects live with it (Android parity) —
        // not just persists the flag.
        await appState.setActiveConfig(connectionID: c.id, configID: cfg.id)
        dismiss()
    }
}
