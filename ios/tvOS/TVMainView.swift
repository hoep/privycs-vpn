import SwiftUI
import PrivycsCore

/// Apple TV connect screen — mirrors the iPhone/iPad ConnectionView (Privycs
/// design system) adapted to focus/remote navigation. Circular connect disc with
/// the protocol logo, bar-chart traffic, health + handshake, clear server picker.
struct TVMainView: View {
    @EnvironmentObject private var state: TVAppState
    @State private var showSettings = false

    /// Protocol shown on the disc: the live one when connected, else the picked
    /// server's protocol (so the disc previews what you're about to connect).
    private var discProtocol: VpnProtocol? {
        state.status.connected ? state.status.activeProtocol : state.selectedConfig?.protocol
    }

    var body: some View {
        ScrollView {
            VStack(spacing: 22) {
                topBar
                statusHero
                TVConnectDisc(connected: state.status.connected,
                              connecting: state.connecting,
                              activeProtocol: discProtocol) {
                    Task { await state.toggle() }
                }
                .disabled(state.connecting || state.selectedConfig == nil)

                if state.status.connected {
                    trafficCards
                    detailsCard
                }
                serverSection
            }
            .padding(.horizontal, 80)
            .padding(.vertical, 40)
            .frame(maxWidth: .infinity)
        }
        .fullScreenCover(isPresented: $showSettings) {
            TVSettingsView().environmentObject(state)
        }
    }

    private var topBar: some View {
        HStack(spacing: 16) {
            Image("ic_privycs_logo").resizable().scaledToFit().frame(width: 34, height: 34)
            Text("Privycs VPN").font(.system(size: 22, weight: .bold))
                .foregroundStyle(TVColor.onSurface)
            Spacer()
            // borderedProminent → teal fill + WHITE label (system-contrasted) so it
            // reads in BOTH light and dark, focused or not. Refresh lives here (not
            // in the server header) to keep the server row a clean focus target.
            Button { Task { await state.refreshConfigs() } } label: {
                Label(String(localized: "tv.main.refresh", defaultValue: "Refresh"),
                      systemImage: "arrow.clockwise")
                    .font(.system(size: 19, weight: .semibold))
            }
            .buttonStyle(.borderedProminent).tint(TVColor.teal)
            Button { showSettings = true } label: {
                Label(String(localized: "tv.settings.title", defaultValue: "Settings"),
                      systemImage: "gearshape.fill")
                    .font(.system(size: 19, weight: .semibold))
            }
            .buttonStyle(.borderedProminent).tint(TVColor.teal)
        }
        .focusSection()
    }

    // MARK: — Status hero

    private var statusHero: some View {
        let connected = state.status.connected
        return VStack(spacing: 12) {
            Text(connected ? String(localized: "tv.status.connected")
                           : String(localized: "tv.status.disconnected"))
                .font(.system(size: 34, weight: .bold))
                .foregroundStyle(connected ? TVColor.teal : TVColor.onSurface)
            if connected {
                HStack(spacing: 10) {
                    if state.status.uptime > 0 {
                        Text(formatUptime(state.status.uptime))
                            .font(.system(size: 19, weight: .medium)).monospacedDigit()
                            .foregroundStyle(TVColor.onSurfaceVariant)
                    }
                    if state.health != .none {
                        TVHealthPill(level: state.health)
                    }
                }
            } else if let sel = state.selectedConfig {
                Text(sel.name).font(.system(size: 21))
                    .foregroundStyle(TVColor.onSurfaceVariant)
            }
        }
    }

    // MARK: — Traffic (bar sparkline, green/blue — iOS parity)

    private var trafficCards: some View {
        HStack(spacing: 20) {
            statCard(title: String(localized: "tv.stats.download", defaultValue: "Download"), icon: "arrow.down",
                     total: state.status.rxBytes, speed: state.rxSpeed,
                     history: state.rxHistory, tint: Color(red: 0.13, green: 0.77, blue: 0.37))   // green-500 #22C55E
            statCard(title: String(localized: "tv.stats.upload", defaultValue: "Upload"), icon: "arrow.up",
                     total: state.status.txBytes, speed: state.txSpeed,
                     history: state.txHistory, tint: Color(red: 0.23, green: 0.51, blue: 0.96))   // blue-500 #3B82F6
        }
        .frame(maxWidth: 860)
    }

    private func statCard(title: String, icon: String, total: Int64, speed: Double,
                          history: [Double], tint: Color) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Image(systemName: icon).foregroundStyle(tint)
                Text(title).font(.system(size: 20, weight: .semibold))
                    .foregroundStyle(TVColor.onSurface)
                Spacer()
                Text(formatSpeed(speed)).font(.system(size: 17, weight: .medium)).monospacedDigit()
                    .foregroundStyle(tint)
            }
            Text(formatBytes(total)).font(.system(size: 28, weight: .bold)).monospacedDigit()
                .foregroundStyle(TVColor.onSurface)
            TVSpeedSparkline(samples: history, tint: tint).frame(height: 40)
        }
        .padding(22)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(TVColor.surface, in: RoundedRectangle(cornerRadius: 20))
        .overlay(RoundedRectangle(cornerRadius: 20).stroke(TVColor.outline, lineWidth: 1))
    }

    // MARK: — Connection details (VPN IP / Endpoint / Last handshake)

    private var detailsCard: some View {
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
        .frame(maxWidth: 860)
        .background(TVColor.surface, in: RoundedRectangle(cornerRadius: 20))
        .overlay(RoundedRectangle(cornerRadius: 20).stroke(TVColor.outline, lineWidth: 1))
    }

    private func detailRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).foregroundStyle(TVColor.onSurfaceVariant)
            Spacer()
            Text(value.isEmpty ? "—" : value).monospacedDigit().lineLimit(1)
                .foregroundStyle(TVColor.onSurface)
        }
        .font(.system(size: 21, weight: .medium))
        .padding(.vertical, 16)
    }

    // MARK: — Server picker (focusable; CLICK selects)

    private var serverSection: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("tv.main.servers", tableName: nil)
                .font(.system(size: 24, weight: .bold)).foregroundStyle(TVColor.onSurface)

            if state.loadingConfigs && state.remoteConfigs.isEmpty {
                ProgressView().frame(maxWidth: .infinity, alignment: .center).padding()
            } else if let err = state.configError, state.remoteConfigs.isEmpty {
                VStack(alignment: .leading, spacing: 8) {
                    Text("tv.main.load_failed", tableName: nil)
                        .font(.system(size: 21, weight: .semibold)).foregroundStyle(TVColor.error)
                    Text(err).font(.system(size: 18)).foregroundStyle(TVColor.onSurfaceVariant)
                }
            } else if state.remoteConfigs.isEmpty {
                Text("tv.main.no_configs", tableName: nil)
                    .font(.system(size: 19)).foregroundStyle(TVColor.onSurfaceVariant)
            } else {
                ScrollView(.horizontal, showsIndicators: false) {
                    LazyHStack(spacing: 20) {
                        ForEach(state.remoteConfigs) { entry in
                            Button { state.selectedConfigID = entry.id } label: {
                                serverCard(entry)
                            }
                            .buttonStyle(.card)
                        }
                    }
                    .padding(.vertical, 8)
                }
            }
            if let err = state.configError, !state.remoteConfigs.isEmpty {
                Text(err).font(.system(size: 18)).foregroundStyle(TVColor.error)
            }
        }
        .frame(maxWidth: 1100)
        .focusSection()
    }

    private func serverCard(_ entry: RemoteConfigEntry) -> some View {
        let cc = countryCode(for: entry)
        let flag = PoolHostnameLabels.flagEmoji(cc)
        let country = PoolHostnameLabels.countryNameFromCode(cc)
        let selected = state.selectedConfig?.id == entry.id
        return VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(flag.isEmpty ? "🌐" : flag).font(.system(size: 40))
                Spacer()
                Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                    .foregroundStyle(selected ? TVColor.teal : TVColor.onSurfaceVariant)
            }
            Text(entry.name).font(.system(size: 21, weight: .semibold)).lineLimit(1)
                .foregroundStyle(TVColor.onSurface)
            if !country.isEmpty {
                Text(country).font(.system(size: 17)).foregroundStyle(TVColor.onSurfaceVariant).lineLimit(1)
            }
            HStack(spacing: 6) {
                Image(tvProtocolAsset(entry.protocol))
                    .renderingMode(.template).resizable().scaledToFit()
                    .frame(width: 18, height: 18)
                Text(entry.protocol.displayName).font(.system(size: 15, weight: .bold))
            }
            .padding(.horizontal, 10).padding(.vertical, 4)
            .background(TVColor.teal.opacity(0.18), in: Capsule())
            .foregroundStyle(TVColor.teal)
        }
        .padding(18)
        .frame(width: 240, alignment: .leading)
        .background(selected ? TVColor.teal.opacity(0.12) : Color.clear, in: RoundedRectangle(cornerRadius: 16))
        .overlay(RoundedRectangle(cornerRadius: 16)
            .stroke(selected ? TVColor.teal : Color.clear, lineWidth: 3))
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
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, sec)
                     : String(format: "%02d:%02d", m, sec)
    }
}
