import SwiftUI

/// Privycs design-system typography for tvOS — Inter (sans) + Fira Code (mono),
/// mirroring the iOS app's PrivycsFont. Font.custom falls back to the system font
/// if the family isn't registered, so this is safe even before the TTFs bundle.
enum TVFont {
    static func sans(_ size: CGFloat, _ weight: Font.Weight = .regular) -> Font {
        Font.custom(interName(weight), size: size)
    }
    static func mono(_ size: CGFloat, _ weight: Font.Weight = .regular) -> Font {
        Font.custom(firaName(weight), size: size)
    }
    private static func interName(_ w: Font.Weight) -> String {
        switch w {
        case .bold, .heavy, .black: return "Inter-Bold"
        case .semibold:             return "Inter-SemiBold"
        case .medium:               return "Inter-Medium"
        default:                    return "Inter-Regular"
        }
    }
    private static func firaName(_ w: Font.Weight) -> String {
        switch w {
        case .semibold, .bold, .heavy, .black: return "FiraCode-SemiBold"
        case .medium:                          return "FiraCode-Medium"
        default:                               return "FiraCode-Regular"
        }
    }
}
