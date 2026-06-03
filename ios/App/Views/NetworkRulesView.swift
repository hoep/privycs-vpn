import SwiftUI
import PrivycsCore

/// On-Demand & Network Rules screen. Mirror der Android v1.0.5.73+
/// + Desktop NetworkRulesView. Drei sections: Master Toggle, Rules-
/// Liste, Live-Eval-Card.
struct NetworkRulesView: View {
    @EnvironmentObject private var appState: AppState
    @State private var masterEnabled = true
    @State private var showAddRule = false

    var body: some View {
        List {
            Section {
                Toggle("Auto-tunnel master", isOn: $masterEnabled)
                    .onChange(of: masterEnabled) { _, new in
                        Task {
                            var s = appState.settings
                            s.networkRulesEnabled = new
                            try? await appState.settingsRepo.save(s)
                            appState.settings = s
                            await appState.evaluateAndApplyRules()
                        }
                    }
            } footer: {
                Text("When off, no rule is evaluated — VPN is fully manual.")
            }

            Section("Rules (priority top-down)") {
                if appState.rules.isEmpty {
                    Text("No rules yet. Tap + to add.")
                        .foregroundStyle(.secondary)
                        .font(.callout)
                } else {
                    ForEach(appState.rules) { rule in
                        ruleRow(rule)
                    }
                    .onMove(perform: moveRules)
                    .onDelete(perform: deleteRules)
                }
            }

            Section("Current evaluation") {
                liveEvalCard
            }
        }
        .navigationTitle("On-Demand & Network Rules")
        .toolbar {
            ToolbarItem(placement: .topBarLeading) { EditButton() }
            ToolbarItem(placement: .topBarTrailing) {
                Button { showAddRule = true } label: { Image(systemName: "plus") }
            }
        }
        .sheet(isPresented: $showAddRule) {
            AddRuleSheet().environmentObject(appState)
        }
        .task {
            masterEnabled = appState.settings.networkRulesEnabled
            // Configuring SSID rules requires the Wi-Fi SSID, which iOS only
            // hands out once location is granted — prompt here so the app even
            // shows up under Settings ▸ Privacy ▸ Location Services.
            appState.ssidProvider.requestPermissionIfNeeded()
        }
    }

    private func ruleRow(_ rule: NetworkRule) -> some View {
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 4) {
                Text(ruleName(rule)).font(.body)
                Text(actionLabel(rule)).font(.caption2).foregroundStyle(.secondary)
            }
            Spacer()
            Toggle("", isOn: Binding(
                get: { rule.enabled },
                set: { newVal in toggleRule(rule, enabled: newVal) }
            ))
            .labelsHidden()
        }
    }

    private func toggleRule(_ rule: NetworkRule, enabled: Bool) {
        var rules = appState.rules
        guard let idx = rules.firstIndex(where: { $0.id == rule.id }) else { return }
        rules[idx].enabled = enabled
        appState.rules = rules
        Task {
            try? await appState.rulesRepo.save(rules)
            await appState.evaluateAndApplyRules()
        }
    }

    private var liveEvalCard: some View {
        let engine = NetworkRulesEngine()
        let result = engine.evaluate(
            rules: appState.rules,
            state: appState.networkState,
            masterEnabled: masterEnabled
        )
        return VStack(alignment: .leading, spacing: 8) {
            Label(networkLabel(appState.networkState), systemImage: networkIcon(appState.networkState))
                .font(.callout)
            Label(masterEnabled ? "Auto-tunnel: On" : "Auto-tunnel: Off", systemImage: "switch.2")
                .font(.callout)
                .foregroundStyle(masterEnabled ? .primary : .secondary)
            Divider()
            Label(decisionLabel(result, master: masterEnabled), systemImage: "arrow.forward.circle")
                .font(.callout.weight(.medium))
                .foregroundStyle(.tint)
        }
        .padding(.vertical, 4)
    }

    private func ruleName(_ rule: NetworkRule) -> String {
        if !rule.name.isEmpty { return rule.name }
        switch rule.matchType {
        case .any: return "Any network"
        case .networkType:
            switch rule.matchValue.lowercased() {
            case "wifi": return "Wi-Fi"
            case "mobile": return "Mobile"
            case "ethernet": return "Ethernet"
            case "wifi_mobile": return "Wi-Fi or Mobile"
            default: return "Any (online)"
            }
        case .ssidExact: return "Wi-Fi “\(rule.matchValue)”"
        case .ssidPattern: return "Wi-Fi like “\(rule.matchValue)”"
        case .bssid: return "BSSID \(rule.matchValue)"
        }
    }

    private func actionLabel(_ rule: NetworkRule) -> String {
        switch rule.action {
        case .noVpn: return "→ Disconnect VPN"
        case .connectActive: return "→ Connect active selection"
        case .connection: return "→ Connect to \(connectionName(rule.targetId))"
        case .pool: return "→ Activate pool \(poolName(rule.targetId))"
        }
    }

    private func connectionName(_ id: String) -> String {
        appState.connections.first { $0.id == id }?.name ?? String(id.prefix(8))
    }

    private func poolName(_ id: String) -> String {
        appState.pools.first { $0.id == id }?.name ?? String(id.prefix(8))
    }

    private func networkLabel(_ state: NetworkState) -> String {
        switch state.networkType {
        case .wifi: return state.ssid.isEmpty ? "Wi-Fi" : "Wi-Fi (\(state.ssid))"
        case .mobile: return "Mobile data"
        case .ethernet: return "Ethernet"
        case .none: return "No network"
        case .any: return "Network"
        }
    }

    private func networkIcon(_ state: NetworkState) -> String {
        switch state.networkType {
        case .wifi: return "wifi"
        case .mobile: return "antenna.radiowaves.left.and.right"
        case .ethernet: return "cable.connector"
        case .none: return "wifi.slash"
        case .any: return "network"
        }
    }

    private func decisionLabel(_ result: NetworkRulesEngine.Result, master: Bool) -> String {
        if !master { return "Manual control only — engine takes no action" }
        if result.matchedRule == nil { return "No rule matches — engine takes no action" }
        return "Rule matches — engine is acting"
    }

    private func moveRules(from source: IndexSet, to dest: Int) {
        var rules = appState.rules
        rules.move(fromOffsets: source, toOffset: dest)
        appState.rules = rules
        Task { try? await appState.rulesRepo.save(rules) }
    }

    private func deleteRules(_ offsets: IndexSet) {
        var rules = appState.rules
        rules.remove(atOffsets: offsets)
        appState.rules = rules
        Task { try? await appState.rulesRepo.save(rules) }
    }
}
