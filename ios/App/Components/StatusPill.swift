import SwiftUI

/// Connection-status pill — coloured dot + label. Three visual states
/// match the Android status chip (Connected / Connecting / Disconnected).
struct StatusPill: View {
    enum State { case connected, connecting, disconnected }
    let state: State

    private var color: Color {
        switch state {
        case .connected:    return PrivycsColor.connected
        case .connecting:   return PrivycsColor.warning
        case .disconnected: return PrivycsColor.disconnected
        }
    }

    private var label: String {
        switch state {
        case .connected:    return String(localized: "Connected")
        case .connecting:   return String(localized: "Connecting…")
        case .disconnected: return String(localized: "Disconnected")
        }
    }

    var body: some View {
        HStack(spacing: 7) {
            Circle()
                .fill(color)
                .frame(width: 9, height: 9)
            Text(label)
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(PrivycsColor.onSurface)
        }
        .padding(.horizontal, 13)
        .padding(.vertical, 7)
        .background(Capsule().fill(PrivycsColor.surfaceVariant))
        .overlay(Capsule().stroke(color.opacity(0.35), lineWidth: 0.5))
    }
}
