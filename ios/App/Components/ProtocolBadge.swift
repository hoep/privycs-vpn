import SwiftUI
import PrivycsCore

/// Pill badge for a protocol — 1:1 port of the Android ConnectScreen
/// ProtocolBadges styling:
///   ACTIVE   → bg = brandColor @ 0.2, text/icon = brandColor, 1pt
///              brandColor @ 0.3 border
///   INACTIVE → bg = surfaceVariant, text/icon = onSurfaceVariant
///              (neutral — NOT brand-tinted)
/// Icon is the real Android brand logo (ported VectorDrawable/PNG),
/// template-tinted to the text colour.
struct ProtocolBadge: View {
    let proto: VpnProtocol
    var endpoint: String? = nil
    var active: Bool = true
    var compact: Bool = false
    /// Show the protocol's full brand name next to the logo (Android Connect-
    /// screen parity: "WireGuard" / "AmneziaWG" / …). The Configs screen leaves
    /// this off and shows the endpoint host instead.
    var showName: Bool = false
    /// Number of configs of this protocol in the connection. >1 shows a
    /// "×N" count (Android parity — a connection can hold N same-protocol
    /// endpoints as a failover bag).
    var count: Int = 1

    private var tint: Color { active ? proto.brandColor : PrivycsColor.onSurfaceVariant }
    private var bg: Color { active ? proto.brandColor.opacity(0.2) : PrivycsColor.surfaceVariant }

    var body: some View {
        HStack(spacing: 5) {
            Image(proto.assetName)
                .renderingMode(.template)
                .resizable()
                .scaledToFit()
                .frame(width: compact ? 14 : 16, height: compact ? 14 : 16)
            // Full brand name on the Connect screen (Android parity).
            if showName {
                Text(proto.displayName)
                    .font(.system(size: 13, weight: .medium))
            }
            if count > 1 {
                Text("×\(count)")
                    .font(.system(size: 11, weight: .bold))
                    .foregroundStyle(tint.opacity(0.85))
            }
            if let endpoint, !endpoint.isEmpty, !compact {
                Text(endpoint)
                    .font(.system(size: 11))
                    .foregroundStyle(tint.opacity(0.7))
                    .lineLimit(1)
            }
        }
        .foregroundStyle(tint)
        .padding(.horizontal, compact ? 7 : 9)
        .padding(.vertical, compact ? 3 : 5)
        .background(Capsule().fill(bg))
        .overlay(active ? Capsule().stroke(proto.brandColor.opacity(0.3), lineWidth: 1) : nil)
    }
}
