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

    init(pool: Pool) {
        self.pool = pool
        self._policy = State(initialValue: pool.policy)
        self._rotationInterval = State(initialValue: pool.rotation?.intervalSeconds ?? 0)
        self._splitMode = State(initialValue: pool.splitTunnel?.mode ?? .off)
        self._splitCidrText = State(initialValue: (pool.splitTunnel?.cidrs ?? []).joined(separator: ", "))
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
                .onChange(of: policy) { _, new in persistPolicy(new) }
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
                .onChange(of: rotationInterval) { _, new in persistRotation(new) }
            }

            Section {
                Picker("Split tunnel", selection: $splitMode) {
                    Text("Off").tag(PoolSplitTunnel.SplitTunnelMode.off)
                    Text("Include only").tag(PoolSplitTunnel.SplitTunnelMode.includeOnly)
                    Text("Exclude listed").tag(PoolSplitTunnel.SplitTunnelMode.excludeListed)
                }
                .onChange(of: splitMode) { _, _ in persistSplit() }
                if splitMode != .off {
                    TextField("CIDRs (comma-separated)", text: $splitCidrText, axis: .vertical)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .font(.system(size: 13, design: .monospaced))
                        .onSubmit { persistSplit() }
                }
            } header: {
                Text("Split tunnel")
            } footer: {
                Text(splitMode == .includeOnly
                     ? "Only the listed networks route through the VPN."
                     : splitMode == .excludeListed
                       ? "All traffic routes through the VPN except the listed networks."
                       : "All traffic routes through the VPN.")
            }

            Section("Members (\(pool.members.count))") {
                if let activeID = pool.activeMemberID.isEmpty ? nil : pool.activeMemberID,
                   let active = pool.members.first(where: { $0.id == activeID }) {
                    HStack {
                        Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                        Text(active.name).font(.body)
                        Spacer()
                        Text(active.country).font(.caption).foregroundStyle(.secondary)
                    }
                }
                ForEach(pool.members.filter { $0.id != pool.activeMemberID }) { m in
                    HStack {
                        Text(m.name)
                        Spacer()
                        Text(m.country).font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
        }
        .navigationTitle(pool.name)
    }

    private func persistPolicy(_ new: PoolPolicy) {
        var p = pool
        p.policy = new
        Task { try? await appState.poolRepo.save(p) }
    }

    private func persistRotation(_ seconds: Int) {
        var p = pool
        if seconds == 0 {
            p.rotation = nil
        } else {
            var r = p.rotation ?? PoolRotation()
            r.intervalSeconds = seconds
            p.rotation = r
        }
        Task { try? await appState.poolRepo.save(p) }
    }

    private func persistSplit() {
        var p = pool
        let cidrs = splitCidrText
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        p.splitTunnel = splitMode == .off ? nil : PoolSplitTunnel(mode: splitMode, cidrs: cidrs)
        Task {
            try? await appState.poolRepo.save(p)
            appState.pools = (try? await appState.poolRepo.loadAll()) ?? appState.pools
        }
    }
}
