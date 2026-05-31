import SwiftUI
import PrivycsCore

/// Main Connect-Screen. Mirror der Android ConnectScreen +
/// Desktop ConnectionView.vue. Big-Button-Connect, status pill,
/// traffic counter, protocol-picker, pool-aware overlay.
struct ConnectionView: View {
    @EnvironmentObject private var appState: AppState
    @State private var connecting = false
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            VStack(spacing: 24) {
                statusPill
                connectButton
                if appState.status.connected {
                    statsCards
                    connectionDetails
                }
                if let msg = errorMessage {
                    Text(msg)
                        .font(.caption)
                        .foregroundStyle(.red)
                        .multilineTextAlignment(.center)
                }
                Spacer()
            }
            .padding()
            .navigationTitle("Privycs VPN")
            .navigationBarTitleDisplayMode(.inline)
        }
    }

    private var statusPill: some View {
        HStack(spacing: 8) {
            Circle()
                .fill(appState.status.connected ? .green : .gray)
                .frame(width: 10, height: 10)
            Text(appState.status.connected ? "Connected" : "Disconnected")
                .font(.caption)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .background(Capsule().fill(.thinMaterial))
    }

    private var connectButton: some View {
        Button {
            Task { await toggleConnection() }
        } label: {
            ZStack {
                Circle()
                    .fill(appState.status.connected ? Color.green.opacity(0.2) : Color.accentColor.opacity(0.2))
                    .frame(width: 180, height: 180)
                if connecting {
                    ProgressView().scaleEffect(2.0)
                } else {
                    Image(systemName: appState.status.connected ? "checkmark.shield.fill" : "shield")
                        .font(.system(size: 80, weight: .light))
                        .foregroundStyle(appState.status.connected ? .green : .accentColor)
                }
            }
        }
        .buttonStyle(.plain)
        .disabled(connecting || appState.connections.isEmpty)
    }

    private var statsCards: some View {
        HStack(spacing: 12) {
            statCard(title: "Download", value: formatBytes(appState.status.rxBytes), icon: "arrow.down.circle")
            statCard(title: "Upload", value: formatBytes(appState.status.txBytes), icon: "arrow.up.circle")
        }
    }

    private func statCard(title: String, value: String, icon: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Image(systemName: icon)
                Text(title).font(.caption2)
            }
            .foregroundStyle(.secondary)
            Text(value).font(.title3).bold()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding()
        .background(RoundedRectangle(cornerRadius: 12).fill(.thinMaterial))
    }

    private var connectionDetails: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !appState.status.serverEndpoint.isEmpty {
                detailRow("Server", value: appState.status.serverEndpoint)
            }
            if !appState.status.localAddress.isEmpty {
                detailRow("Tunnel IP", value: appState.status.localAddress)
            }
            if let proto = appState.status.activeProtocol {
                detailRow("Protocol", value: proto.displayName)
            }
        }
        .padding()
        .background(RoundedRectangle(cornerRadius: 12).fill(.thinMaterial))
    }

    private func detailRow(_ label: String, value: String) -> some View {
        HStack {
            Text(label).font(.caption).foregroundStyle(.secondary)
            Spacer()
            Text(value).font(.caption).fontDesign(.monospaced)
        }
    }

    private func toggleConnection() async {
        errorMessage = nil
        if appState.status.connected {
            connecting = true
            await appState.tunnelManager.disconnect()
            connecting = false
            return
        }
        guard let firstConn = appState.connections.first else { return }
        connecting = true
        defer { connecting = false }
        do {
            try await appState.tunnelManager.connect(firstConn)
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func formatBytes(_ b: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: b, countStyle: .binary)
    }
}
