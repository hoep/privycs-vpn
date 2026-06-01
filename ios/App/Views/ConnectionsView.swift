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
    @State private var editConnFor: SavedConnection?
    @State private var editConfigFor: EditConfigTarget?

    /// Identifiable wrapper so a (connection, config) pair can drive a
    /// `.sheet(item:)` for the raw-config editor.
    struct EditConfigTarget: Identifiable {
        let connectionID: String
        let config: ProtocolConfig
        var id: String { config.id }
    }

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
            .sheet(item: $editConnFor) { conn in
                EditConnectionSheet(connection: conn).environmentObject(appState)
            }
            .sheet(item: $editConfigFor) { target in
                EditProtocolConfigSheet(connectionID: target.connectionID, config: target.config)
                    .environmentObject(appState)
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
                if !conn.dnsOverride.isEmpty {
                    Image(systemName: "lock.shield").font(.caption2).foregroundStyle(PrivycsColor.teal)
                }
                Spacer()
                Button { editConnFor = conn } label: {
                    Image(systemName: "pencil").font(.caption)
                }
                .buttonStyle(.borderless).foregroundStyle(.secondary)
            }
            // Tappable protocol badges — tap to switch the active config
            // (per-protocol connect, like Android's ProtocolBadges).
            // One pill PER PROTOCOL with an ×N count when a protocol has
            // multiple configs (Android parity — a connection is a failover
            // bag of same-protocol endpoints). Tap switches the active config
            // to that protocol; if connected, setActiveConfig reconnects.
            FlowRow(spacing: 6) {
                ForEach(groupedProtocols(conn), id: \.self) { proto in
                    let cfgs = conn.protocols.filter { $0.protocol == proto }
                    Button {
                        let target = cfgs.first(where: { $0.id == conn.activeConfigID }) ?? cfgs.first
                        if let target {
                            Task { await appState.setActiveConfig(connectionID: conn.id, configID: target.id) }
                        }
                    } label: {
                        ProtocolBadge(
                            proto: proto,
                            endpoint: cfgs.count == 1 ? endpointHost(cfgs[0].serverAddress) : nil,
                            active: cfgs.contains { $0.id == conn.activeConfigID },
                            count: cfgs.count
                        )
                    }
                    // .borderless (not .plain) so each pill is an independent
                    // tap target inside the List row.
                    .buttonStyle(.borderless)
                    .contextMenu {
                        ForEach(cfgs) { cfg in
                            if cfg.protocol == .wireguard || cfg.protocol == .amneziawg || cfg.protocol == .openvpn {
                                Button {
                                    editConfigFor = EditConfigTarget(connectionID: conn.id, config: cfg)
                                } label: { Label("Edit \(configLabel(cfg))", systemImage: "pencil") }
                            }
                        }
                        ForEach(cfgs) { cfg in
                            Button(role: .destructive) {
                                Task { await appState.removeConfig(connectionID: conn.id, configID: cfg.id) }
                            } label: { Label("Remove \(configLabel(cfg))", systemImage: "trash") }
                        }
                    }
                }
                Button { addProtocolFor = conn } label: {
                    Image(systemName: "plus.circle").font(.system(size: 16)).foregroundStyle(PrivycsColor.teal)
                }
                .buttonStyle(.borderless)
            }
            // Per-protocol VPN-IP summary (Android parity) — only configs
            // that have a cached inner address from a prior connect.
            let withIP = conn.protocols.filter { !$0.localAddress.isEmpty }
            if !withIP.isEmpty {
                Text(withIP.map { "\($0.protocol.shortLabel): \($0.localAddress)" }.joined(separator: " · "))
                    .font(.caption2).fontDesign(.monospaced).foregroundStyle(.secondary)
                    .lineLimit(1).truncationMode(.middle)
            }
        }
        .padding(.vertical, 2)
    }

    /// Distinct protocols of a connection, in order of first appearance.
    private func groupedProtocols(_ conn: SavedConnection) -> [VpnProtocol] {
        var seen = Set<VpnProtocol>()
        var out: [VpnProtocol] = []
        for c in conn.protocols where seen.insert(c.protocol).inserted { out.append(c.protocol) }
        return out
    }

    /// Human label for a config in context menus (nickname → filename → proto).
    private func configLabel(_ cfg: ProtocolConfig) -> String {
        if !cfg.nickname.isEmpty { return cfg.nickname }
        if !cfg.filename.isEmpty { return cfg.filename }
        return cfg.protocol.shortLabel
    }

    /// Strip the port for a compact endpoint display in the badge.
    private func endpointHost(_ s: String) -> String {
        guard !s.isEmpty else { return s }
        if s.hasPrefix("["), let close = s.firstIndex(of: "]") {   // [IPv6]:port
            return String(s[s.index(after: s.startIndex)..<close])
        }
        return s.split(separator: ":").first.map(String.init) ?? s
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

/// Edit a connection's name + per-connection DNS override (Android's
/// edit-connection dialog had both; iOS previously only renamed).
struct EditConnectionSheet: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss
    let connection: SavedConnection
    @State private var name: String
    @State private var dns: String

    init(connection: SavedConnection) {
        self.connection = connection
        _name = State(initialValue: connection.name)
        _dns = State(initialValue: connection.dnsOverride)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Name") {
                    TextField("Name", text: $name)
                }
                Section {
                    TextField("e.g. 1.1.1.1, 9.9.9.9", text: $dns)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                } header: {
                    Text("DNS override")
                } footer: {
                    Text("Comma-separated DNS servers used while this connection is active. Empty = use the global setting.")
                }
            }
            .navigationTitle("Edit connection")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) { Button("Save") { Task { await save() } } }
                ToolbarItem(placement: .topBarLeading) { Button("Cancel") { dismiss() } }
            }
        }
    }

    private func save() async {
        var updated = connection
        let n = name.trimmingCharacters(in: .whitespaces)
        if !n.isEmpty { updated.name = n }
        updated.dnsOverride = dns.trimmingCharacters(in: .whitespaces)
        try? await appState.connectionRepo.save(updated)
        appState.connections = (try? await appState.connectionRepo.loadAll()) ?? appState.connections
        dismiss()
    }
}

/// Raw config text editor for a WireGuard/AmneziaWG/OpenVPN config —
/// re-parses + validates on save (port of Android's edit-protocol dialog).
struct EditProtocolConfigSheet: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss
    let connectionID: String
    let config: ProtocolConfig
    @State private var text: String
    @State private var error: String?

    init(connectionID: String, config: ProtocolConfig) {
        self.connectionID = connectionID
        self.config = config
        _text = State(initialValue: config.configContent)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                TextEditor(text: $text)
                    .font(.system(size: 12, design: .monospaced))
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                    .padding(8)
                if let error {
                    Text(error).font(.caption).foregroundStyle(PrivycsColor.error)
                        .frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal)
                }
            }
            .navigationTitle("Edit \(config.protocol.shortLabel)")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) { Button("Save") { Task { await save() } } }
                ToolbarItem(placement: .topBarLeading) { Button("Cancel") { dismiss() } }
            }
        }
    }

    private func save() async {
        let detected = ConfigImport.detectProtocol(filename: config.filename, content: text)
        guard detected == config.protocol else {
            error = "This no longer parses as \(config.protocol.shortLabel)."
            return
        }
        let updated = ProtocolConfig(
            id: config.id, protocol: config.protocol, filename: config.filename,
            nickname: config.nickname, configContent: text,
            serverAddress: ConfigImport.extractServerAddress(text, config.protocol),
            localAddress: config.localAddress, addedAt: config.addedAt
        )
        _ = try? await appState.connectionRepo.addOrUpdate(connectionID: connectionID, name: "", config: updated)
        appState.connections = (try? await appState.connectionRepo.loadAll()) ?? appState.connections
        dismiss()
    }
}
