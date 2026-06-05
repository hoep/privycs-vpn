import SwiftUI
import PrivycsCore

struct PoolDetailView: View {
    @EnvironmentObject private var appState: AppState
    let pool: Pool
    @State private var policy: PoolPolicy
    @State private var rotationInterval: Int = 0
    @State private var memberPickerShown = false
    @State private var splitMode: PoolSplitTunnel.SplitTunnelMode
    @State private var splitCidrText: String
    @State private var excludePrivate: Bool
    @State private var dnsOverride: String

    init(pool: Pool) {
        self.pool = pool
        self._policy = State(initialValue: pool.policy)
        self._rotationInterval = State(initialValue: pool.rotation?.intervalSeconds ?? 0)
        self._splitMode = State(initialValue: pool.splitTunnel?.mode ?? .off)
        self._splitCidrText = State(initialValue: (pool.splitTunnel?.bypassCidrs ?? []).joined(separator: ", "))
        self._excludePrivate = State(initialValue: pool.splitTunnel?.excludePrivateNetworks ?? false)
        self._dnsOverride = State(initialValue: pool.dnsOverride)
    }

    var body: some View {
        Form {
            Section {
                Button {
                    Task {
                        appState.selectedTargetID = "pool:\(pool.id)"
                        await appState.connectPool(pool)
                    }
                } label: {
                    Label(
                        appState.activePool?.id == pool.id && appState.status.connected
                            ? "Pool active" : "Activate pool",
                        systemImage: appState.activePool?.id == pool.id && appState.status.connected
                            ? "checkmark.circle.fill" : "bolt.circle"
                    )
                    .foregroundStyle(PrivycsColor.teal)
                }
                .disabled(pool.members.isEmpty)

                if let mem = appState.activePoolMember, appState.activePool?.id == pool.id {
                    HStack {
                        Text("Current member").foregroundStyle(.secondary)
                        Spacer()
                        Text("\(mem.name) \(mem.country.uppercased())")
                            .font(.system(size: 13))
                    }
                }
            }

            Section("Policy") {
                Picker("Selection policy", selection: $policy) {
                    ForEach(PoolPolicy.allCases) { p in
                        Text(p.displayName).tag(p)
                    }
                }
                .onChange(of: policy) { _ in persist() }
            }

            Section("Rotation") {
                Picker("Auto-rotate every", selection: $rotationInterval) {
                    Text("Off").tag(0)
                    Text("5 minutes").tag(300)
                    Text("15 minutes").tag(900)
                    Text("1 hour").tag(3600)
                    Text("4 hours").tag(14400)
                    Text("Daily").tag(86400)
                }
                .onChange(of: rotationInterval) { _ in persist() }
            }

            Section {
                Picker("Split tunnel", selection: $splitMode) {
                    Text("Off").tag(PoolSplitTunnel.SplitTunnelMode.off)
                    Text("Bypass listed").tag(PoolSplitTunnel.SplitTunnelMode.excludeListed)
                }
                .onChange(of: splitMode) { _ in persist() }
                if splitMode != .off {
                    TextField("Bypass CIDRs (comma-separated)", text: $splitCidrText, axis: .vertical)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .font(.system(size: 13, design: .monospaced))
                        .onSubmit { persist() }
                    Toggle("Also bypass private networks", isOn: $excludePrivate)
                        .onChange(of: excludePrivate) { _ in persist() }
                }
            } header: {
                Text("Split tunnel")
            } footer: {
                Text(splitMode == .excludeListed
                     ? "All traffic routes through the VPN except the listed networks (which bypass it)."
                     : "All traffic routes through the VPN.")
            }

            Section {
                DnsField(value: $dnsOverride, onCommit: { persist() })
            } header: {
                Text("DNS override")
            } footer: {
                Text("DNS servers for this pool. Takes precedence over the global setting. Empty = use the global setting.")
            }

            Section("Members (\(pool.members.count))") {
                if let activeID = pool.activeMemberID.isEmpty ? nil : pool.activeMemberID,
                   let active = pool.members.first(where: { $0.id == activeID }) {
                    HStack {
                        Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                        Text(active.name).font(.body)
                        Spacer()
                        Text(flagLabel(active.country)).font(.caption).foregroundStyle(.secondary)
                    }
                }
                ForEach(pool.members.filter { $0.id != pool.activeMemberID }) { m in
                    HStack {
                        Text(m.name)
                        Spacer()
                        Text(flagLabel(m.country)).font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
        }
        .navigationTitle(pool.name)
    }

    /// "🇩🇪 DE" — flag emoji + code, or just the code when no flag resolves.
    private func flagLabel(_ cc: String) -> String {
        let flag = PoolHostnameLabels.flagEmoji(cc)
        return flag.isEmpty ? cc.uppercased() : "\(flag) \(cc.uppercased())"
    }

    /// Single writer — builds the pool from ALL current edit state so a
    /// later change can't revert an earlier one (each setter used to start
    /// from the original snapshot and silently drop the other fields).
    private func persist() {
        var p = pool
        p.policy = policy
        if rotationInterval == 0 {
            p.rotation = nil
        } else {
            var r = p.rotation ?? PoolRotation()
            r.intervalSeconds = rotationInterval
            p.rotation = r
        }
        let cidrs = splitCidrText
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        p.splitTunnel = splitMode == .off
            ? nil
            : PoolSplitTunnel(bypassCidrs: cidrs, excludePrivateNetworks: excludePrivate)
        p.dnsOverride = dnsOverride.trimmingCharacters(in: .whitespaces)
        Task {
            try? await appState.poolRepo.save(p)
            appState.pools = (try? await appState.poolRepo.loadAll()) ?? appState.pools
        }
    }
}
