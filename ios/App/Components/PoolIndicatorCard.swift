import SwiftUI
import PrivycsCore

/// Active-pool card shown on the Connect screen — port of Android's
/// PoolIndicatorCard. Shows the pool name, policy, current member +
/// country, and (for rotating pools) a live countdown to the next
/// rotation. Tapping "Rotate now" forces an immediate rotation.
struct PoolIndicatorCard: View {
    let poolName: String
    let policy: PoolPolicy
    let memberName: String
    let memberCountry: String
    /// UNIX epoch of next rotation, 0 = no rotation.
    let nextRotationAt: Int64
    let onRotateNow: () -> Void

    @State private var now = Date()
    private let ticker = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    private var countdown: String? {
        guard nextRotationAt > 0 else { return nil }
        let remaining = nextRotationAt - Int64(now.timeIntervalSince1970)
        guard remaining > 0 else { return String(localized: "rotating…") }
        let m = remaining / 60, s = remaining % 60
        return m > 0 ? String(format: "%dm %02ds", m, s) : "\(s)s"
    }

    var body: some View {
        VStack(spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: "circle.grid.3x3.fill")
                    .foregroundStyle(PrivycsColor.teal)
                VStack(alignment: .leading, spacing: 1) {
                    Text(poolName).font(.system(size: 14, weight: .semibold))
                        .foregroundStyle(PrivycsColor.onSurface)
                    Text(policy.displayName).font(.system(size: 11))
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if let countdown {
                    VStack(alignment: .trailing, spacing: 1) {
                        Text(countdown).font(.system(size: 13, weight: .semibold).monospacedDigit())
                            .foregroundStyle(PrivycsColor.teal)
                        Text("next rotation").font(.system(size: 9)).foregroundStyle(.secondary)
                    }
                }
            }

            Divider()

            HStack(spacing: 8) {
                Image(systemName: "checkmark.circle.fill")
                    .font(.system(size: 13))
                    .foregroundStyle(PrivycsColor.connected)
                Text(memberName.isEmpty ? "—" : memberName)
                    .font(.system(size: 13, weight: .medium))
                    .foregroundStyle(PrivycsColor.onSurface)
                if !memberCountry.isEmpty {
                    Text(memberCountry.uppercased())
                        .font(.system(size: 11)).foregroundStyle(.secondary)
                }
                Spacer()
                Button(action: onRotateNow) {
                    Label("Rotate", systemImage: "arrow.triangle.2.circlepath")
                        .font(.system(size: 12, weight: .medium))
                }
                .buttonStyle(.plain)
                .foregroundStyle(PrivycsColor.teal)
            }
        }
        .padding(14)
        .background(RoundedRectangle(cornerRadius: 12).fill(PrivycsColor.surface))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(PrivycsColor.teal.opacity(0.3), lineWidth: 0.5))
        .onReceive(ticker) { now = $0 }
    }
}
