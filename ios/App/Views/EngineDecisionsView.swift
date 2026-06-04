import SwiftUI
import Combine
import PrivycsCore

/// Live "what the engine decided & why" panel (v1.0.9). Shown in place of the
/// manual Protocol-Failover-Order screen when Automatic protocol selection is
/// on. Polls the shadow engine every 4 s and renders each decision's localized
/// string — the same i18n keys (and wording) the desktop SettingsView uses.
struct EngineDecisionsView: View {
    @EnvironmentObject private var appState: AppState
    @State private var decisions: [EngineDecision] = []

    private let ticker = Timer.publish(every: 4, on: .main, in: .common).autoconnect()

    var body: some View {
        Form {
            Section {
                Text("Let the engine choose and recover the best protocol for the current network.")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }

            Section {
                if decisions.isEmpty {
                    Text("No decisions yet — connect to see what the engine chooses and why.")
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(Array(decisions.reversed().prefix(20))) { d in
                        Text(decisionText(d))
                            .font(.callout)
                    }
                }
            } header: {
                Text("Engine decisions")
            } footer: {
                Text("The engine decides the protocol automatically. The manual order is hidden while this is on.")
            }
        }
        .navigationTitle("Engine decisions")
        .onAppear { refresh() }
        .onReceive(ticker) { _ in refresh() }
    }

    private func refresh() { decisions = appState.engineShadow.decisions() }

    /// Maps the engine's stable i18n key to the localized string. Arg strings
    /// use String(localized:) interpolation, whose generated catalog key is the
    /// "%@" form (e.g. "Connecting via %@…").
    private func decisionText(_ d: EngineDecision) -> String {
        let arg = d.args.first ?? ""
        switch d.key {
        case "decision.connecting": return String(localized: "Connecting via \(arg)…")
        case "decision.validating": return String(localized: "Verifying connectivity…")
        case "decision.connected": return String(localized: "Connected via \(arg)")
        case "decision.degraded": return String(localized: "Connection degraded — monitoring")
        case "decision.recovered": return String(localized: "Connection recovered")
        case "decision.recover_wait": return String(localized: "Recovering: waiting for the link to settle")
        case "decision.recover_revalidate": return String(localized: "Recovering: re-checking connectivity")
        case "decision.recover_restart": return String(localized: "Recovering: restarting the tunnel")
        case "decision.switching": return String(localized: "Switching protocol to \(arg)")
        case "decision.backoff": return String(localized: "Connection failed — backing off before retry")
        case "decision.captive": return String(localized: "Captive portal detected — sign in to continue")
        case "decision.roam": return String(localized: "Network changed — re-validating")
        case "decision.disconnected": return String(localized: "Disconnected")
        case "decision.suspended": return String(localized: "Paused (system sleep)")
        case "decision.resumed_idle": return String(localized: "Resumed")
        case "decision.resumed_revalidate": return String(localized: "Resumed — re-validating")
        case "decision.no_profile": return String(localized: "No usable protocol available")
        default: return d.to
        }
    }
}
