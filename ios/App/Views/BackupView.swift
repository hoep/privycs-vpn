import SwiftUI
import PrivycsCore
import UniformTypeIdentifiers

/// Encrypted backup export/import screen — parity with Android's
/// Settings → Export/Import Backup. Cross-platform compatible (same
/// AES-256-GCM + PBKDF2 envelope as Android/Desktop).
struct BackupView: View {
    @EnvironmentObject private var appState: AppState
    @State private var password = ""
    @State private var exportURL: URL?
    @State private var showShare = false   // iOS 15 ShareLink fallback
    @State private var showImporter = false
    @State private var message: String?
    @State private var isError = false
    @State private var busy = false

    var body: some View {
        Form {
            Section {
                SecureField("Passphrase", text: $password)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
            } header: {
                Text("Passphrase")
            } footer: {
                Text("Encrypts the backup (AES-256-GCM). You'll need the same passphrase to restore — there is no recovery if you forget it. Backups are cross-platform with the Android and Desktop apps.")
            }

            Section("Export") {
                Button {
                    Task { await doExport() }
                } label: { Label("Create encrypted backup", systemImage: "square.and.arrow.up") }
                    .disabled(password.count < 4 || busy)
                if let url = exportURL {
                    if #available(iOS 16, *) {
                        ShareLink(item: url) { Label("Share backup file", systemImage: "paperplane") }
                    } else {
                        Button { showShare = true } label: {
                            Label("Share backup file", systemImage: "paperplane")
                        }
                        .sheet(isPresented: $showShare) { ShareSheet(items: [url]) }
                    }
                }
            }

            Section("Import") {
                Button {
                    showImporter = true
                } label: { Label("Restore from backup", systemImage: "square.and.arrow.down") }
                    .disabled(password.count < 4 || busy)
            }

            if let message {
                Section {
                    Label(message, systemImage: isError ? "xmark.circle.fill" : "checkmark.circle.fill")
                        .foregroundStyle(isError ? PrivycsColor.error : PrivycsColor.connected)
                        .font(.callout)
                }
            }
        }
        .navigationTitle("Backup & Restore")
        .navigationBarTitleDisplayMode(.inline)
        .fileImporter(isPresented: $showImporter,
                      allowedContentTypes: [.json, .data],
                      allowsMultipleSelection: false) { result in
            Task { await doImport(result) }
        }
    }

    private func doExport() async {
        busy = true; defer { busy = false }
        do {
            let data = try await appState.exportBackup(password: password)
            let url = FileManager.default.temporaryDirectory
                .appendingPathComponent("privycs-backup.pvcbackup")
            try data.write(to: url, options: .atomic)
            exportURL = url
            message = String(localized: "Backup created — tap Share to save it."); isError = false
        } catch {
            message = backupErrorText(error); isError = true
        }
    }

    /// Localized message for a backup failure. BackupError gets a translated
    /// string (incl. the new "decrypted but unreadable" case, distinct from a
    /// wrong passphrase); anything else falls back to its description.
    private func backupErrorText(_ error: Error) -> String {
        guard let e = error as? BackupManager.BackupError else { return error.localizedDescription }
        switch e {
        case .decryptFailed:
            return String(localized: "Wrong passphrase or corrupted backup file.")
        case .payloadUnreadable:
            return String(localized: "The backup decrypted, but its contents couldn't be read — it may be from a newer or incompatible version of the app.")
        case .malformed:
            return String(localized: "This file isn't a valid Privycs backup.")
        case .unsupportedVersion(let v):
            return String(localized: "This backup (version \(v)) isn't supported by this app.")
        }
    }

    private func doImport(_ result: Result<[URL], Error>) async {
        guard case .success(let urls) = result, let url = urls.first else { return }
        guard url.startAccessingSecurityScopedResource() else {
            message = String(localized: "Could not open the selected file."); isError = true; return
        }
        defer { url.stopAccessingSecurityScopedResource() }
        busy = true; defer { busy = false }
        do {
            let data = try Data(contentsOf: url)
            let r = try await appState.importBackup(data, password: password)
            message = String(localized: "Restored \(r.connections) connection(s), \(r.pools) pool(s), \(r.rules) rule(s).")
            isError = false
        } catch {
            message = backupErrorText(error); isError = true
        }
    }
}
