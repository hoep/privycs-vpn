import SwiftUI

/// Privycs design-system typography for SwiftUI — Inter (sans, the UI
/// typeface) + Fira Code (mono, for technical labels / stats / chips /
/// numerals). Bundled under App/Resources/Fonts and registered via
/// UIAppFonts in project.yml. Resolved by PostScript name.
///
/// All helpers use `Font.custom(_:size:relativeTo:)` so the fonts still
/// scale with Dynamic Type, matching the system-font behaviour they
/// replace. Apply `PrivycsFont.inter(...)` / `PrivycsFont.mono(...)`
/// wherever a `.system(...)` font was used; set a global default once at
/// the app root via `.font(PrivycsFont.inter(17))`.
enum PrivycsFont {

    static func inter(
        _ size: CGFloat,
        _ weight: Font.Weight = .regular,
        relativeTo style: Font.TextStyle = .body
    ) -> Font {
        Font.custom(interPostScriptName(for: weight), size: size, relativeTo: style)
    }

    static func mono(
        _ size: CGFloat,
        _ weight: Font.Weight = .regular,
        relativeTo style: Font.TextStyle = .body
    ) -> Font {
        Font.custom(firaPostScriptName(for: weight), size: size, relativeTo: style)
    }

    // MARK: PostScript name mapping (static weights bundled)

    private static func interPostScriptName(for weight: Font.Weight) -> String {
        switch weight {
        case .bold, .heavy, .black:        return "Inter-Bold"
        case .semibold:                    return "Inter-SemiBold"
        case .medium:                      return "Inter-Medium"
        default:                           return "Inter-Regular"
        }
    }

    private static func firaPostScriptName(for weight: Font.Weight) -> String {
        switch weight {
        case .semibold, .bold, .heavy, .black: return "FiraCode-SemiBold"
        case .medium:                          return "FiraCode-Medium"
        default:                               return "FiraCode-Regular"
        }
    }
}
