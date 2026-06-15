#if os(macOS)
import AppKit
import CoreText

/// macOS doesn't honour the iOS `UIAppFonts` Info.plist key, so the bundled
/// design-system typefaces (Inter sans + Fira Code mono, under App/Resources)
/// aren't auto-registered. Register every bundled .ttf at launch so
/// PrivycsFont.custom(...) resolves them by PostScript name (otherwise SwiftUI
/// silently falls back to the system font and the brand identity is lost).
enum PrivycsMacFonts {
    static func register() {
        guard let resURL = Bundle.main.resourceURL else { return }
        let fm = FileManager.default
        guard let walker = fm.enumerator(at: resURL,
                                         includingPropertiesForKeys: nil) else { return }
        for case let url as URL in walker where url.pathExtension.lowercased() == "ttf" {
            CTFontManagerRegisterFontsForURL(url as CFURL, .process, nil)
        }
    }
}
#endif
