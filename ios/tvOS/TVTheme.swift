import SwiftUI

/// Privycs design-system tokens for tvOS (the iOS app's PrivycsTheme lives in the
/// App target). The TV is always dark, so we use the dark "command-console" ramp
/// directly: teal #00CDAB on near-black #070B0E with #0E161C surfaces.
enum TVColor {
    static let teal             = rgb(0x00, 0xCD, 0xAB)   // brand / accent / fills
    static let tealBright       = rgb(0x16, 0xE0, 0xBE)
    static let background       = rgb(0x07, 0x0B, 0x0E)   // --ink (dark)
    static let surface          = rgb(0x0E, 0x16, 0x1C)   // --surface
    static let surfaceVariant   = rgb(0x17, 0x24, 0x2E)
    static let onSurface        = rgb(0xEA, 0xF1, 0xF3)   // --fg
    static let onSurfaceVariant = rgb(0x9D, 0xB2, 0xBD)   // --fg-2
    static let outline          = rgb(0x1F, 0x2D, 0x36)
    static let error            = rgb(0xEF, 0x44, 0x44)

    private static func rgb(_ r: Int, _ g: Int, _ b: Int) -> Color {
        Color(red: Double(r) / 255, green: Double(g) / 255, blue: Double(b) / 255)
    }
}
