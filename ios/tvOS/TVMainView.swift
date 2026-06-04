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
        VStack(spacing: 40) {
            Spacer()
            statusBadge
            connectButton
            if let err = state.configError, !state.remoteConfigs.isEmpty {
                Text(err).font(.callout).foregroundStyle(.red)
                    .multilineTextAlignment(.center)
            }
            Spacer()
            Button(role: .destructive) {
                Task { await state.unenroll() }
            } label: {
                Label(String(localized: "tv.main.unlink"), systemImage: "xmark.circle")
            }
        }
        .padding(40)
    }

    private var statusBadge: some View {
        VStack(spacing: 12) {
            let connected = state.status.connected
            Image(systemName: connected ? "lock.shield.fill" : "shield.slash")
                .font(.system(size: 100))
                .foregroundStyle(connected ? .green : .secondary)
            Text(connected ? String(localized: "tv.status.connected")
                           : String(localized: "tv.status.disconnected"))
                .font(.title).bold()
            if connected {
                if !state.status.serverEndpoint.isEmpty {
                    Text(state.status.serverEndpoint)
                        .font(.callout).foregroundStyle(.secondary)
                }
                Text(state.status.activeProtocol?.displayName ?? "")
                    .font(.callout).foregroundStyle(.secondary)
            } else if let sel = state.selectedConfig {
                Text(sel.name).font(.callout).foregroundStyle(.secondary)
            }
        }
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
}
