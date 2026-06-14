import SwiftUI
import PrivycsCore

/// tvOS ports of the iOS app's connect/traffic components (those live in the App
/// target, so they're re-declared here against TVColor).

/// Shared byte/speed/uptime formatting for the tvOS screens.
enum TVFormat {
    static func bytes(_ b: Int64) -> String {
        let u = ["B", "KB", "MB", "GB", "TB"]
        var v = Double(max(0, b)); var i = 0
        while v >= 1024 && i < u.count - 1 { v /= 1024; i += 1 }
        return i == 0 ? "\(Int(v)) \(u[i])" : String(format: "%.1f %@", v, u[i])
    }
    static func speed(_ bps: Double) -> String { bytes(Int64(bps)) + "/s" }
    static func uptime(_ s: Int64) -> String {
        let h = s / 3600, m = (s % 3600) / 60, sec = s % 60
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, sec) : String(format: "%02d:%02d", m, sec)
    }
}

/// Asset-catalog name of the protocol brand logo (the imagesets are copied into
/// tvOS/Assets.xcassets). Mirrors VpnProtocol.assetName in PrivycsTheme. The
/// AmneziaWG entry is the REAL logo asset (the design mockup used a placeholder).
func tvProtocolAsset(_ p: VpnProtocol) -> String {
    switch p {
    case .wireguard: return "ic_protocol_wireguard"
    case .openvpn:   return "ic_protocol_openvpn"
    case .ipsec:     return "ic_protocol_strongswan"
    case .amneziawg: return "ic_protocol_amneziawg"
    }
}

/// Brand colour per protocol — mirrors PrivycsColor (WireGuardRed / OpenVpnOrange
/// / IpSecBlue / AmneziaWgIndigo) in the App target's PrivycsTheme.
func tvProtocolColor(_ p: VpnProtocol) -> Color {
    switch p {
    case .wireguard: return Color(red: 0.53, green: 0.09, blue: 0.10)   // #88171A
    case .openvpn:   return Color(red: 0.92, green: 0.49, blue: 0.13)   // #EA7E20
    case .ipsec:     return Color(red: 0.15, green: 0.39, blue: 0.92)   // #2563EB
    case .amneziawg: return Color(red: 0.39, green: 0.40, blue: 0.95)   // #6366F1
    }
}

/// Bar-chart throughput sparkline — port of the iOS SpeedSparkline (gapped,
/// rounded, auto-scaled bars), not a line.
struct TVSpeedSparkline: View {
    let samples: [Double]
    var tint: Color
    var body: some View {
        Canvas { ctx, size in
            guard samples.count > 1 else { return }
            let maxV = max(samples.max() ?? 1, 1)
            let n = samples.count
            let gap: CGFloat = 0.30
            let slot = size.width / CGFloat(n)
            let barW = slot * (1 - gap)
            for (i, v) in samples.enumerated() {
                let h = max(CGFloat(v / maxV) * size.height, 2)
                let x = CGFloat(i) * slot + (slot - barW) / 2
                let rect = CGRect(x: x, y: size.height - h, width: barW, height: h)
                ctx.fill(Path(roundedRect: rect, cornerRadius: barW / 3), with: .color(tint))
            }
        }
    }
}

/// Circular status disc — NON-interactive visual (glow ring, gradient fill, the
/// active protocol's brand logo in the centre). The actual connect/disconnect is a
/// separate standard button beneath it, because custom focusable buttons on tvOS
/// were unreliable to land focus on.
struct TVConnectDisc: View {
    let connected: Bool
    let connecting: Bool
    let activeProtocol: VpnProtocol?

    // Design disc gradient (#1ED4B0 → #0BA98B → #0C8E76) + the teal-tinted mark.
    private static let g1 = Color(red: 0.118, green: 0.831, blue: 0.690)
    private static let g2 = Color(red: 0.043, green: 0.663, blue: 0.545)
    private static let g3 = Color(red: 0.047, green: 0.557, blue: 0.463)
    private static let off1 = Color(red: 0.227, green: 0.278, blue: 0.314)
    private static let off2 = Color(red: 0.133, green: 0.173, blue: 0.200)
    private var markTint: Color { connected ? Self.g2 : Color(red: 0.604, green: 0.655, blue: 0.682) }

    var body: some View {
        ZStack {
            // outer ring
            Circle().stroke((connected ? TVColor.teal : TVColor.onSurfaceVariant).opacity(connected ? 0.45 : 0.25), lineWidth: 2)
                .frame(width: 300, height: 300)
            // gradient disc
            Circle()
                .fill(RadialGradient(colors: connected ? [Self.g1, Self.g2, Self.g3] : [Self.off1, Self.off2],
                                     center: .init(x: 0.5, y: 0.36), startRadius: 6, endRadius: 150))
                .frame(width: 252, height: 252)
                .shadow(color: connected ? Self.g2.opacity(0.6) : .black.opacity(0.5), radius: 32, y: 16)
            // white mark circle with the (real) protocol logo
            Circle().fill(.white).frame(width: 120, height: 120)
                .shadow(color: .black.opacity(0.15), radius: 8, y: 4)
                .overlay {
                    if connecting {
                        ProgressView().controlSize(.large).tint(Self.g2)
                    } else if let p = activeProtocol {
                        Image(tvProtocolAsset(p)).renderingMode(.template).resizable().scaledToFit()
                            .frame(width: 74, height: 74).foregroundStyle(markTint)
                    } else {
                        Image(systemName: connected ? "checkmark.shield.fill" : "shield")
                            .font(.system(size: 60, weight: .light)).foregroundStyle(markTint)
                    }
                }
        }
        .frame(width: 300, height: 300)
    }
}

/// Connect-screen pool/rotation card — port of the iOS PoolIndicatorCard: pool
/// name + policy, the current server, a live countdown to the next rotation, and
/// a Rotate-now button.
struct TVPoolStatusCard: View {
    let poolName: String
    let policy: PoolPolicy
    let memberName: String
    let memberCountry: String
    let nextRotationAt: Int64
    let onRotateNow: () -> Void

    @State private var now = Date()
    private let ticker = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    private var countdown: String? {
        guard nextRotationAt > 0 else { return nil }
        let r = nextRotationAt - Int64(now.timeIntervalSince1970)
        if r <= 0 { return "…" }
        return String(format: "%d:%02d", r / 60, r % 60)
    }

    private var policyLabel: String {
        switch policy {
        case .geoNearest: return loc("tv.pool.policy_geo")
        case .random:     return loc("tv.pool.policy_random")
        case .roundRobin: return loc("tv.pool.policy_rr")
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 12) {
                Image(systemName: "square.stack.3d.up.fill").foregroundStyle(TVColor.teal)
                VStack(alignment: .leading, spacing: 2) {
                    Text(poolName).font(TVFont.sans(20, .semibold)).foregroundStyle(TVColor.onSurface).lineLimit(1)
                    Text(policyLabel).font(TVFont.mono(14)).foregroundStyle(TVColor.onSurfaceVariant)
                }
                Spacer()
                if let countdown {
                    VStack(spacing: 2) {
                        Text(countdown).font(TVFont.mono(20, .semibold)).foregroundStyle(TVColor.teal).monospacedDigit()
                        Text(loc("tv.pool.next")).font(TVFont.mono(12)).foregroundStyle(TVColor.onSurfaceVariant)
                    }
                }
            }
            HStack(spacing: 12) {
                Image(systemName: "dot.radiowaves.left.and.right").foregroundStyle(TVColor.teal)
                Text(memberName.isEmpty ? "—" : memberName).font(TVFont.sans(18)).foregroundStyle(TVColor.onSurface).lineLimit(1)
                if !memberCountry.isEmpty {
                    Text(memberCountry.uppercased()).font(TVFont.mono(13)).foregroundStyle(TVColor.onSurfaceVariant)
                }
                Spacer()
                Button { onRotateNow() } label: {
                    Label(loc("tv.pool.rotate_now"), systemImage: "arrow.triangle.2.circlepath")
                        .font(TVFont.sans(16, .semibold)).foregroundStyle(TVColor.teal)
                        .padding(.vertical, 9).padding(.horizontal, 16)
                }
                .buttonStyle(.card)
            }
        }
        .padding(24).frame(maxWidth: .infinity)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 22))
        .overlay(RoundedRectangle(cornerRadius: 22).stroke(TVColor.outline.opacity(0.5), lineWidth: 1))
        .onReceive(ticker) { now = $0 }
    }
}

/// Tunnel-health pill — port of the iOS TunnelHealthPill (dot + label, tinted
/// capsule). tvOS derives the level from connection + handshake age.
enum TVHealthLevel { case none, healthy, degraded }

struct TVHealthPill: View {
    let level: TVHealthLevel
    private var color: Color {
        switch level {
        case .healthy:  return TVColor.tealFill
        case .degraded: return Color(red: 0.96, green: 0.62, blue: 0.04)   // warning
        case .none:     return TVColor.onSurfaceVariant
        }
    }
    private var label: String {
        switch level {
        case .healthy:  return loc("tv.health.healthy")
        case .degraded: return loc("tv.health.degraded")
        case .none:     return ""
        }
    }
    var body: some View {
        HStack(spacing: 8) {
            Circle().fill(color).frame(width: 10, height: 10)
            Text(label).font(.system(size: 18, weight: .medium)).foregroundStyle(color)
        }
        .padding(.horizontal, 14).padding(.vertical, 6)
        .background(Capsule().fill(color.opacity(0.14)))
    }
}
