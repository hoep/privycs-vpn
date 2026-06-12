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

/// Big circular connect/disc — port of the iOS ConnectButton: glow ring, gradient
/// fill, the active protocol's brand logo in the centre (shield when idle).
struct TVConnectDisc: View {
    let connected: Bool
    let connecting: Bool
    let activeProtocol: VpnProtocol?
    let onTap: () -> Void

    private var ring: Color { connected ? TVColor.teal : TVColor.onSurfaceVariant }

    var body: some View {
        Button(action: onTap) {
            ZStack {
                Circle().stroke(ring.opacity(connected ? 0.55 : 0.30), lineWidth: 4)
                    .frame(width: 224, height: 224)
                Circle()
                    .fill(LinearGradient(
                        colors: connected
                            ? [TVColor.teal.opacity(0.32), TVColor.teal.opacity(0.12)]
                            : [TVColor.surfaceVariant, TVColor.surface],
                        startPoint: .topLeading, endPoint: .bottomTrailing))
                    .frame(width: 196, height: 196)
                    .overlay(Circle().stroke(ring.opacity(0.6), lineWidth: 2))
                if connecting {
                    ProgressView().controlSize(.large).tint(TVColor.teal)
                } else if let p = activeProtocol {
                    Image(tvProtocolAsset(p))
                        .renderingMode(.template).resizable().scaledToFit()
                        .frame(width: 84, height: 84)
                        .foregroundStyle(ring)
                } else {
                    Image(systemName: connected ? "checkmark.shield.fill" : "shield")
                        .font(.system(size: 84, weight: .light))
                        .foregroundStyle(ring)
                }
            }
        }
        .buttonStyle(.plain)
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
