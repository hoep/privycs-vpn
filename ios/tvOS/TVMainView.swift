import SwiftUI
import PrivycsCore

/// Main connect screen for Apple TV. Left: a focusable list of gateway-pulled
/// servers (flag + name + protocol badge). Right: the big connect/disconnect
/// control + live status. Pure focus navigation, no QR / file import / rules.
struct TVMainView: View {
    @EnvironmentObject private var state: TVAppState

    var body: some View {
        HStack(spacing: 0) {
            serverList
                .frame(maxWidth: 700)
            Divider()
            connectPanel
                .frame(maxWidth: .infinity)
        }
        .padding(60)
    }

    // MARK: — Server list

    private var serverList: some View {
        VStack(alignment: .leading, spacing: 24) {
            HStack {
                Text("tv.main.servers", tableName: nil)
                    .font(.title).bold()
                Spacer()
                Button {
                    Task { await state.refreshConfigs() }
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .accessibilityLabel(String(localized: "tv.main.refresh"))
            }

            if state.loadingConfigs && state.remoteConfigs.isEmpty {
                ProgressView().frame(maxWidth: .infinity, alignment: .center)
            } else if let err = state.configError, state.remoteConfigs.isEmpty {
                VStack(alignment: .leading, spacing: 16) {
                    Text("tv.main.load_failed", tableName: nil)
                        .font(.headline).foregroundStyle(.red)
                    Text(err).font(.callout).foregroundStyle(.secondary)
                }
            } else if state.remoteConfigs.isEmpty {
                Text("tv.main.no_configs", tableName: nil)
                    .foregroundStyle(.secondary)
            } else {
                ScrollView {
                    LazyVStack(spacing: 12) {
                        ForEach(state.remoteConfigs) { entry in
                            Button {
                                state.selectedConfigID = entry.id
                            } label: {
                                serverRow(entry)
                            }
                            .buttonStyle(.card)
                        }
                    }
                }
            }
        }
    }

    private func serverRow(_ entry: RemoteConfigEntry) -> some View {
        // Country + flag derived from the peer/interface name (city3 + cc
        // convention) like the phone app's PoolHostnameLabels.
        let cc = countryCode(for: entry)
        let flag = PoolHostnameLabels.flagEmoji(cc)
        let country = PoolHostnameLabels.countryNameFromCode(cc)
        let selected = state.selectedConfig?.id == entry.id
        return HStack(spacing: 16) {
            Text(flag.isEmpty ? "🌐" : flag).font(.largeTitle)
            VStack(alignment: .leading, spacing: 4) {
                Text(entry.name).font(.headline)
                if !country.isEmpty {
                    Text(country).font(.subheadline).foregroundStyle(.secondary)
                }
            }
            Spacer()
            Text(entry.protocol.displayName)
                .font(.caption).bold()
                .padding(.horizontal, 10).padding(.vertical, 4)
                .background(.tint.opacity(0.2), in: Capsule())
            if selected {
                Image(systemName: "checkmark.circle.fill").foregroundStyle(.tint)
            }
        }
        .padding(.vertical, 8)
    }

    /// Best-effort ISO country from a hostname like "at-vie-wg-001".
    private func countryCode(for entry: RemoteConfigEntry) -> String {
        let name = entry.interfaceName.isEmpty ? entry.peerName : entry.interfaceName
        let parts = name.split(separator: "-")
        if let first = parts.first, first.count == 2 { return String(first).uppercased() }
        return ""
    }

    // MARK: — Connect panel

    private var connectPanel: some View {
        ScrollView {
            VStack(spacing: 36) {
                statusHero
                connectButton
                if state.status.connected {
                    trafficCards
                    detailsCard
                }
                if let err = state.configError, !state.remoteConfigs.isEmpty {
                    Text(err).font(.callout).foregroundStyle(.red)
                        .multilineTextAlignment(.center)
                }
                Button(role: .destructive) {
                    Task { await state.unenroll() }
                } label: {
                    Label(String(localized: "tv.main.unlink"), systemImage: "xmark.circle")
                }
                .padding(.top, 8)
            }
            .padding(40)
            .frame(maxWidth: .infinity)
        }
    }

    private var statusHero: some View {
        let connected = state.status.connected
        return VStack(spacing: 14) {
            ZStack {
                Circle()
                    .fill((connected ? Color.green : Color.gray).opacity(0.15))
                    .frame(width: 200, height: 200)
                Image(systemName: connected ? "lock.shield.fill" : "shield.slash")
                    .font(.system(size: 96))
                    .foregroundStyle(connected ? .green : .secondary)
            }
            Text(connected ? String(localized: "tv.status.connected")
                           : String(localized: "tv.status.disconnected"))
                .font(.largeTitle).bold()
                .foregroundStyle(connected ? .green : .primary)
            if connected {
                HStack(spacing: 10) {
                    if let p = state.status.activeProtocol {
                        Text(p.displayName)
                            .font(.headline)
                            .padding(.horizontal, 12).padding(.vertical, 5)
                            .background(.tint.opacity(0.2), in: Capsule())
                    }
                    if state.status.uptime > 0 {
                        Text(formatUptime(state.status.uptime))
                            .font(.headline).monospacedDigit()
                            .foregroundStyle(.secondary)
                    }
                }
            } else if let sel = state.selectedConfig {
                Text(sel.name).font(.title3).foregroundStyle(.secondary)
            }
        }
    }

    private var trafficCards: some View {
        HStack(spacing: 24) {
            statCard(title: String(localized: "tv.stats.download", defaultValue: "Download"), icon: "arrow.down",
                     total: state.status.rxBytes, speed: state.rxSpeed,
                     history: state.rxHistory, tint: .green)
            statCard(title: String(localized: "tv.stats.upload", defaultValue: "Upload"), icon: "arrow.up",
                     total: state.status.txBytes, speed: state.txSpeed,
                     history: state.txHistory, tint: .blue)
        }
    }

    private func statCard(title: String, icon: String, total: Int64, speed: Double,
                          history: [Double], tint: Color) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: icon).foregroundStyle(tint)
                Text(title).font(.headline)
                Spacer()
                Text(formatSpeed(speed)).font(.subheadline).monospacedDigit()
                    .foregroundStyle(.secondary)
            }
            Text(formatBytes(total)).font(.title2).bold().monospacedDigit()
            Sparkline(values: history, tint: tint).frame(height: 44)
        }
        .padding(20)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18))
    }

    private var detailsCard: some View {
        VStack(spacing: 0) {
            detailRow(String(localized: "tv.detail.endpoint", defaultValue: "Endpoint"), state.status.serverEndpoint)
            if !state.status.localAddress.isEmpty {
                Divider()
                detailRow(String(localized: "tv.detail.vpn_ip", defaultValue: "VPN IP"), state.status.localAddress)
            }
        }
        .padding(.horizontal, 24).padding(.vertical, 8)
        .frame(maxWidth: .infinity)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 18))
    }

    private func detailRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).foregroundStyle(.secondary)
            Spacer()
            Text(value.isEmpty ? "—" : value).monospacedDigit().lineLimit(1)
        }
        .font(.headline)
        .padding(.vertical, 14)
    }

    private var connectButton: some View {
        Button {
            Task { await state.toggle() }
        } label: {
            HStack {
                if state.connecting {
                    ProgressView()
                } else {
                    Image(systemName: state.status.connected ? "stop.fill" : "play.fill")
                }
                Text(state.status.connected ? String(localized: "tv.action.disconnect")
                                            : String(localized: "tv.action.connect"))
                    .font(.title2).bold()
            }
            .frame(minWidth: 320)
            .padding(.vertical, 8)
        }
        .disabled(state.connecting || state.selectedConfig == nil)
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

/// Minimal throughput sparkline for tvOS (no shared dependency on the phone's
/// TransferStatsCard, which lives in the iOS app target).
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
