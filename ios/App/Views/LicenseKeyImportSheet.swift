import SwiftUI
import PrivycsCore

/// Paste-Sheet zum Importieren eines ed25519-signed License-Keys
/// (z.B. von LemonSqueezy Cross-Platform-Bundle).
struct LicenseKeyImportSheet: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss

    @State private var keyText = ""
    @State private var importing = false
    @State private var result: String?
    @State private var isError = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("Paste your license key", text: $keyText, axis: .vertical)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .lineLimit(4...12)
                        .fontDesign(.monospaced)
                        .font(.caption)
                } footer: {
                    Text("License keys are ed25519-signed and verified offline. Paste the entire string including the .signature.")
                }

                if let result {
                    Section {
                        Text(result)
                            .foregroundStyle(isError ? .red : .green)
                            .font(.callout)
                    }
                }
            }
            .navigationTitle("Import License")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Import") { Task { await importKey() } }
                        .disabled(keyText.isEmpty || importing)
                }
                ToolbarItem(placement: .topBarLeading) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
    }

    private func importKey() async {
        importing = true
        defer { importing = false }
        let trimmed = keyText.trimmingCharacters(in: .whitespacesAndNewlines)
        do {
            let payload = try await appState.entitlementRepo.importLicenseKey(trimmed)
            result = String(localized: "License accepted — \(payload.sku) (\(payload.platforms.joined(separator: ", ")))")
            isError = false
            // Auto-dismiss nach kurzer Bestätigung
            try? await Task.sleep(for: .seconds(2))
            dismiss()
        } catch {
            result = error.localizedDescription
            isError = true
        }
    }
}
