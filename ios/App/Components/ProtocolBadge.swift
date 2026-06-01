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

    private var tint: Color { active ? proto.brandColor : PrivycsColor.onSurfaceVariant }
    private var bg: Color { active ? proto.brandColor.opacity(0.2) : PrivycsColor.surfaceVariant }

    var body: some View {
        HStack(spacing: 5) {
            Image(proto.assetName)
                .renderingMode(.template)
                .resizable()
                .scaledToFit()
                .frame(width: compact ? 12 : 14, height: compact ? 12 : 14)
            if !compact {
                Text(proto.shortLabel)
                    .font(.system(size: 12, weight: .semibold))
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
