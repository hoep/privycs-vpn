import SwiftUI
import UniformTypeIdentifiers
import PrivycsCore

/// Add screen — file import (.conf/.ovpn/.sswan/.mobileconfig),
/// QR scan (raw WireGuard config or privycs:// enrollment), and
/// gateway config pull. Port of Android's AddConnectionScreen.
struct AddConnectionView: View {
    @EnvironmentObject private var appState: AppState
    @State private var showFileImporter = false
    @State private var showQRScanner = false
    @State private var showGatewaySheet = false
    @State private var showAddPool = false
    @State private var importErrorMessage: String?
    @State private var importedNote: String?
    // Pool import: the tab-root .fileImporter is reused for pools (it presents
    // reliably here and has no parent sheet to mis-dismiss on iOS 15). When this
    // flag is set, a successful import hands the files to AddPoolView instead of
    // importing them as single connections. [v1.1.4]
    @State private var importingForPool = false
    @State private var pickedPoolFiles: [URL] = []

    var body: some View {
        AdaptiveNavStack {
            List {
                Section {
                    Button {
                        importingForPool = false   // guard against a stale pool flag from a cancelled pick
                        showFileImporter = true
                    } label: {
                        Label("Import from file", systemImage: "doc.badge.plus")
                    }
                    Button {
                        showQRScanner = true
                    } label: {
                        Label("Scan QR code", systemImage: "qrcode.viewfinder")
                    }
                    Button {
                        showGatewaySheet = true
                    } label: {
                        Label("Pull from Privycs Gateway", systemImage: "cloud.bolt")
                    }
                    .disabled(appState.gatewayClient == nil)
                } header: {
                    Text("Single connection")
                } footer: {
                    Text(appState.gatewayClient == nil
                         ? "Supported: .conf (WireGuard/AmneziaWG), .ovpn (OpenVPN), .sswan / .mobileconfig (IPSec). Configure a gateway in Settings to pull remote configs."
                         : "Supported: .conf, .ovpn, .sswan / .mobileconfig. Or pull your configs from the gateway.")
                }
                Section {
                    Button {
                        // Pick the pool files at the tab root FIRST (the
                        // .fileImporter presents reliably here and has no parent
                        // sheet to mis-dismiss), THEN open the pool-config sheet
                        // with the chosen files. Sidesteps the iOS-15 nested-
                        // importer bug entirely. [v1.1.4]
                        importingForPool = true
                        pickedPoolFiles = []
                        showFileImporter = true
                    } label: {
                        Label("Create a VPN pool", systemImage: "circle.grid.3x3.fill")
                    }
                } header: {
                    Text("VPN pool")
                } footer: {
                    Text("Bundle several servers into a pool and let the app rotate between them (geo-nearest / round-robin / random) with automatic failover. Import individual configs or a single .zip archive.")
                }
                if let note = importedNote {
                    Section { Label(note, systemImage: "checkmark.circle.fill").foregroundStyle(PrivycsColor.connected) }
                }
                if let err = importErrorMessage {
                    Section { Text(err).foregroundStyle(.red).font(.caption) }
                }
            }
            .navigationTitle("Add Config")
            .fileImporter(
                isPresented: $showFileImporter,
                allowedContentTypes: [UTType.data],
                allowsMultipleSelection: true
            ) { result in
                Task { await handleImport(result) }
            }
            .sheet(isPresented: $showQRScanner) {
                QRScannerView { raw in
                    showQRScanner = false
                    Task { await handleQRScan(raw) }
                }
            }
            .sheet(isPresented: $showGatewaySheet) {
                GatewayConfigSheet().environmentObject(appState)
            }
            .sheet(isPresented: $showAddPool) {
                AddPoolView(initialFiles: pickedPoolFiles).environmentObject(appState)
            }
        }
    }

    private func handleImport(_ result: Result<[URL], Error>) async {
        // Pool-import branch: hand the chosen files to AddPoolView instead of
        // importing them as single connections. [v1.1.4]
        if importingForPool {
            importingForPool = false
            if case .success(let urls) = result, !urls.isEmpty {
                pickedPoolFiles = urls
                showAddPool = true
            }
            return
        }
        importErrorMessage = nil; importedNote = nil
        guard case .success(let urls) = result else {
            if case .failure(let err) = result { importErrorMessage = err.localizedDescription }
            return
        }
        var count = 0
        for url in urls {
            guard url.startAccessingSecurityScopedResource() else { continue }
            defer { url.stopAccessingSecurityScopedResource() }
            // Read via Data → String to avoid the deprecated
            // String(contentsOf:encoding:) overload.
            guard let data = try? Data(contentsOf: url),
                  let raw = String(data: data, encoding: .utf8) else {
                importErrorMessage = String(localized: "Could not read \(url.lastPathComponent)")
                continue
            }
            await appState.importConnection(
                name: ConfigImport.deriveConnectionName(url.lastPathComponent),
                filename: url.lastPathComponent,
                content: raw
            )
            count += 1
        }
        if count > 0 { importedNote = String(localized: "\(count) config(s) imported") }
    }

    private func handleQRScan(_ raw: String) async {
        importErrorMessage = nil; importedNote = nil
        guard let payload = QRPayload.parse(raw) else {
            importErrorMessage = String(localized: "Unrecognized QR code")
            return
        }
        switch payload {
        case .wireguard(let text), .amneziawg(let text), .openvpn(let text):
            await appState.importConnection(name: String(localized: "Scanned config"), filename: "qr.conf", content: text)
            importedNote = String(localized: "Imported from QR")
        case .privycsEnrollment(let url, let apiKey):
            await appState.applyGatewayEnrollment(url: url, apiKey: apiKey)
            importedNote = String(localized: "Gateway enrolled — pull your configs")
            showGatewaySheet = true
        }
    }
}

/// Sheet listing the user's gateway configs (Pro "pull from gateway").
/// Fetches /api/v1/connect/my-configs, downloads + imports on tap.
struct GatewayConfigSheet: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss

    /// When set, imported configs are added to this existing connection
    /// (add-protocol flow); nil = each import becomes a new connection.
    var targetConnectionID: String? = nil

    @State private var entries: [RemoteConfigEntry] = []
    @State private var loading = true
    @State private var error: String?
    @State private var importingID: Int?
    @State private var importedNames: [String] = []

    var body: some View {
        AdaptiveNavStack {
            List {
                if loading {
                    HStack { Spacer(); ProgressView(); Spacer() }
                } else if let error {
                    Text(error).foregroundStyle(.red).font(.callout)
                } else if entries.isEmpty {
                    Text("No remote configs found.").foregroundStyle(.secondary)
                } else {
                    ForEach(entries) { entry in
                        Button {
                            Task { await importEntry(entry) }
                        } label: {
                            HStack(spacing: 10) {
                                ProtocolBadge(proto: entry.protocol)
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(entry.name).font(.system(size: 14, weight: .medium))
                                        .foregroundStyle(PrivycsColor.onSurface)
                                    // Interface + assigned VPN IP (Android parity).
                                    let detail = [entry.interfaceName, entry.vpnIP]
                                        .filter { !$0.isEmpty }.joined(separator: " · ")
                                    if !detail.isEmpty {
                                        Text(detail).font(.system(size: 11))
                                            .foregroundStyle(.secondary)
                                    }
                                }
                                Spacer()
                                if importingID == entry.id {
                                    ProgressView()
                                } else if importedNames.contains(entry.name) {
                                    Image(systemName: "checkmark.circle.fill").foregroundStyle(PrivycsColor.connected)
                                } else {
                                    Image(systemName: "arrow.down.circle").foregroundStyle(PrivycsColor.teal)
                                }
                            }
                        }
                        .buttonStyle(.plain)
                    }
                }

                if !importedNames.isEmpty {
                    Section {
                        Label("Imported \(importedNames.count) config(s) — closing…",
                              systemImage: "checkmark.circle.fill")
                            .foregroundStyle(PrivycsColor.connected)
                    }
                }
            }
            .navigationTitle("Remote Configs")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarTrailing) { Button("Done") { dismiss() } }
            }
            .task { await load() }
        }
    }

    private func load() async {
        loading = true; error = nil
        defer { loading = false }
        guard let client = appState.gatewayClient else {
            error = String(localized: "Gateway not configured. Set it in Settings.")
            return
        }
        do { entries = try await client.listMyConfigs() }
        catch { self.error = error.localizedDescription }
    }

    private func importEntry(_ entry: RemoteConfigEntry) async {
        guard let client = appState.gatewayClient else { return }
        importingID = entry.id
        defer { importingID = nil }
        do {
            let raw = try await client.fetchConfig(entry: entry)
            let ext: String
            switch entry.protocol {
            case .openvpn: ext = "ovpn"
            case .ipsec: ext = "sswan"
            default: ext = "conf"
            }
            await appState.importConnection(
                name: entry.name,
                filename: "\(entry.name).\(ext)",
                content: raw,
                intoConnectionID: targetConnectionID
            )
            // Visible feedback (the import used to silently sit there).
            if !importedNames.contains(entry.name) { importedNames.append(entry.name) }
            // Auto-close so the user lands back on the connections list
            // and sees the freshly-imported config.
            try? await Task.sleep(nanoseconds: UInt64(1.0 * 1_000_000_000))
            dismiss()
        } catch {
            self.error = error.localizedDescription
        }
    }
}
