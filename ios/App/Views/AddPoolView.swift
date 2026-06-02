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
                        Label("Select configs or a .zip", systemImage: "doc.badge.plus")
                    }
                    if !pickedFiles.isEmpty {
                        ForEach(pickedFiles, id: \.self) { url in
                            Text(url.lastPathComponent).font(.caption2)
                        }
                    }
                } footer: {
                    Text("Pick individual .conf/.ovpn/.sswan files, or a single .zip archive from your provider — all configs inside it become pool members. Country is parsed from each filename when possible.")
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
                    .disabled(name.isEmpty || pickedFiles.isEmpty)
                }
                ToolbarItem(placement: .topBarLeading) {
                    Button("Cancel") { dismiss() }
                }
            }
            .fileImporter(
                isPresented: $fileImporterShown,
                allowedContentTypes: [UTType.zip, UTType.data],
                allowsMultipleSelection: true
            ) { result in
                if case .success(let urls) = result {
                    pickedFiles = urls
                }
            }
        }
    }

    private func save() async {
        // Collect configs from every picked file — a .zip is expanded into
        // all its member configs (Android PoolImporter parity), loose files
        // are taken as-is.
        var configs: [PoolImporter.ExtractedConfig] = []
        for url in pickedFiles {
            // Read regardless of the access-grant result — for fileImporter
            // URLs startAccessing often returns false yet the file IS readable;
            // a hard `continue` here silently dropped every file → "0 configs"
            // (the reported "pools can't even be imported" bug).
            let access = url.startAccessingSecurityScopedResource()
            defer { if access { url.stopAccessingSecurityScopedResource() } }
            guard let data = try? Data(contentsOf: url) else {
                PrivycsLog.log("Pool import: could not read \(url.lastPathComponent)")
                continue
            }
            if url.pathExtension.lowercased() == "zip" {
                let extracted = PoolImporter.extractZip(data)
                PrivycsLog.log("Pool import: zip \(url.lastPathComponent) → \(extracted.count) config(s)")
                configs += extracted
            } else if let raw = String(data: data, encoding: .utf8),
                      PoolImporter.isConfigFile(url.lastPathComponent) {
                configs.append(.init(filename: url.lastPathComponent, content: raw))
            }
        }
        var members = PoolImporter.makeMembers(configs)
        PrivycsLog.log("Pool import: \(pickedFiles.count) file(s) → \(configs.count) config(s) → \(members.count) member(s)")
        guard members.count >= 1 else {
            errorMessage = "No valid config files found in the selection."
            return
        }
        // Geolocate each member's server (IP→country via the bundled DB) so
        // country flags show even when the filename has no <cc>- prefix.
        members = await PoolImporter.enrichCountries(members)
        let pool = Pool(id: UUID().uuidString, name: name, policy: policy, members: members)
        do {
            try await appState.poolRepo.save(pool)
            appState.pools = (try? await appState.poolRepo.loadAll()) ?? []
            dismiss()
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}
