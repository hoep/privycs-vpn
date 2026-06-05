import SwiftUI
import UniformTypeIdentifiers
import PrivycsCore

/// Pool aus Multi-File-Import (ZIP oder einzelne Configs).
struct AddPoolView: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss

    /// Files chosen at the tab root BEFORE this sheet opened — the iOS-15-safe
    /// flow (see AddConnectionView): the proven tab-root `.fileImporter` picks
    /// the files, then this sheet opens with them already in hand. No picker is
    /// presented from inside this sheet (that mis-dismisses on iOS 15).
    let initialFiles: [URL]

    @State private var name = ""
    @State private var policy: PoolPolicy = .geoNearest
    @State private var pickedFiles: [URL] = []
    @State private var errorMessage: String?

    var body: some View {
        AdaptiveNavStack {
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
                    if pickedFiles.isEmpty {
                        Text("No files selected — cancel and tap “Create a VPN pool” again.")
                            .foregroundColor(.secondary).font(.caption)
                    } else {
                        ForEach(pickedFiles, id: \.self) { url in
                            Label(url.lastPathComponent, systemImage: "doc").font(.caption)
                        }
                    }
                } header: {
                    Text("Selected files (\(pickedFiles.count))")
                } footer: {
                    Text("These become the pool members — a single .zip archive is expanded into all the configs inside it. Country is parsed from each filename when possible.")
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
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button("Save") {
                        Task { await save() }
                    }
                    .disabled(name.isEmpty || pickedFiles.isEmpty)
                }
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("Cancel") { dismiss() }
                }
            }
            .onAppear { if pickedFiles.isEmpty { pickedFiles = initialFiles } }
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
            errorMessage = String(localized: "No valid config files found in the selection.")
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
