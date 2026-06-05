import SwiftUI
import PrivycsCore

/// Pool aus Multi-File-Import (ZIP oder einzelne Configs).
struct AddPoolView: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss

    /// Config contents already EXTRACTED at the tab root before this sheet
    /// opened (see AddConnectionView). The files are read there, inside the
    /// `.fileImporter` security scope — reading them later (at Save time) is
    /// unreliable because the security-scoped grant can lapse, which left
    /// `pickedFiles` effectively empty and the Save button permanently disabled
    /// ("bei Speichern passiert nix"). So this sheet only ever works with
    /// already-read content; no file URL is touched here.
    let initialConfigs: [PoolImporter.ExtractedConfig]

    @State private var name = ""
    @State private var policy: PoolPolicy = .geoNearest
    @State private var configs: [PoolImporter.ExtractedConfig] = []
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
                    if configs.isEmpty {
                        Text("No configs found — cancel and tap “Create a VPN pool” again.")
                            .foregroundColor(.secondary).font(.caption)
                    } else {
                        ForEach(configs, id: \.filename) { c in
                            Label(c.filename, systemImage: "doc").font(.caption)
                        }
                    }
                } header: {
                    Text("Selected configs (\(configs.count))")
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
                    .disabled(name.isEmpty || configs.isEmpty)
                }
                ToolbarItem(placement: .navigationBarLeading) {
                    Button("Cancel") { dismiss() }
                }
            }
            .onAppear { if configs.isEmpty { configs = initialConfigs } }
        }
    }

    private func save() async {
        // Configs were already extracted at pick time (tab root) — just build
        // the members, geolocate, and persist. No file reading here.
        var members = PoolImporter.makeMembers(configs)
        PrivycsLog.log("Pool save: \(configs.count) config(s) → \(members.count) member(s)")
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
