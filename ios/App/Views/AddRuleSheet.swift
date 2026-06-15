import SwiftUI
import PrivycsCore

/// Add/Edit sheet for a NetworkRule — full parity with Android's rule
/// editor: 5 match types (any / network-type / SSID exact / SSID glob /
/// BSSID) and 4 actions (no-VPN / connect-active / connection / pool).
struct AddRuleSheet: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss

    @State private var name = ""
    @State private var matchType: RuleMatchType = .networkType
    @State private var networkTypeValue = "wifi"     // for .networkType
    @State private var matchValue = ""               // for ssid/bssid
    @State private var action: RuleAction = .connectActive
    @State private var targetID = ""

    var body: some View {
        AdaptiveNavStack {
            Form {
                Section("Name") {
                    TextField("Optional", text: $name)
                }
                Section {
                    Picker("Condition", selection: $matchType) {
                        Text("Any network").tag(RuleMatchType.any)
                        Text("Network type").tag(RuleMatchType.networkType)
                        Text("Wi-Fi name (exact)").tag(RuleMatchType.ssidExact)
                        Text("Wi-Fi name (pattern)").tag(RuleMatchType.ssidPattern)
                        Text("Wi-Fi BSSID (MAC)").tag(RuleMatchType.bssid)
                    }
                    switch matchType {
                    case .networkType:
                        Picker("Type", selection: $networkTypeValue) {
                            Text("Any (online)").tag("any")
                            Text("Wi-Fi").tag("wifi")
                            Text("Mobile").tag("mobile")
                            Text("Ethernet").tag("ethernet")
                            Text("Wi-Fi or Mobile").tag("wifi_mobile")
                        }
                    case .ssidExact:
                        TextField("SSID", text: $matchValue)
                            .textInputAutocapitalization(.never).autocorrectionDisabled()
                    case .ssidPattern:
                        TextField("Pattern (e.g. Cafe*)", text: $matchValue)
                            .textInputAutocapitalization(.never).autocorrectionDisabled()
                    case .bssid:
                        TextField("MAC (AA:BB:CC:DD:EE:FF)", text: $matchValue)
                            .textInputAutocapitalization(.never).autocorrectionDisabled()
                    case .any:
                        EmptyView()
                    }
                } header: {
                    Text("Match")
                } footer: {
                    if matchType == .ssidExact || matchType == .ssidPattern || matchType == .bssid {
                        Text("Wi-Fi name / BSSID rules need Location permission so iOS can read the network name — allow it when prompted (or in Settings ▸ Privacy ▸ Location). Without it these rules can’t match.")
                            .font(.caption2)
                    }
                }
                Section("Action") {
                    Picker("When matched", selection: $action) {
                        Text("Disconnect (no VPN)").tag(RuleAction.noVpn)
                        Text("Connect active selection").tag(RuleAction.connectActive)
                        Text("Connect to connection").tag(RuleAction.connection)
                        Text("Activate pool").tag(RuleAction.pool)
                    }
                    if action == .connection {
                        Picker("Connection", selection: $targetID) {
                            ForEach(appState.connections) { c in Text(c.name).tag(c.id) }
                        }
                    } else if action == .pool {
                        Picker("Pool", selection: $targetID) {
                            ForEach(appState.pools) { p in Text(p.name).tag(p.id) }
                        }
                    }
                }
            }
            .navigationTitle("New Rule")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .pvcsTrailing) { Button("Save") { Task { await save() } } }
                ToolbarItem(placement: .pvcsLeading) { Button("Cancel") { dismiss() } }
            }
        }
    }

    private func save() async {
        let value: String
        switch matchType {
        case .networkType: value = networkTypeValue
        case .ssidExact, .ssidPattern, .bssid: value = matchValue.trimmingCharacters(in: .whitespaces)
        case .any: value = ""
        }
        let rule = NetworkRule(
            id: UUID().uuidString,
            priority: appState.rules.count,
            matchType: matchType,
            matchValue: value,
            action: action,
            targetId: (action == .connection || action == .pool) ? targetID : "",
            enabled: true,
            name: name.trimmingCharacters(in: .whitespaces)
        )
        var rules = appState.rules
        rules.append(rule)
        appState.rules = rules
        try? await appState.rulesRepo.save(rules)
        await appState.onRulesChanged()
        dismiss()
    }
}
