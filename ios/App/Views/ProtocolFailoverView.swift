import SwiftUI
import PrivycsCore

/// Reorderable protocol-failover order — the default protocol pick when a
/// connection holds several protocols and none is explicitly active.
/// Mirrors Android's Settings "Protocol Failover Order" list (drag to
/// reorder). Persists to `AppSettings.protocolFailoverOrder`, which the
/// connect path consumes via `SavedConnection.resolvedActiveConfig`.
struct ProtocolFailoverView: View {
    @EnvironmentObject private var appState: AppState
    @State private var order: [VpnProtocol] = []

    var body: some View {
        List {
            Section {
                ForEach(order) { proto in
                    HStack(spacing: 12) {
                        Image(systemName: "line.3.horizontal").foregroundStyle(.secondary)
                        Text(proto.displayName)
                        Spacer()
                    }
                }
                .onMove(perform: move)
            } header: {
                Text("Drag to reorder")
            } footer: {
                Text("When a connection holds several protocols and you haven't pinned one, the app connects with the first protocol in this list that the connection has. Default: AmneziaWG → WireGuard → OpenVPN → IPSec.")
            }
            Section {
                Button("Reset to default") { setOrder(VpnProtocol.defaultFailoverOrder) }
            }
        }
        .navigationTitle("Protocol Failover")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { EditButton() }
        .task {
            let stored = appState.settings.protocolFailoverOrder
            order = stored.isEmpty ? VpnProtocol.defaultFailoverOrder : completed(stored)
        }
    }

    /// Every protocol exactly once — a stored order may predate a protocol
    /// being added to the enum, so append any missing ones in default order.
    private func completed(_ stored: [VpnProtocol]) -> [VpnProtocol] {
        var out = stored
        for p in VpnProtocol.defaultFailoverOrder where !out.contains(p) { out.append(p) }
        return out
    }

    private func move(from: IndexSet, to: Int) {
        order.move(fromOffsets: from, toOffset: to)
        persist()
    }

    private func setOrder(_ new: [VpnProtocol]) {
        order = new
        persist()
    }

    private func persist() {
        var s = appState.settings
        s.protocolFailoverOrder = order
        appState.settings = s
        Task { try? await appState.settingsRepo.save(s) }
    }
}
