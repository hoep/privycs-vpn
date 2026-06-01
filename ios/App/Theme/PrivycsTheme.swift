import SwiftUI
import UIKit
import PrivycsCore

/// Privycs design system — 1:1 port of the Android `ui/theme/Theme.kt`
/// token set so the iOS app shares the exact brand identity.
/// Brand primary is PrivycsTeal #00CDAB. Background/surface colours
/// are adaptive (dark/light) via UIColor dynamic providers; brand,
/// status and protocol colours are fixed across both schemes
/// (matching Android, where PrivycsTeal is identical in both).
enum PrivycsColor {

    // MARK: Brand
    static let teal      = Color(hex: 0x00CDAB)
    static let tealDark  = Color(hex: 0x00A88C)
    static let tealLight = Color(hex: 0x33D7BC)

    // MARK: Status
    static let connected    = Color(hex: 0x00CDAB)
    static let disconnected = Color(hex: 0x6B7280)
    static let error        = Color(hex: 0xEF4444)
    static let warning      = Color(hex: 0xF59E0B)

    // MARK: Protocol badge colours (match Android exactly)
    static let wireGuard  = Color(hex: 0x88171A)
    static let openVPN    = Color(hex: 0xEA7E20)
    static let ipsec      = Color(hex: 0x2563EB)
    static let amneziaWG  = Color(hex: 0x6366F1)

    // MARK: Adaptive background / surface
    static let background = Color(light: 0xF8FAFB, dark: 0x0F1419)
    static let surface    = Color(light: 0xFFFFFF, dark: 0x1A1F2E)
    static let surfaceVariant = Color(light: 0xF0F2F5, dark: 0x242938)

    // MARK: Adaptive on-surface text
    static let onSurface       = Color(light: 0x111827, dark: 0xE5E7EB)
    static let onSurfaceVariant = Color(light: 0x6B7280, dark: 0x9CA3AF)
    static let outline         = Color(light: 0xD1D5DB, dark: 0x374151)
}

extension VpnProtocol {
    /// Brand colour for protocol badges. Mirrors Android
    /// WireGuardRed / OpenVpnOrange / IpSecBlue / AmneziaWgIndigo.
    var brandColor: Color {
        switch self {
        case .wireguard: return PrivycsColor.wireGuard
        case .openvpn:   return PrivycsColor.openVPN
        case .ipsec:     return PrivycsColor.ipsec
        case .amneziawg: return PrivycsColor.amneziaWG
        }
    }

    /// Short label for compact badges (WG / OVPN / IPSec / AWG).
    var shortLabel: String {
        switch self {
        case .wireguard: return "WG"
        case .openvpn:   return "OVPN"
        case .ipsec:     return "IPSec"
        case .amneziawg: return "AWG"
        }
    }

    /// Asset-catalog name of the real Android protocol logo (ported
    /// VectorDrawable→SVG / mono PNG, template-tinted). Used instead of
    /// SF Symbols so iOS matches the Android brand iconography 1:1.
    var assetName: String {
        switch self {
        case .wireguard: return "ic_protocol_wireguard"
        case .openvpn:   return "ic_protocol_openvpn"
        case .ipsec:     return "ic_protocol_strongswan"
        case .amneziawg: return "ic_protocol_amneziawg"
        }
    }

    /// SF Symbol fallback (used only if an asset is missing).
    var sfSymbol: String {
        switch self {
        case .wireguard: return "bolt.shield"
        case .openvpn:   return "lock.shield"
        case .ipsec:     return "key.horizontal"
        case .amneziawg: return "waveform.path.ecg.rectangle"
        }
    }
}

// MARK: - Color hex helpers

extension Color {
    /// Solid hex colour, e.g. `Color(hex: 0x00CDAB)`.
    init(hex: UInt32) {
        let r = Double((hex >> 16) & 0xFF) / 255.0
        let g = Double((hex >> 8) & 0xFF) / 255.0
        let b = Double(hex & 0xFF) / 255.0
        self.init(.sRGB, red: r, green: g, blue: b, opacity: 1.0)
    }

    /// Adaptive colour that resolves to `light` or `dark` based on the
    /// active UITraitCollection — the iOS equivalent of Android's
    /// dark/light ColorScheme split.
    init(light: UInt32, dark: UInt32) {
        self.init(uiColor: UIColor { traits in
            traits.userInterfaceStyle == .dark
                ? UIColor(hexValue: dark)
                : UIColor(hexValue: light)
        })
    }
}

private extension UIColor {
    convenience init(hexValue: UInt32) {
        self.init(
            red:   CGFloat((hexValue >> 16) & 0xFF) / 255.0,
            green: CGFloat((hexValue >> 8) & 0xFF) / 255.0,
            blue:  CGFloat(hexValue & 0xFF) / 255.0,
            alpha: 1.0
        )
    }
}
