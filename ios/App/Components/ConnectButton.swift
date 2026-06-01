import SwiftUI
import PrivycsCore

/// The big circular connect/disconnect button — port of Android's
/// 170dp giant button: outer glow-ring (animated when connected),
/// gradient fill, protocol icon in the centre, spinner while
/// connecting. Long-press surfaces the pause sheet (wired by caller).
struct ConnectButton: View {
    let connected: Bool
    let connecting: Bool
    /// Protocol of the active config — drives the centre icon when
    /// connected; falls back to a shield when idle/unknown.
    let activeProtocol: VpnProtocol?
    /// Sinkhole / kill-switch lock — renders danger-red, taps are no-ops.
    var sinkhole: Bool = false
    let onTap: () -> Void
    var onLongPress: (() -> Void)? = nil

    @State private var pulse = false

    private var ringColor: Color {
        if sinkhole { return PrivycsColor.error }
        return connected ? PrivycsColor.teal : PrivycsColor.disconnected
    }

    private var fillGradient: LinearGradient {
        let base: [Color]
        if sinkhole {
            base = [PrivycsColor.error.opacity(0.30), PrivycsColor.error.opacity(0.12)]
        } else if connected {
            base = [PrivycsColor.teal.opacity(0.32), PrivycsColor.tealDark.opacity(0.14)]
        } else {
            base = [PrivycsColor.surfaceVariant, PrivycsColor.surface]
        }
        return LinearGradient(colors: base, startPoint: .topLeading, endPoint: .bottomTrailing)
    }

    private var centerIcon: String {
        if sinkhole { return "lock.fill" }
        if connected { return activeProtocol?.sfSymbol ?? "checkmark.shield.fill" }
        return "shield"
    }

    /// Center content — the active protocol's REAL brand logo (template-
    /// tinted), matching Android's connect button. Falls back to a shield
    /// SF Symbol when idle / no protocol picked / sinkhole.
    @ViewBuilder private var centerContent: some View {
        if connecting {
            ProgressView().controlSize(.large).tint(PrivycsColor.teal)
        } else if !sinkhole, let proto = activeProtocol {
            Image(proto.assetName)
                .renderingMode(.template)
                .resizable()
                .scaledToFit()
                .frame(width: 68, height: 68)
                .foregroundStyle(ringColor)
        } else {
            Image(systemName: centerIcon)
                .font(.system(size: 72, weight: .light))
                .foregroundStyle(ringColor)
        }
    }

    var body: some View {
        Button(action: { if !sinkhole { onTap() } }) {
            ZStack {
                // Animated glow ring — only pulses while connected.
                Circle()
                    .stroke(ringColor.opacity(connected ? 0.55 : 0.30), lineWidth: 4)
                    .frame(width: 196, height: 196)
                    .scaleEffect(connected && pulse ? 1.04 : 1.0)
                    .opacity(connected && pulse ? 0.4 : 0.9)

                Circle()
                    .fill(fillGradient)
                    .frame(width: 170, height: 170)
                    .overlay(Circle().stroke(ringColor.opacity(0.6), lineWidth: 2))
                    .shadow(color: ringColor.opacity(connected ? 0.45 : 0.0), radius: 24)

                centerContent
            }
        }
        .buttonStyle(.plain)
        .contentShape(Circle())
        .onLongPressGesture(minimumDuration: 0.45) { onLongPress?() }
        .onAppear {
            withAnimation(.easeInOut(duration: 1.4).repeatForever(autoreverses: true)) {
                pulse = true
            }
        }
        .animation(.easeInOut(duration: 0.35), value: connected)
        .animation(.easeInOut(duration: 0.35), value: sinkhole)
    }
}
