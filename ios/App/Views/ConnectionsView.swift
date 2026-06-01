import SwiftUI
import UniformTypeIdentifiers
import PrivycsCore

/// Configs screen — port of Android ConnectionsScreen. Per-connection
/// protocol badges (tap to switch active config), "+" to add another
/// protocol/config to a connection, rename, delete-config, delete-
/// connection; pools navigate to PoolDetailView.
struct ConnectionsView: View {
    @EnvironmentObject private var appState: AppState
    @State private var addProtocolFor: SavedConnection?
    @State private var renameFor: SavedConnection?
    @State private var renameText = ""

    var body: some View {
        NavigationStack {
            List {
                if !appState.pools.isEmpty {
                    Section("Pools") {
                        ForEach(appState.pools) { pool in
                            NavigationLink {
                                PoolDetailView(pool: pool).environmentObject(appState)
                            } label: { poolRow(pool) }
                        }
                    }
                }
                Section("Connections") {
                    if appState.connections.isEmpty {
                        Text("No configs yet. Use the Add tab to import.")
                            .foregroundStyle(.secondary).font(.callout)
                    } else {
                        ForEach(appState.connections) { conn in
                            connectionRow(conn)
                        }
                        .onDelete(perform: deleteConnections)
                    }
                }
            }
            .navigationTitle("Configs")
            .sheet(item: $addProtocolFor) { conn in
                AddProtocolSheet(connection: conn).environmentObject(appState)
            }
            .alert("Rename connection", isPresented: Binding(
                get: { renameFor != nil }, set: { if !$0 { renameFor = nil } }
            )) {
                TextField("Name", text: $renameText)
                Button("Cancel", role: .cancel) { renameFor = nil }
                Button("Save") { commitRename() }
            }
        }
    }

    private func poolRow(_ pool: Pool) -> some View {
        HStack(spacing: 10) {
            Image(systemName: "circle.grid.3x3.fill").foregroundStyle(PrivycsColor.teal)
            VStack(alignment: .leading, spacing: 2) {
                Text(pool.name).font(.body)
                Text("\(pool.policy.displayName) · \(pool.members.count) servers")
                    .font(.caption2).foregroundStyle(.secondary)
            }
            if appState.activePool?.id == pool.id {
                Spacer()
                Text("Active").font(.caption2.weight(.semibold)).foregroundStyle(PrivycsColor.teal)
            }
        }
    }

    private func connectionRow(_ conn: SavedConnection) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(conn.name).font(.body)
                Spacer()
                Button { startRename(conn) } label: {
                    Image(systemName: "pencil").font(.caption)
                }
                .buttonStyle(.borderless).foregroundStyle(.secondary)
            }
            // Tappable protocol badges — tap to switch the active config
            // (per-protocol connect, like Android's ProtocolBadges).
            FlowRow(spacing: 6) {
                ForEach(conn.protocols) { cfg in
                    Button {
                        Task { await appState.setActiveConfig(connectionID: conn.id, configID: cfg.id) }
                    } label: {
                        ProtocolBadge(proto: cfg.protocol, endpoint: cfg.serverAddress)
                            .overlay(alignment: .topTrailing) {
                                if cfg.id == conn.activeConfigID {
                                    Circle().fill(PrivycsColor.connected).frame(width: 6, height: 6).offset(x: 3, y: -3)
                                }
                            }
                            .opacity(cfg.id == conn.activeConfigID ? 1.0 : 0.55)
                    }
                    .buttonStyle(.plain)
                    .contextMenu {
                        Button(role: .destructive) {
                            Task { await appState.removeConfig(connectionID: conn.id, configID: cfg.id) }
                        } label: { Label("Remove \(cfg.protocol.shortLabel)", systemImage: "trash") }
                    }
                }
                Button { addProtocolFor = conn } label: {
                    Image(systemName: "plus.circle").font(.system(size: 16)).foregroundStyle(PrivycsColor.teal)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.vertical, 2)
    }

    private func startRename(_ conn: SavedConnection) {
        renameText = conn.name
        renameFor = conn
    }

    private func commitRename() {
        guard let conn = renameFor else { return }
        let newName = renameText.trimmingCharacters(in: .whitespaces)
        renameFor = nil
        guard !newName.isEmpty else { return }
        Task {
            var updated = conn
            updated.name = newName
            try? await appState.connectionRepo.save(updated)
            appState.connections = (try? await appState.connectionRepo.loadAll()) ?? appState.connections
        }
    }

    private func deleteConnections(at offsets: IndexSet) {
        let ids = offsets.map { appState.connections[$0].id }
        Task {
            for id in ids { try? await appState.connectionRepo.delete(id) }
            appState.connections = (try? await appState.connectionRepo.loadAll()) ?? []
        }
    }
}

/// Sheet to add another protocol/config to an EXISTING connection —
/// file import or gateway pull, both targeting `connection.id`.
struct AddProtocolSheet: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss
    let connection: SavedConnection

    @State private var showFileImporter = false
    @State private var showGateway = false
    @State private var note: String?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    Button {
                        showFileImporter = true
                    } label: { Label("Import from file", systemImage: "doc.badge.plus") }
                    Button {
                        showGateway = true
                    } label: { Label("Pull from Privycs Gateway", systemImage: "cloud.bolt") }
                        .disabled(appState.gatewayClient == nil)
                } footer: {
                    Text("Adds another protocol to “\(connection.name)”.")
                }
                if let note { Section { Label(note, systemImage: "checkmark.circle.fill").foregroundStyle(PrivycsColor.connected) } }
            }
            .navigationTitle("Add Protocol")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done") { dismiss() } } }
            .fileImporter(isPresented: $showFileImporter, allowedContentTypes: [UTType.data], allowsMultipleSelection: false) { result in
                Task { await handleFile(result) }
            }
            .sheet(isPresented: $showGateway) {
                GatewayConfigSheet(targetConnectionID: connection.id).environmentObject(appState)
            }
        }
    }

    private func handleFile(_ result: Result<[URL], Error>) async {
        guard case .success(let urls) = result, let url = urls.first else { return }
        guard url.startAccessingSecurityScopedResource() else { return }
        defer { url.stopAccessingSecurityScopedResource() }
        guard let data = try? Data(contentsOf: url), let raw = String(data: data, encoding: .utf8) else {
            note = "Could not read file"; return
        }
        await appState.importConnection(
            name: connection.name,
            filename: url.lastPathComponent,
            content: raw,
            intoConnectionID: connection.id
        )
        note = "Added \(url.lastPathComponent)"
        try? await Task.sleep(for: .seconds(0.8))
        dismiss()
    }
}

/// Minimal wrapping HStack (SwiftUI has no native FlowLayout pre-iOS16
/// Layout; this is a simple wrap using the iOS 16 Layout protocol).
struct FlowRow: Layout {
    var spacing: CGFloat = 6

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let maxWidth = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, rowHeight: CGFloat = 0
        for sv in subviews {
            let s = sv.sizeThatFits(.unspecified)
            if x + s.width > maxWidth { x = 0; y += rowHeight + spacing; rowHeight = 0 }
            x += s.width + spacing
            rowHeight = max(rowHeight, s.height)
        }
        return CGSize(width: maxWidth == .infinity ? x : maxWidth, height: y + rowHeight)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let maxWidth = bounds.width
        var x: CGFloat = bounds.minX, y: CGFloat = bounds.minY, rowHeight: CGFloat = 0
        for sv in subviews {
            let s = sv.sizeThatFits(.unspecified)
            if x - bounds.minX + s.width > maxWidth { x = bounds.minX; y += rowHeight + spacing; rowHeight = 0 }
            sv.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(s))
            x += s.width + spacing
            rowHeight = max(rowHeight, s.height)
        }
    }
}
