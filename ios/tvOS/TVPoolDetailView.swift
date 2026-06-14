import SwiftUI
import PrivycsCore

/// Configure a pool on the Apple TV — parity with the iPhone PoolDetailView:
/// rename, selection policy, auto-rotation interval, DNS override. The member
/// list is intentionally NOT rendered here — pools can hold hundreds of servers
/// and drawing them made this screen crawl; the rotation engine manages them.
struct TVPoolDetailView: View {
    @EnvironmentObject private var state: TVAppState
    @Environment(\.dismiss) private var dismiss
    let poolID: String

    @State private var name = ""
    @State private var policy: PoolPolicy = .roundRobin
    @State private var rotationSec = 0
    @State private var dns = ""

    private var memberCount: Int { state.pools.first(where: { $0.id == poolID })?.members.count ?? 0 }
    private var isActive: Bool { state.activePool?.id == poolID && state.status.connected }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 10) {
                Text("[ POOL ]").font(TVFont.mono(15)).tracking(3).foregroundStyle(TVColor.teal)
                Text(name.isEmpty ? loc("tv.configs.pools") : name)
                    .font(TVFont.sans(46, .bold)).foregroundStyle(TVColor.onSurface).lineLimit(1)
                Text("\(memberCount) \(loc("tv.pool.members"))")
                    .font(TVFont.mono(16)).foregroundStyle(TVColor.onSurfaceVariant)
            }
            .padding(.bottom, 30)

            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    TVSettingsBlock(title: loc("tv.pool.name")) {
                        TextField(loc("tv.pool.name"), text: $name)
                            .font(TVFont.sans(22))
                            .onSubmit { persist(reconnect: false) }
                    }
                    TVSettingsBlock(title: loc("tv.pool.policy")) {
                        TVSegmented(options: [(PoolPolicy.geoNearest, loc("tv.pool.policy_geo")),
                                              (PoolPolicy.random, loc("tv.pool.policy_random")),
                                              (PoolPolicy.roundRobin, loc("tv.pool.policy_rr"))],
                                    selection: $policy) { _ in persist(reconnect: true) }
                    }
                    TVSettingsBlock(title: loc("tv.pool.rotation")) {
                        TVSegmented(options: [(0, loc("tv.pool.rot_off")), (300, loc("tv.pool.rot_5m")),
                                              (900, loc("tv.pool.rot_15m")), (3600, loc("tv.pool.rot_1h")),
                                              (14400, loc("tv.pool.rot_4h")), (86400, loc("tv.pool.rot_daily"))],
                                    selection: $rotationSec) { _ in persist(reconnect: true) }
                    }
                    TVSettingsBlock(title: "DNS", description: loc("tv.settings.dns_hint2")) {
                        TextField(loc("tv.settings.dns_placeholder"), text: $dns)
                            .font(TVFont.mono(21))
                            .onSubmit { persist(reconnect: true) }
                    }

                    Button {
                        state.selectPool(poolID)
                        Task { if let p = state.pools.first(where: { $0.id == poolID }) { await state.connectPool(p) }; dismiss() }
                    } label: {
                        Label(loc(isActive ? "tv.pool.active" : "tv.pool.activate"),
                              systemImage: isActive ? "checkmark.circle.fill" : "bolt.circle")
                            .font(TVFont.sans(24, .semibold)).foregroundStyle(TVColor.teal)
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 16)
                    }
                    .buttonStyle(.card)
                    .padding(.top, 8)
                }
                // Side + bottom breathing room so focused .card buttons aren't
                // clipped by the ScrollView bounds (tvOS focus lift) and the last
                // control clears the bottom overscan.
                .padding(.horizontal, 6)
                .padding(.bottom, 80)
            }
        }
        .padding(.horizontal, 80).padding(.top, 60).padding(.bottom, 40)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .tvScreenChrome(theme: state.settings.theme)
        .focusSection()
        .onAppear(perform: load)
    }

    private func load() {
        guard let p = state.pools.first(where: { $0.id == poolID }) else { dismiss(); return }
        name = p.name
        policy = p.policy
        rotationSec = p.rotation?.intervalSeconds ?? 0
        dns = p.dnsOverride
    }

    private func persist(reconnect: Bool) {
        guard var p = state.pools.first(where: { $0.id == poolID }) else { return }
        p.name = name.trimmingCharacters(in: .whitespaces).isEmpty ? p.name : name.trimmingCharacters(in: .whitespaces)
        p.policy = policy
        p.dnsOverride = dns.trimmingCharacters(in: .whitespaces)
        if rotationSec == 0 {
            p.rotation = nil
        } else {
            var r = p.rotation ?? PoolRotation()
            r.intervalSeconds = rotationSec
            p.rotation = r
        }
        let pool = p
        Task { await state.savePool(pool, reconnect: reconnect) }
    }
}
