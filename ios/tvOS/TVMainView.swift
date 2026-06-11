import SwiftUI
import PrivycsCore

/// Apple TV connect screen — mirrors the iPhone/iPad ConnectionView look (Privycs
/// design system: teal on the dark command-console ramp, card surfaces) adapted
/// to focus/remote navigation. The Connect control takes default focus.
struct TVMainView: View {
    @EnvironmentObject private var state: TVAppState
    @Namespace private var focusNS

    var body: some View {
        ScrollView {
            VStack(spacing: 28) {
                statusHero
                connectButton
                    .prefersDefaultFocus(in: focusNS)
                if state.status.connected {
                    trafficCards
                    detailsCard
                }
                serverSection
                Button(role: .destructive) {
                    Task { await state.unenroll() }
                } label: {
                    Label(String(localized: "tv.main.unlink"), systemImage: "xmark.circle")
                        .font(.system(size: 20, weight: .medium))
                }
                .padding(.top, 4)
            }
            .padding(.horizontal, 80)
            .padding(.vertical, 50)
            .frame(maxWidth: .infinity)
        }
        .focusScope(focusNS)
    }

    // MARK: — Status hero

    private var statusHero: some View {
        let connected = state.status.connected
        return VStack(spacing: 12) {
            ZStack {
                Circle()
                    .fill((connected ? TVColor.teal : TVColor.onSurfaceVariant).opacity(0.14))
                    .frame(width: 150, height: 150)
                Image(systemName: connected ? "lock.shield.fill" : "shield.slash.fill")
                    .font(.system(size: 68))
                    .foregroundStyle(connected ? TVColor.teal : TVColor.onSurfaceVariant)
            }
            Text(connected ? String(localized: "tv.status.connected")
                           : String(localized: "tv.status.disconnected"))
                .font(.system(size: 34, weight: .bold))
                .foregroundStyle(connected ? TVColor.teal : TVColor.onSurface)
            if connected {
                HStack(spacing: 10) {
                    if let p = state.status.activeProtocol {
                        Text(p.displayName)
                            .font(.system(size: 19, weight: .semibold))
                            .padding(.horizontal, 12).padding(.vertical, 5)
                            .background(TVColor.teal.opacity(0.18), in: Capsule())
                            .foregroundStyle(TVColor.teal)
                    }
                    if state.status.uptime > 0 {
                        Text(formatUptime(state.status.uptime))
                            .font(.system(size: 19, weight: .medium)).monospacedDigit()
                            .foregroundStyle(TVColor.onSurfaceVariant)
                    }
                }
            } else if let sel = state.selectedConfig {
                Text(sel.name).font(.system(size: 21))
                    .foregroundStyle(TVColor.onSurfaceVariant)
            }
        }
    }

    // MARK: — Connect

    private var connectButton: some View {
        Button {
            Task { await state.toggle() }
        } label: {
            HStack(spacing: 12) {
                if state.connecting {
                    ProgressView()
                } else {
                    Image(systemName: state.status.connected ? "stop.fill" : "bolt.fill")
                }
                Text(state.status.connected ? String(localized: "tv.action.disconnect")
                                            : String(localized: "tv.action.connect"))
                    .font(.system(size: 26, weight: .bold))
            }
            .frame(minWidth: 380)
            .padding(.vertical, 6)
            .foregroundStyle(state.status.connected ? TVColor.onSurface : TVColor.background)
        }
        .tint(state.status.connected ? TVColor.surfaceVariant : TVColor.teal)
        .disabled(state.connecting || state.selectedConfig == nil)
    }

    // MARK: — Traffic

    private var trafficCards: some View {
        HStack(spacing: 20) {
            statCard(title: String(localized: "tv.stats.download", defaultValue: "Download"), icon: "arrow.down",
                     total: state.status.rxBytes, speed: state.rxSpeed,
                     history: state.rxHistory, tint: TVColor.teal)
            statCard(title: String(localized: "tv.stats.upload", defaultValue: "Upload"), icon: "arrow.up",
                     total: state.status.txBytes, speed: state.txSpeed,
                     history: state.txHistory, tint: TVColor.tealBright)
        }
        .frame(maxWidth: 860)
    }

    private func statCard(title: String, icon: String, total: Int64, speed: Double,
                          history: [Double], tint: Color) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: icon).foregroundStyle(tint)
                Text(title).font(.system(size: 21, weight: .semibold))
                    .foregroundStyle(TVColor.onSurface)
                Spacer()
                Text(formatSpeed(speed)).font(.system(size: 18, weight: .medium)).monospacedDigit()
                    .foregroundStyle(TVColor.onSurfaceVariant)
            }
            Text(formatBytes(total)).font(.system(size: 30, weight: .bold)).monospacedDigit()
                .foregroundStyle(TVColor.onSurface)
            Sparkline(values: history, tint: tint).frame(height: 38)
        }
        .padding(22)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(TVColor.surface, in: RoundedRectangle(cornerRadius: 20))
        .overlay(RoundedRectangle(cornerRadius: 20).stroke(TVColor.outline, lineWidth: 1))
    }

    private var detailsCard: some View {
        VStack(spacing: 0) {
            detailRow(String(localized: "tv.detail.endpoint", defaultValue: "Endpoint"),
                      state.status.serverEndpoint)
            if !state.status.localAddress.isEmpty {
                Divider().background(TVColor.outline)
                detailRow(String(localized: "tv.detail.vpn_ip", defaultValue: "VPN IP"),
                          state.status.localAddress)
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

    // MARK: — Server picker (horizontal, focusable)

    private var serverSection: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("tv.main.servers", tableName: nil)
                    .font(.system(size: 24, weight: .bold)).foregroundStyle(TVColor.onSurface)
                Spacer()
                Button { Task { await state.refreshConfigs() } } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .accessibilityLabel(String(localized: "tv.main.refresh"))
            }

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
                if selected {
                    Image(systemName: "checkmark.circle.fill").foregroundStyle(TVColor.teal)
                }
            }
            Text(entry.name).font(.system(size: 21, weight: .semibold)).lineLimit(1)
                .foregroundStyle(TVColor.onSurface)
            if !country.isEmpty {
                Text(country).font(.system(size: 17)).foregroundStyle(TVColor.onSurfaceVariant).lineLimit(1)
            }
            Text(entry.protocol.displayName)
                .font(.system(size: 15, weight: .bold))
                .padding(.horizontal, 10).padding(.vertical, 4)
                .background(TVColor.teal.opacity(0.18), in: Capsule())
                .foregroundStyle(TVColor.teal)
        }
        .padding(18)
        .frame(width: 240, alignment: .leading)
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

/// Minimal throughput sparkline for tvOS (the phone's TransferStatsCard lives in
/// the iOS app target, not shared).
private struct Sparkline: View {
    let values: [Double]
    let tint: Color
    var body: some View {
        GeometryReader { geo in
            let maxV = max(values.max() ?? 1, 1)
            let n = max(values.count, 1)
            Path { p in
                for (i, v) in values.enumerated() {
                    let x = geo.size.width * CGFloat(i) / CGFloat(max(n - 1, 1))
                    let y = geo.size.height * (1 - CGFloat(v / maxV))
                    if i == 0 { p.move(to: CGPoint(x: x, y: y)) }
                    else { p.addLine(to: CGPoint(x: x, y: y)) }
                }
            }
            .stroke(tint, style: StrokeStyle(lineWidth: 3, lineCap: .round, lineJoin: .round))
        }
    }
}
