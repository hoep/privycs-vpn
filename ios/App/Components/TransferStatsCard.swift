import SwiftUI

/// Download/Upload stat card — port of Android's transfer cards:
/// icon + label, cumulative total, live speed, and a SpeedSparkline.
struct TransferStatsCard: View {
    let title: String
    let icon: String
    /// Cumulative bytes.
    let totalBytes: Int64
    /// Current throughput in bytes/sec.
    let speedBytesPerSec: Double
    /// Rolling throughput window for the sparkline.
    let history: [Double]
    var tint: Color = PrivycsColor.teal

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 5) {
                Image(systemName: icon)
                Text(title)
            }
            .font(.system(size: 12, weight: .medium))
            .foregroundStyle(PrivycsColor.onSurfaceVariant)

            Text(formatBytes(totalBytes))
                .font(.system(size: 20, weight: .bold))
                .foregroundStyle(PrivycsColor.onSurface)
                .lineLimit(1)
                .minimumScaleFactor(0.7)

            Text(formatSpeed(speedBytesPerSec))
                .font(.system(size: 11))
                .foregroundStyle(tint)

            SpeedSparkline(samples: history, tint: tint)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(RoundedRectangle(cornerRadius: 12).fill(PrivycsColor.surface))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(PrivycsColor.outline.opacity(0.4), lineWidth: 0.5))
    }

    private func formatBytes(_ b: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: b, countStyle: .binary)
    }

    private func formatSpeed(_ bps: Double) -> String {
        let s = ByteCountFormatter.string(fromByteCount: Int64(bps), countStyle: .binary)
        return "\(s)/s"
    }
}
