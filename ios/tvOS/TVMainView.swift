import SwiftUI
import PrivycsCore

/// Apple TV main shell — left navigation rail + content area, per the Privycs
/// tvOS design system (console look: teal on the dark command-console ramp,
/// gradient + glow background, frosted cards). Screens: Connect / Configs / Rules
/// / Settings. Focus moves rail ⇄ content (each is a focus section).
enum TVScreen: String, CaseIterable, Identifiable {
    case connect, configs, rules, settings
    var id: String { rawValue }
    var title: String {
        switch self {
        case .connect:  return loc("tv.nav.connect")
        case .configs:  return loc("tv.nav.configs")
        case .rules:    return loc("tv.nav.rules")
        case .settings: return loc("tv.nav.settings")
        }
    }
    var icon: String {
        switch self {
        case .connect:  return "bolt.horizontal.circle"
        case .configs:  return "rectangle.stack"
        case .rules:    return "slider.horizontal.3"
        case .settings: return "gearshape"
        }
    }
    var kicker: String { "[ \(title.uppercased()) ]" }
}

struct TVMainView: View {
    @EnvironmentObject private var state: TVAppState
    @State private var screen: TVScreen = .connect

    var body: some View {
        HStack(spacing: 0) {
            rail
            content
        }
    }

    // MARK: — Left rail

    private var rail: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 14) {
                Image("ic_privycs_logo").resizable().scaledToFit().frame(width: 48, height: 48)
                    .clipShape(RoundedRectangle(cornerRadius: 12))
                VStack(alignment: .leading, spacing: 2) {
                    Text("Privycs").font(TVFont.sans(26, .bold)).foregroundStyle(TVColor.onSurface)
                    Text("Secure.Private.Simple.").font(TVFont.mono(12)).foregroundStyle(TVColor.teal)
                        .lineLimit(1).minimumScaleFactor(0.7)
                }
            }
            .padding(.bottom, 36).padding(.horizontal, 6)

            VStack(spacing: 14) {
                ForEach(TVScreen.allCases) { s in
                    Button { screen = s } label: {
                        HStack(spacing: 20) {
                            Image(systemName: s.icon).font(.system(size: 32, weight: .regular))
                            Text(s.title).font(TVFont.sans(26, .semibold))
                                .lineLimit(1).minimumScaleFactor(0.6)
                            Spacer(minLength: 0)
                        }
                        .foregroundStyle(screen == s ? TVColor.teal : TVColor.onSurfaceVariant)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.vertical, 18).padding(.horizontal, 12)
                    }
                    .buttonStyle(.card)
                }
            }

            Spacer()

            HStack(spacing: 10) {
                Circle().fill(state.status.connected ? TVColor.tealFill : TVColor.onSurfaceVariant)
                    .frame(width: 9, height: 9)
                Text(state.status.connected ? loc("tv.status.connected")
                                            : loc("tv.status.disconnected"))
                    .font(TVFont.mono(14)).foregroundStyle(TVColor.onSurfaceVariant)
            }
            .padding(.horizontal, 6)
        }
        .padding(.horizontal, 26).padding(.vertical, 44)
        .frame(width: 330)
        .background(TVColor.side.opacity(0.55))
        .overlay(Rectangle().fill(TVColor.outline).frame(width: 1), alignment: .trailing)
        .focusSection()
    }

    // MARK: — Content

    @ViewBuilder private var content: some View {
        VStack(alignment: .leading, spacing: 0) {
            screenHeader
            ScrollView {
                Group {
                    switch screen {
                    case .connect:  TVConnectScreen()
                    case .configs:  TVConfigsScreen()
                    case .rules:    TVRulesScreen()
                    case .settings: TVSettingsScreen()
                    }
                }
                // Top padding so the first row's focus-lift (.card scale) isn't
                // clipped by the ScrollView's top edge under the header.
                .padding(.top, 16).padding(.horizontal, 60).padding(.bottom, 50)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private var screenHeader: some View {
        let connected = state.status.connected
        let title: String = {
            if screen == .connect {
                return connected ? loc("tv.status.connected")
                                 : loc("tv.connect.notconnected")
            }
            return screen.title
        }()
        return HStack(alignment: .bottom) {
            VStack(alignment: .leading, spacing: 10) {
                Text(screen.kicker).font(TVFont.mono(15)).tracking(3).foregroundStyle(TVColor.teal)
                Text(title).font(TVFont.sans(44, .bold)).foregroundStyle(TVColor.onSurface)
            }
            Spacer()
            if screen == .connect && connected {
                HStack(spacing: 10) {
                    Circle().fill(TVColor.okFg).frame(width: 9, height: 9)
                    Text(state.status.activeProtocol?.displayName ?? "VPN")
                        .font(TVFont.mono(16)).foregroundStyle(TVColor.okFg)
                }
                .padding(.horizontal, 18).padding(.vertical, 11)
                .background(TVColor.okFg.opacity(0.12), in: Capsule())
            }
        }
        .padding(.horizontal, 60).padding(.top, 54).padding(.bottom, 28)
    }
}
