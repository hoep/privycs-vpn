import SwiftUI
import PrivycsCore

/// Pause the VPN for a fixed duration or until manually resumed —
/// port of Android's ManualPauseSheet. While paused, on-demand and all
/// network-rule automation are frozen; a timed pause auto-resumes.
struct ManualPauseSheet: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss

    /// Preset durations (seconds) — nil = pause until the user resumes.
    private let presets: [(label: String, seconds: TimeInterval?)] = [
        (String(localized: "5 minutes"), 5 * 60),
        (String(localized: "15 minutes"), 15 * 60),
        (String(localized: "30 minutes"), 30 * 60),
        (String(localized: "1 hour"), 60 * 60),
        (String(localized: "Until I resume"), nil),
    ]

    var body: some View {
        AdaptiveNavStack {
            List {
                Section {
                    ForEach(presets, id: \.label) { preset in
                        Button {
                            Task {
                                await appState.pause(seconds: preset.seconds)
                                dismiss()
                            }
                        } label: {
                            HStack {
                                Image(systemName: preset.seconds == nil ? "pause.circle" : "clock")
                                    .foregroundStyle(PrivycsColor.teal)
                                Text(preset.label)
                                Spacer()
                            }
                        }
                        .buttonStyle(.plain)
                    }
                } header: {
                    Text("Pause for")
                } footer: {
                    Text("The VPN disconnects and stays off — network rules and on-demand are paused too. A timed pause reconnects your current selection automatically.")
                }
            }
            .navigationTitle("Pause VPN")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) { Button("Cancel") { dismiss() } }
            }
        }
    }
}
