import SwiftUI
import UIKit

/// Privycs design-system tokens for tvOS — ADAPTIVE light/dark (driven by the TV's
/// Settings ▸ General ▸ Appearance), mirroring the iOS app's PrivycsColor ramp.
/// Brand teal is fixed; background/surface/text adapt. (PrivycsTheme itself lives
/// in the App target, so these are re-declared here for the tvOS target.)
enum TVColor {
    // Brand — teal foreground darkens on light surfaces (contrast), like accent.
    static let teal             = dyn(0x0A8F78, 0x00CDAB)
    static let tealFill         = c(0x00CDAB)   // brand FILL (same in both schemes)
    static let tealBright       = c(0x16E0BE)
    static let onTeal           = c(0x05201B)   // text/icon on a teal fill

    // Command-console ramp (light --ink/--surface · dark --ink/--surface).
    static let background       = dyn(0xEDF3F2, 0x070B0E)
    static let backgroundTop    = dyn(0xE3EEEC, 0x0A171C)   // gradient top (teal-tinted)
    static let surface          = dyn(0xFFFFFF, 0x0E161C)
    static let surfaceVariant   = dyn(0xF2F7F6, 0x17242E)
    static let onSurface        = dyn(0x08191C, 0xEAF1F3)
    static let onSurfaceVariant = dyn(0x44585E, 0x9DB2BD)
    static let outline          = dyn(0xE2E8E7, 0x1F2D36)
    static let error            = c(0xEF4444)

    private static func dyn(_ light: Int, _ dark: Int) -> Color {
        Color(UIColor { tc in tc.userInterfaceStyle == .dark ? ui(dark) : ui(light) })
    }
    private static func c(_ hex: Int) -> Color { Color(ui(hex)) }
    private static func ui(_ hex: Int) -> UIColor {
        UIColor(red: CGFloat((hex >> 16) & 0xFF) / 255,
                green: CGFloat((hex >> 8) & 0xFF) / 255,
                blue: CGFloat(hex & 0xFF) / 255, alpha: 1)
    }
}
