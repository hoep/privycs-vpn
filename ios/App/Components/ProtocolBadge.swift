import SwiftUI
import PrivycsCore

/// Pill badge for a protocol — port of the Android protocol-badge
/// (tinted icon + short label). Used in the connection picker, the
/// connections list, and the multi-config sheet.
struct ProtocolBadge: View {
    let proto: VpnProtocol
    var endpoint: String? = nil
    var compact: Bool = false

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: proto.sfSymbol)
                .font(.system(size: compact ? 10 : 12, weight: .semibold))
            if !compact {
                Text(proto.shortLabel)
                    .font(.system(size: 12, weight: .semibold))
            }
            if let endpoint, !endpoint.isEmpty, !compact {
                Text(endpoint)
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
        }
        .foregroundStyle(proto.brandColor)
        .padding(.horizontal, compact ? 7 : 9)
        .padding(.vertical, compact ? 3 : 5)
        .background(
            Capsule().fill(proto.brandColor.opacity(0.14))
        )
        .overlay(
            Capsule().stroke(proto.brandColor.opacity(0.30), lineWidth: 0.5)
        )
    }
}
