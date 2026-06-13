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
        case .connect:  return String(localized: "tv.nav.connect",  defaultValue: "Connect")
        case .configs:  return String(localized: "tv.nav.configs",  defaultValue: "Configs")
        case .rules:    return String(localized: "tv.nav.rules",    defaultValue: "Rules")
        case .settings: return String(localized: "tv.nav.settings", defaultValue: "Settings")
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
                    Text("VPN").font(TVFont.mono(13)).foregroundStyle(TVColor.teal)
                }
            }
            .padding(.bottom, 36).padding(.horizontal, 6)

            ForEach(TVScreen.allCases) { s in
                Button { screen = s } label: {
                    HStack(spacing: 16) {
                        Image(systemName: s.icon).font(.system(size: 24))
                        Text(s.title).font(TVFont.sans(22, .medium))
                        Spacer()
                    }
                    .foregroundStyle(screen == s ? TVColor.teal : TVColor.onSurfaceVariant)
                    .padding(.vertical, 4)
                }
                .buttonStyle(.card)
            }

            Spacer()

            HStack(spacing: 10) {
                Circle().fill(state.status.connected ? TVColor.tealFill : TVColor.onSurfaceVariant)
                    .frame(width: 9, height: 9)
                Text(state.status.connected ? String(localized: "tv.status.connected")
                                            : String(localized: "tv.status.disconnected"))
                    .font(TVFont.mono(14)).foregroundStyle(TVColor.onSurfaceVariant)
            }
            .padding(.horizontal, 6)
        }
        .padding(.horizontal, 26).padding(.vertical, 44)
        .frame(width: 300)
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
                .padding(.horizontal, 60).padding(.bottom, 50)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private var screenHeader: some View {
        let connected = state.status.connected
        let title: String = {
            if screen == .connect {
                return connected ? String(localized: "tv.status.connected")
                                 : String(localized: "tv.connect.notconnected", defaultValue: "Not Connected")
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
