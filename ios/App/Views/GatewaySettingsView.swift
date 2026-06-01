import SwiftUI
import PrivycsCore

/// Gateway configuration — URL + API key, with a verify button that
/// hits /api/v1/connect/my-configs. Port of Android's gateway section.
struct GatewaySettingsView: View {
    @EnvironmentObject private var appState: AppState
    @State private var url = ""
    @State private var apiKey = ""
    @State private var verifying = false
    @State private var result: String?
    @State private var ok = false

    var body: some View {
        Form {
            Section {
                TextField("https://gateway.example.com", text: $url)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .keyboardType(.URL)
                SecureField("API key", text: $apiKey)
                    .textInputAutocapitalization(.never)
            } footer: {
                Text("Your per-user API key from the gateway's user settings. Sent as a Bearer token.")
            }

            Section {
                Button {
                    Task { await saveAndVerify() }
                } label: {
                    HStack {
                        Text("Save & verify")
                        if verifying { Spacer(); ProgressView() }
                    }
                }
                .disabled(verifying || url.isEmpty || apiKey.isEmpty)

                if let result {
                    Label(result, systemImage: ok ? "checkmark.circle.fill" : "xmark.circle.fill")
                        .foregroundStyle(ok ? PrivycsColor.connected : PrivycsColor.error)
                        .font(.callout)
                }
            }
        }
        .navigationTitle("Privycs Gateway")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            url = appState.settings.gatewayURL
            apiKey = appState.settings.apiKey
        }
    }

    private func saveAndVerify() async {
        verifying = true; result = nil
        defer { verifying = false }
        var s = appState.settings
        s.gatewayURL = url.trimmingCharacters(in: .whitespaces)
        s.apiKey = apiKey.trimmingCharacters(in: .whitespaces)
        try? await appState.settingsRepo.save(s)
        appState.settings = s

        guard let client = appState.gatewayClient else {
            ok = false; result = "Invalid gateway URL"
            return
        }
        do {
            let configs = try await client.listMyConfigs()
            ok = true
            result = "Connected — \(configs.count) config\(configs.count == 1 ? "" : "s") available"
        } catch {
            ok = false
            result = error.localizedDescription
        }
    }
}
