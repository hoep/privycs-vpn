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
                        HStack(alignment: .top, spacing: 8) {
                            Text(shortTime(d.at))
                                .font(PrivycsFont.mono(12, relativeTo: .caption))
                                .foregroundStyle(.secondary)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(decisionText(d))
                                    .font(.callout)
                                if let reason = reasonText(d) {
                                    Text(reason)
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                        }
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

    private static let isoParser = ISO8601DateFormatter()
    private static let hms: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "HH:mm:ss"
        return f
    }()

    /// "20:45:03" from the decision's RFC3339 timestamp (empty if unparseable).
    private func shortTime(_ at: String) -> String {
        guard let d = Self.isoParser.date(from: at) else { return "" }
        return Self.hms.string(from: d)
    }

    /// Network-aware reason line, with the country name resolved from its code.
    /// nil when the decision carries no reason.
    private func reasonText(_ d: EngineDecision) -> String? {
        guard !d.reason.isEmpty, let code = d.reasonArgs.first else { return nil }
        let country = PoolHostnameLabels.countryNameFromCode(code)
        if country.isEmpty { return nil }
        switch d.reason {
        case "reason.country_open":
            return loc("\(country): no widespread VPN blocking — a fast protocol is fine here.")
        case "reason.country_restrictive_awg":
            return loc("\(country) is known for DPI/censorship — AmneziaWG’s obfuscation is the right protocol here.")
        case "reason.country_restrictive_use_awg":
            return loc("\(country) censors VPN traffic — AmneziaWG (obfuscated) is available; using it avoids detection.")
        case "reason.country_restrictive_no_awg":
            return loc("\(country) censors VPN traffic — no AmneziaWG profile here, so this protocol may be blocked.")
        default:
            return nil
        }
    }

    /// Map a protocol token to its proper brand label (never localized).
    private func protoLabel(_ token: String) -> String {
        switch token.lowercased() {
        case "wireguard": return "WireGuard"
        case "amneziawg", "amnezia": return "AmneziaWG"
        case "openvpn": return "OpenVPN"
        case "ipsec": return "IPSec"
        default: return token
        }
    }

    /// Maps the engine's stable i18n key to the localized string. Arg strings
    /// use loc() interpolation, whose generated catalog key is the
    /// "%@" form (e.g. "Connecting via %@…").
    private func decisionText(_ d: EngineDecision) -> String {
        let arg = protoLabel(d.args.first ?? "")
        switch d.key {
        case "decision.connecting": return loc("Connecting via \(arg)…")
        case "decision.validating": return loc("Verifying connectivity…")
        case "decision.connected": return loc("Connected via \(arg)")
        case "decision.degraded": return loc("Connection degraded — monitoring")
        case "decision.recovered": return loc("Connection recovered")
        case "decision.recover_wait": return loc("Recovering: waiting for the link to settle")
        case "decision.recover_revalidate": return loc("Recovering: re-checking connectivity")
        case "decision.recover_restart": return loc("Recovering: restarting the tunnel")
        case "decision.switching": return loc("Switching protocol to \(arg)")
        case "decision.backoff": return loc("Connection failed — backing off before retry")
        case "decision.captive": return loc("Captive portal detected — sign in to continue")
        case "decision.roam": return loc("Network changed — re-validating")
        case "decision.disconnected": return loc("Disconnected")
        case "decision.suspended": return loc("Paused (system sleep)")
        case "decision.resumed_idle": return loc("Resumed")
        case "decision.resumed_revalidate": return loc("Resumed — re-validating")
        case "decision.no_profile": return loc("No usable protocol available")
        default: return d.to
        }
    }
}
