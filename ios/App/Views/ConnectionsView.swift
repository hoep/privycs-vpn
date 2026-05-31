import SwiftUI
import PrivycsCore

/// Configs-Screen — Liste aller importierten SavedConnections +
/// Pools mit Edit/Delete pro Eintrag.
struct ConnectionsView: View {
    @EnvironmentObject private var appState: AppState

    var body: some View {
        NavigationStack {
            List {
                if !appState.pools.isEmpty {
                    Section("Pools") {
                        ForEach(appState.pools) { pool in
                            poolRow(pool)
                        }
                    }
                }
                Section("Connections") {
                    if appState.connections.isEmpty {
                        Text("No configs yet. Use the Add tab to import.")
                            .foregroundStyle(.secondary)
                            .font(.callout)
                    } else {
                        ForEach(appState.connections) { conn in
                            connectionRow(conn)
                        }
                        .onDelete(perform: deleteConnections)
                    }
                }
            }
            .navigationTitle("Configs")
        }
    }

    private func poolRow(_ pool: Pool) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(pool.name).font(.body)
            HStack {
                Text(pool.policy.displayName).font(.caption2)
                Text("•").font(.caption2)
                Text("\(pool.members.count) members").font(.caption2)
            }
            .foregroundStyle(.secondary)
        }
    }

    private func connectionRow(_ conn: SavedConnection) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(conn.name).font(.body)
            HStack(spacing: 4) {
                ForEach(conn.protocols) { p in
                    Text(p.protocol.displayName)
                        .font(.caption2)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(Capsule().fill(.thinMaterial))
                }
            }
        }
    }

    private func deleteConnections(at offsets: IndexSet) {
        let ids = offsets.map { appState.connections[$0].id }
        Task {
            for id in ids {
                try? await appState.connectionRepo.delete(id)
            }
            appState.connections = (try? await appState.connectionRepo.loadAll()) ?? []
        }
    }
}
