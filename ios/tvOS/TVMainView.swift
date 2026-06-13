import SwiftUI
import PrivycsCore

/// Apple TV connect screen. Wide, no-scroll layout: when connected the disc sits
/// in the CENTER with the traffic counters on the left and the connection details
/// on the right. Server picker below. Privycs design system, focus/remote-friendly.
struct TVMainView: View {
    @EnvironmentObject private var state: TVAppState
    @State private var showSettings = false

    private var discProtocol: VpnProtocol? {
        state.status.connected ? state.status.activeProtocol : state.selectedConfig?.protocol
    }

    var body: some View {
        VStack(spacing: 22) {
            topBar
            if state.status.connected {
                HStack(alignment: .center, spacing: 32) {
                    leftColumn
                    centerColumn.frame(width: 340)
                    rightColumn
                }
                .frame(maxWidth: .infinity)
            } else {
                centerColumn
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 8)
            }
            serverSection
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 70)
        .padding(.vertical, 36)
        .fullScreenCover(isPresented: $showSettings) {
            TVSettingsView().environmentObject(state)
        }
    }

    private var topBar: some View {
        HStack(spacing: 16) {
            Image("ic_privycs_logo").resizable().scaledToFit().frame(width: 34, height: 34)
            Text("Privycs VPN").font(.system(size: 22, weight: .bold)).foregroundStyle(TVColor.onSurface)
            Spacer()
            // Connect lives in the top bar — that's the row whose buttons the remote
            // CAN reach reliably (centre-of-screen custom controls would not take
            // focus on tvOS). Primary action, so it's first + teal.
            // .card (not borderedProminent): borderedProminent renders white-on-
            // white when UNfocused on tvOS (invisible). .card always shows a visible
            // surface (same as the server cards) + lifts on focus; explicit label
            // colours keep them readable. Connect = teal to stand out.
            Button { Task { await state.toggle() } } label: {
                HStack(spacing: 8) {
                    if state.connecting { ProgressView() }
                    else { Image(systemName: state.status.connected ? "stop.fill" : "bolt.fill") }
                    Text(state.status.connected ? String(localized: "tv.action.disconnect", defaultValue: "Disconnect")
                                                : String(localized: "tv.action.connect", defaultValue: "Connect"))
                        .font(.system(size: 20, weight: .bold))
                }
                .foregroundStyle(state.status.connected ? TVColor.onSurface : TVColor.teal)
            }
            .buttonStyle(.card)
            Button { Task { await state.refreshConfigs() } } label: {
                Label(String(localized: "tv.main.refresh", defaultValue: "Refresh"), systemImage: "arrow.clockwise")
                    .font(.system(size: 19, weight: .semibold)).foregroundStyle(TVColor.onSurface)
            }
            .buttonStyle(.card)
            Button { showSettings = true } label: {
                Label(String(localized: "tv.settings.title", defaultValue: "Settings"), systemImage: "gearshape.fill")
                    .font(.system(size: 19, weight: .semibold)).foregroundStyle(TVColor.onSurface)
            }
            .buttonStyle(.card)
        }
        .focusSection()
    }

    // MARK: — Center (status + disc)

    private var centerColumn: some View {
        let connected = state.status.connected
        return VStack(spacing: 16) {
            Text(connected ? String(localized: "tv.status.connected")
                           : String(localized: "tv.status.disconnected"))
                .font(.system(size: 44, weight: .bold))   // SF Display ≥40pt
                .foregroundStyle(connected ? TVColor.teal : TVColor.onSurface)
                .shadow(color: connected ? TVColor.teal.opacity(0.4) : .clear, radius: 18)
            TVConnectDisc(connected: connected, connecting: state.connecting,
                          activeProtocol: discProtocol)
            if connected {
                HStack(spacing: 10) {
                    if state.status.uptime > 0 {
                        Text(formatUptime(state.status.uptime))
                            .font(.system(size: 19, weight: .medium)).monospacedDigit()
                            .foregroundStyle(TVColor.onSurfaceVariant)
                    }
                    if state.health != .none { TVHealthPill(level: state.health) }
                }
            } else if let sel = state.selectedConfig {
                Text(sel.name).font(.system(size: 20)).foregroundStyle(TVColor.onSurfaceVariant).lineLimit(1)
            }
        }
        // display-only now (Connect moved to the top bar) — no focusSection needed.
    }

    // MARK: — Left (traffic) / Right (details)

    private var leftColumn: some View {
        VStack(spacing: 16) {
            statCard(title: String(localized: "tv.stats.download", defaultValue: "Download"), icon: "arrow.down",
                     total: state.status.rxBytes, speed: state.rxSpeed,
                     history: state.rxHistory, tint: Color(red: 0.13, green: 0.77, blue: 0.37))
            statCard(title: String(localized: "tv.stats.upload", defaultValue: "Upload"), icon: "arrow.up",
                     total: state.status.txBytes, speed: state.txSpeed,
                     history: state.txHistory, tint: Color(red: 0.23, green: 0.51, blue: 0.96))
        }
        .frame(maxWidth: .infinity)
    }

    private var rightColumn: some View {
        VStack(spacing: 0) {
            if !state.status.localAddress.isEmpty {
                detailRow(String(localized: "tv.detail.vpn_ip", defaultValue: "VPN IP"), state.status.localAddress)
            }
            if !state.status.serverEndpoint.isEmpty {
                if !state.status.localAddress.isEmpty { Divider().background(TVColor.outline) }
                detailRow(String(localized: "tv.detail.endpoint", defaultValue: "Endpoint"), state.status.serverEndpoint)
            }
            if !state.status.lastHandshake.isEmpty {
                Divider().background(TVColor.outline)
                detailRow(String(localized: "tv.detail.handshake", defaultValue: "Last handshake"), state.status.lastHandshake)
            }
        }
        .padding(.horizontal, 24)
        .frame(maxWidth: .infinity)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 22))
        .overlay(RoundedRectangle(cornerRadius: 22).stroke(TVColor.outline.opacity(0.5), lineWidth: 1))
        .shadow(color: .black.opacity(0.25), radius: 16, y: 8)
    }

    private func statCard(title: String, icon: String, total: Int64, speed: Double,
                          history: [Double], tint: Color) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: icon).foregroundStyle(tint)
                Text(title).font(.system(size: 19, weight: .semibold)).foregroundStyle(TVColor.onSurface)
                Spacer()
                Text(formatSpeed(speed)).font(.system(size: 16, weight: .medium)).monospacedDigit()
                    .foregroundStyle(tint)
            }
            Text(formatBytes(total)).font(.system(size: 26, weight: .bold)).monospacedDigit()
                .foregroundStyle(TVColor.onSurface)
            TVSpeedSparkline(samples: history, tint: tint).frame(height: 34)
        }
        .padding(20)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 22))
        .overlay(RoundedRectangle(cornerRadius: 22).stroke(TVColor.outline.opacity(0.5), lineWidth: 1))
        .shadow(color: .black.opacity(0.25), radius: 16, y: 8)
    }

    private func detailRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).foregroundStyle(TVColor.onSurfaceVariant)
            Spacer()
            Text(value.isEmpty ? "—" : value).monospacedDigit().lineLimit(1).foregroundStyle(TVColor.onSurface)
        }
        .font(.system(size: 19, weight: .medium))
        .padding(.vertical, 14)
    }

    // MARK: — Server picker

    private var serverSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("tv.main.servers", tableName: nil)
                .font(.system(size: 22, weight: .bold)).foregroundStyle(TVColor.onSurface)
            if state.loadingConfigs && state.remoteConfigs.isEmpty {
                ProgressView().frame(maxWidth: .infinity, alignment: .center).padding()
            } else if let err = state.configError, state.remoteConfigs.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    Text("tv.main.load_failed", tableName: nil)
                        .font(.system(size: 20, weight: .semibold)).foregroundStyle(TVColor.error)
                    Text(err).font(.system(size: 17)).foregroundStyle(TVColor.onSurfaceVariant)
                }
            } else if state.remoteConfigs.isEmpty {
                Text("tv.main.no_configs", tableName: nil).font(.system(size: 18)).foregroundStyle(TVColor.onSurfaceVariant)
            } else {
                ScrollView(.horizontal, showsIndicators: false) {
                    LazyHStack(spacing: 18) {
                        ForEach(state.remoteConfigs) { entry in
                            Button { state.selectedConfigID = entry.id } label: { serverCard(entry) }
                                .buttonStyle(.card)
                        }
                    }
                    .padding(.vertical, 6)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .focusSection()
    }

    private func serverCard(_ entry: RemoteConfigEntry) -> some View {
        let cc = countryCode(for: entry)
        let country = PoolHostnameLabels.countryNameFromCode(cc)
        let selected = state.selectedConfig?.id == entry.id
        return VStack(alignment: .leading, spacing: 8) {
            HStack {
                // Protocol brand logo (brand colour) — replaces the flag/globe and
                // the old protocol pill: the protocol is the thing that varies.
                Image(tvProtocolAsset(entry.protocol))
                    .renderingMode(.template).resizable().scaledToFit()
                    .frame(width: 38, height: 38)
                    .foregroundStyle(tvProtocolColor(entry.protocol))
                Spacer()
                Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                    .foregroundStyle(selected ? TVColor.teal : TVColor.onSurfaceVariant)
            }
            Text(entry.name).font(.system(size: 20, weight: .semibold)).lineLimit(1).foregroundStyle(TVColor.onSurface)
            Text(country.isEmpty ? entry.protocol.displayName : country)
                .font(.system(size: 16)).foregroundStyle(TVColor.onSurfaceVariant).lineLimit(1)
            // Server/endpoint identifier so duplicate-named cards are distinguishable.
            let server = entry.interfaceName.isEmpty
                ? (entry.peerName.isEmpty ? entry.serverAddress : entry.peerName)
                : entry.interfaceName
            if !server.isEmpty {
                Text(server).font(.system(size: 13, design: .monospaced))
                    .foregroundStyle(TVColor.onSurfaceVariant).opacity(0.85).lineLimit(1)
            }
        }
        .padding(18)
        .frame(width: 240, alignment: .leading)
        .background(selected ? AnyShapeStyle(TVColor.teal.opacity(0.18)) : AnyShapeStyle(.ultraThinMaterial),
                    in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18)
            .stroke(selected ? TVColor.teal : TVColor.outline.opacity(0.4), lineWidth: selected ? 3 : 1))
        .shadow(color: .black.opacity(0.2), radius: 10, y: 4)
    }

    private func countryCode(for entry: RemoteConfigEntry) -> String {
        let name = entry.interfaceName.isEmpty ? entry.peerName : entry.interfaceName
        let parts = name.split(separator: "-")
        if let first = parts.first, first.count == 2 { return String(first).uppercased() }
        return ""
    }

    // MARK: — Formatting

    private func formatBytes(_ b: Int64) -> String {
        let u = ["B", "KB", "MB", "GB", "TB"]
        var v = Double(max(0, b)); var i = 0
        while v >= 1024 && i < u.count - 1 { v /= 1024; i += 1 }
        return i == 0 ? "\(Int(v)) \(u[i])" : String(format: "%.1f %@", v, u[i])
    }
    private func formatSpeed(_ bps: Double) -> String { formatBytes(Int64(bps)) + "/s" }
    private func formatUptime(_ s: Int64) -> String {
        let h = s / 3600, m = (s % 3600) / 60, sec = s % 60
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, sec) : String(format: "%02d:%02d", m, sec)
    }
}
