import SwiftUI
import PrivycsCore

/// tvOS ports of the iOS app's connect/traffic components (those live in the App
/// target, so they're re-declared here against TVColor).

/// Asset-catalog name of the protocol brand logo (the imagesets are copied into
/// tvOS/Assets.xcassets). Mirrors VpnProtocol.assetName in PrivycsTheme.
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

    private var ring: Color { connected ? TVColor.teal : TVColor.onSurfaceVariant }

    var body: some View {
        ZStack {
            // soft outer glow ring (depth at 10-foot distance)
            Circle()
                .stroke(ring.opacity(connected ? 0.45 : 0.18), lineWidth: 6)
                .frame(width: 232, height: 232)
                .blur(radius: 6)
            Circle()
                .fill(LinearGradient(
                    colors: connected
                        ? [TVColor.teal.opacity(0.34), TVColor.teal.opacity(0.10)]
                        : [TVColor.surfaceVariant, TVColor.surface],
                    startPoint: .topLeading, endPoint: .bottomTrailing))
                .frame(width: 200, height: 200)
                .overlay(Circle().stroke(ring.opacity(0.7), lineWidth: 3))
                .shadow(color: ring.opacity(connected ? 0.5 : 0.0), radius: 34)
            if connecting {
                ProgressView().controlSize(.large).tint(TVColor.teal)
            } else if let p = activeProtocol {
                Image(tvProtocolAsset(p))
                    .renderingMode(.template).resizable().scaledToFit()
                    .frame(width: 88, height: 88)
                    .foregroundStyle(ring)
            } else {
                Image(systemName: connected ? "checkmark.shield.fill" : "shield")
                    .font(.system(size: 88, weight: .light))
                    .foregroundStyle(ring)
            }
        }
        .frame(width: 220, height: 220)
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
        case .healthy:  return String(localized: "tv.health.healthy", defaultValue: "Healthy")
        case .degraded: return String(localized: "tv.health.degraded", defaultValue: "Degraded")
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
