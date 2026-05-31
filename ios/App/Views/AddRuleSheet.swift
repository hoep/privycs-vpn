import SwiftUI
import PrivycsCore

/// Modal-style Add/Edit-Sheet für eine NetworkRule.
struct AddRuleSheet: View {
    @EnvironmentObject private var appState: AppState
    @Environment(\.dismiss) private var dismiss

    @State private var name = ""
    @State private var networkType: NetworkRule.Match.NetworkType = .wifi
    @State private var ssidMode: NetworkRule.Match.SSIDMode = .all
    @State private var ssidListText = ""
    @State private var actionPick: ActionPick = .disconnect
    @State private var actionTargetID = ""

    enum ActionPick: String, CaseIterable {
        case disconnect = "Disconnect"
        case keepAsIs = "Keep as-is"
        case connectConnection = "Connect to connection"
        case connectPool = "Activate pool"
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Name") {
                    TextField("Optional", text: $name)
                }
                Section("Match") {
                    Picker("Network type", selection: $networkType) {
                        Text("Any").tag(NetworkRule.Match.NetworkType.any)
                        Text("Wi-Fi").tag(NetworkRule.Match.NetworkType.wifi)
                        Text("Mobile").tag(NetworkRule.Match.NetworkType.mobile)
                        Text("Ethernet").tag(NetworkRule.Match.NetworkType.ethernet)
                        Text("Offline").tag(NetworkRule.Match.NetworkType.none)
                    }
                    if networkType == .wifi {
                        Picker("SSID", selection: $ssidMode) {
                            Text("Any SSID").tag(NetworkRule.Match.SSIDMode.all)
                            Text("Only listed").tag(NetworkRule.Match.SSIDMode.only)
                            Text("Except listed").tag(NetworkRule.Match.SSIDMode.except)
                        }
                        if ssidMode != .all {
                            TextField("SSIDs (comma-separated)", text: $ssidListText)
                        }
                    }
                }
                Section("Action") {
                    Picker("When matched", selection: $actionPick) {
                        ForEach(ActionPick.allCases, id: \.self) { p in
                            Text(p.rawValue).tag(p)
                        }
                    }
                    if actionPick == .connectConnection {
                        Picker("Connection", selection: $actionTargetID) {
                            ForEach(appState.connections) { c in
                                Text(c.name).tag(c.id)
                            }
                        }
                    } else if actionPick == .connectPool {
                        Picker("Pool", selection: $actionTargetID) {
                            ForEach(appState.pools) { p in
                                Text(p.name).tag(p.id)
                            }
                        }
                    }
                }
            }
            .navigationTitle("New Rule")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("Save") { Task { await save() } }
                }
                ToolbarItem(placement: .topBarLeading) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
    }

    private func save() async {
        let action: NetworkRule.Action
        switch actionPick {
        case .disconnect: action = .disconnect
        case .keepAsIs: action = .keepAsIs
        case .connectConnection: action = .connectToConnection(connectionID: actionTargetID)
        case .connectPool: action = .connectToPool(poolID: actionTargetID)
        }
        let ssidList = ssidListText
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        let rule = NetworkRule(
            id: UUID().uuidString,
            name: name,
            match: NetworkRule.Match(
                networkType: networkType,
                ssidMode: ssidMode,
                ssidList: ssidList
            ),
            action: action
        )
        var rules = appState.rules
        rules.append(rule)
        appState.rules = rules
        try? await appState.rulesRepo.save(rules)
        dismiss()
    }
}
