import SwiftUI
import PrivycsCore

/// Configure a pool on the Apple TV — parity with the iPhone PoolDetailView:
/// selection policy, auto-rotation interval, DNS override, and per-server
/// management (use / delete). Edits persist through the shared PoolRepository so
/// the same rotation engine honours them.
struct TVPoolDetailView: View {
    @EnvironmentObject private var state: TVAppState
    @Environment(\.dismiss) private var dismiss
    let poolID: String

    @State private var name = ""
    @State private var policy: PoolPolicy = .roundRobin
    @State private var rotationSec = 0
    @State private var dns = ""
    @State private var members: [PoolMember] = []
    @State private var activeMemberID = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 10) {
                Text("[ POOL ]")
                    .font(TVFont.mono(15)).tracking(3).foregroundStyle(TVColor.teal)
                Text(name).font(TVFont.sans(46, .bold)).foregroundStyle(TVColor.onSurface).lineLimit(1)
            }
            .padding(.bottom, 30)

            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    TVSettingsBlock(title: loc("tv.pool.policy")) {
                        TVSegmented(options: [(PoolPolicy.geoNearest, loc("tv.pool.policy_geo")),
                                              (PoolPolicy.random, loc("tv.pool.policy_random")),
                                              (PoolPolicy.roundRobin, loc("tv.pool.policy_rr"))],
                                    selection: $policy) { _ in persist() }
                    }
                    TVSettingsBlock(title: loc("tv.pool.rotation")) {
                        TVSegmented(options: [(0, loc("tv.pool.rot_off")), (300, loc("tv.pool.rot_5m")),
                                              (900, loc("tv.pool.rot_15m")), (3600, loc("tv.pool.rot_1h")),
                                              (14400, loc("tv.pool.rot_4h")), (86400, loc("tv.pool.rot_daily"))],
                                    selection: $rotationSec) { _ in persist() }
                    }
                    TVSettingsBlock(title: "DNS", description: loc("tv.settings.dns_hint2")) {
                        TextField(loc("tv.settings.dns_placeholder"), text: $dns)
                            .font(TVFont.mono(21))
                            .onChange(of: dns) { _, _ in persist() }
                    }

                    Text("\(loc("tv.pool.members")) (\(members.count))")
                        .font(TVFont.mono(15)).tracking(2).foregroundStyle(TVColor.onSurfaceVariant).padding(.top, 8)
                    ForEach(members) { m in memberRow(m) }
                }
            }
        }
        .padding(60)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(LinearGradient(colors: [TVColor.backgroundTop, TVColor.background],
                                   startPoint: .top, endPoint: .bottom).ignoresSafeArea())
        .focusSection()
        .onAppear(perform: load)
    }

    private func memberRow(_ m: PoolMember) -> some View {
        let isActive = m.id == activeMemberID
        return HStack(spacing: 14) {
            Image(tvProtocolAsset(m.protocol)).renderingMode(.template).resizable().scaledToFit()
                .frame(width: 30, height: 30).foregroundStyle(tvProtocolColor(m.protocol))
            VStack(alignment: .leading, spacing: 2) {
                Text(m.name).font(TVFont.sans(20, .semibold)).foregroundStyle(TVColor.onSurface).lineLimit(1)
                if !m.serverAddress.isEmpty {
                    Text(m.serverAddress).font(TVFont.mono(14)).foregroundStyle(TVColor.onSurfaceVariant).lineLimit(1)
                }
            }
            Spacer(minLength: 12)
            if isActive {
                Label(loc("tv.pool.current"), systemImage: "dot.radiowaves.left.and.right")
                    .font(TVFont.mono(14)).foregroundStyle(TVColor.teal).labelStyle(.titleOnly)
            } else {
                TVActionButton(title: loc("tv.pool.set_active"), icon: "checkmark.circle") {
                    activeMemberID = m.id; persist()
                }
            }
            Button(role: .destructive) {
                members.removeAll { $0.id == m.id }
                if activeMemberID == m.id { activeMemberID = members.first?.id ?? "" }
                persist()
            } label: { Image(systemName: "trash").font(.system(size: 22)).foregroundStyle(TVColor.error).padding(14) }
            .buttonStyle(.card)
            .disabled(members.count <= 1)
        }
        .padding(.vertical, 16).padding(.horizontal, 22)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 16))
        .overlay(RoundedRectangle(cornerRadius: 16).stroke(isActive ? TVColor.teal : TVColor.outline, lineWidth: isActive ? 2 : 1))
    }

    private func load() {
        guard let p = state.pools.first(where: { $0.id == poolID }) else { dismiss(); return }
        name = p.name
        policy = p.policy
        rotationSec = p.rotation?.intervalSeconds ?? 0
        dns = p.dnsOverride
        members = p.members
        activeMemberID = p.activeMemberID.isEmpty ? (p.members.first?.id ?? "") : p.activeMemberID
    }

    private func persist() {
        guard var p = state.pools.first(where: { $0.id == poolID }) else { return }
        p.policy = policy
        p.dnsOverride = dns.trimmingCharacters(in: .whitespaces)
        p.members = members
        p.activeMemberID = activeMemberID
        if rotationSec == 0 {
            p.rotation = nil
        } else {
            var r = p.rotation ?? PoolRotation()
            r.intervalSeconds = rotationSec
            p.rotation = r
        }
        let pool = p
        Task { await state.savePool(pool) }
    }
}
