import SwiftUI

/// Canvas bar-chart sparkline — port of Android's SpeedSparkline.
/// Renders a rolling window of throughput samples as gapped bars,
/// auto-scaled to the window max, with a 1.5px floor so tiny bursts
/// stay visible.
struct SpeedSparkline: View {
    /// Most-recent-last throughput samples (bytes/sec). Window length
    /// is whatever the caller keeps; ~24 reads well at 24pt height.
    let samples: [Double]
    var tint: Color = PrivycsColor.teal

    var body: some View {
        Canvas { ctx, size in
            guard samples.count > 1 else { return }
            let maxV = max(samples.max() ?? 1, 1)
            let n = samples.count
            let gap: CGFloat = 0.30           // 30% inter-bar gap, matches Android
            let slot = size.width / CGFloat(n)
            let barW = slot * (1 - gap)
            for (i, v) in samples.enumerated() {
                let h = max(CGFloat(v / maxV) * size.height, 1.5)
                let x = CGFloat(i) * slot + (slot - barW) / 2
                let rect = CGRect(x: x, y: size.height - h, width: barW, height: h)
                ctx.fill(Path(roundedRect: rect, cornerRadius: barW / 3), with: .color(tint))
            }
        }
        .frame(height: 24)
    }
}
