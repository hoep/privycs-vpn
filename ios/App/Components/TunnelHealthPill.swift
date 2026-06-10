import SwiftUI

/// Tunnel-health traffic-light pill — port of Android's 3-state
/// health indicator (HEALTHY / DEGRADED / RECOVERING). Driven by the
/// liveness monitor (Session 2+); hidden when the tunnel is down.
struct TunnelHealthPill: View {
    enum Health { case healthy, degraded, recovering }
    let health: Health

    private var color: Color {
        switch health {
        case .healthy:    return PrivycsColor.connected
        case .degraded:   return PrivycsColor.warning
        case .recovering: return PrivycsColor.error
        }
    }

    private var label: String {
        switch health {
        case .healthy:    return loc("Healthy")
        case .degraded:   return loc("Degraded")
        case .recovering: return loc("Recovering…")
        }
    }

    var body: some View {
        HStack(spacing: 6) {
            Circle().fill(color).frame(width: 7, height: 7)
            Text(label)
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(color)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 4)
        .background(Capsule().fill(color.opacity(0.12)))
    }
}
