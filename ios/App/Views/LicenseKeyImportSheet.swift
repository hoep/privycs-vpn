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
        AdaptiveNavStack {
            Form {
                Section {
                    // Multi-line growing TextField (axis:/lineLimit-range) is
                    // iOS 16+; fall back to a TextEditor on iOS 15.
                    if #available(iOS 16, *) {
                        TextField("Paste your license key", text: $keyText, axis: .vertical)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .lineLimit(4...12)
                            .font(.caption.monospaced())
                    } else {
                        TextEditor(text: $keyText)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .font(.caption.monospaced())
                            .frame(minHeight: 96)
                    }
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
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button("Import") { Task { await importKey() } }
                        .disabled(keyText.isEmpty || importing)
                }
                ToolbarItem(placement: .navigationBarLeading) {
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
            result = loc("License accepted — \(payload.sku) (\(payload.platforms.joined(separator: ", ")))")
            isError = false
            // Auto-dismiss nach kurzer Bestätigung
            try? await Task.sleep(nanoseconds: UInt64(2 * 1_000_000_000))
            dismiss()
        } catch {
            result = error.localizedDescription
            isError = true
        }
    }
}
