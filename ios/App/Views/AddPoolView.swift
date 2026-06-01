import SwiftUI
import UniformTypeIdentifiers
import PrivycsCore

/// Pool aus Multi-File-Import (ZIP oder einzelne Configs).
struct AddPoolView: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss

    @State private var name = ""
    @State private var policy: PoolPolicy = .geoNearest
    @State private var fileImporterShown = false
    @State private var pickedFiles: [URL] = []
    @State private var errorMessage: String?

    var body: some View {
        NavigationStack {
            Form {
                Section("Name") {
                    TextField("Pool name", text: $name)
                }
                Section("Selection policy") {
                    Picker("Policy", selection: $policy) {
                        ForEach(PoolPolicy.allCases) { p in
                            Text(p.displayName).tag(p)
                        }
                    }
                }
                Section {
                    Button {
                        fileImporterShown = true
                    } label: {
                        Label("Select config files (.conf / .ovpn)", systemImage: "doc.badge.plus")
                    }
                    if !pickedFiles.isEmpty {
                        ForEach(pickedFiles, id: \.self) { url in
                            Text(url.lastPathComponent).font(.caption2)
                        }
                    }
                } footer: {
                    Text("Select 2 or more config files to populate the pool. Country + region are parsed from each filename when possible.")
                }
                if let msg = errorMessage {
                    Section {
                        Text(msg).foregroundStyle(.red).font(.caption)
                    }
                }
            }
            .navigationTitle("New Pool")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Save") {
                        Task { await save() }
                    }
                    .disabled(name.isEmpty || pickedFiles.count < 2)
                }
                ToolbarItem(placement: .topBarLeading) {
                    Button("Cancel") { dismiss() }
                }
            }
            .fileImporter(
                isPresented: $fileImporterShown,
                allowedContentTypes: [UTType.data],
                allowsMultipleSelection: true
            ) { result in
                if case .success(let urls) = result {
                    pickedFiles = urls
                }
            }
        }
    }

    private func save() async {
        let id = UUID().uuidString
        var members: [PoolMember] = []
        for (idx, url) in pickedFiles.enumerated() {
            guard url.startAccessingSecurityScopedResource() else { continue }
            defer { url.stopAccessingSecurityScopedResource() }
            guard let raw = try? String(contentsOf: url, encoding: .utf8) else { continue }
            let proto = detectProtocol(filename: url.lastPathComponent, content: raw)
            let mem = PoolMember(
                id: UUID().uuidString,
                name: url.deletingPathExtension().lastPathComponent,
                country: parseCountry(from: url.lastPathComponent),
                region: "",
                index: idx,
                protocol: proto,
                configContent: raw,
                serverAddress: ""
            )
            members.append(mem)
        }
        let pool = Pool(id: id, name: name, policy: policy, members: members)
        do {
            try await appState.poolRepo.save(pool)
            appState.pools = (try? await appState.poolRepo.loadAll()) ?? []
            dismiss()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func detectProtocol(filename: String, content: String) -> VpnProtocol {
        let ext = (filename as NSString).pathExtension.lowercased()
        switch ext {
        case "ovpn": return .openvpn
        case "sswan", "mobileconfig": return .ipsec
        case "conf":
            return content.contains("Jc =") || content.contains("S1 =") ? .amneziawg : .wireguard
        default:
            return .wireguard
        }
    }

    private func parseCountry(from filename: String) -> String {
        // Heuristik: filename "DE-Frankfurt-01.conf" → "DE"
        let stem = (filename as NSString).deletingPathExtension
        let head = stem.split(separator: "-").first.map(String.init) ?? ""
        return head.count == 2 ? head.uppercased() : ""
    }
}
