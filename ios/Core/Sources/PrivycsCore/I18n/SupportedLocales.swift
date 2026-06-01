import Foundation

/// Canonical list of locales privycs-vpn supports. Mirror der
/// Android `LocaleConfig.xml` + Desktop Vue-i18n locales.
public enum SupportedLocale: String, CaseIterable, Identifiable, Sendable {
    case english = "en"
    case german = "de"
    case spanish = "es"
    case french = "fr"
    case italian = "it"
    case portuguese = "pt"

    public var id: String { rawValue }

    /// User-display label im jeweiligen language nativ. Wird in
    /// der Settings → Language Picker UI gezeigt.
    public var nativeName: String {
        switch self {
        case .english: return "English"
        case .german: return "Deutsch"
        case .spanish: return "Español"
        case .french: return "Français"
        case .italian: return "Italiano"
        case .portuguese: return "Português"
        }
    }

    /// Identifier zum Direkt-Übergeben an `Locale(identifier:)`.
    public var localeIdentifier: String { rawValue }
}

/// Helper für i18n-Lookups. Wraps `String(localized:)` + delegiert
/// an die Localizable.xcstrings im App-Target. Caller-Pattern:
///
/// ```
/// Text(L10n.connectButton)
/// // expands to
/// Text(String(localized: "connect.button", bundle: .module))
/// ```
///
/// Wenn das xcstring-Catalog der App nicht im PrivycsCore-Bundle
/// liegt, fällt Apple's NSLocalizedString automatisch auf den
/// Caller-Bundle zurück — UI-side reicht ein simpler
/// `Text("connect.button")`.
public enum L10n {
    /// Convenience für app-side String-Lookups. Verwendet das
    /// PrivycsCore-Bundle als Default — App-Layer überschreibt
    /// via String(localized: bundle:) wenn nötig.
    public static func t(_ key: String) -> String {
        // Default to .main — the app target carries the xcstrings catalog.
        // PrivycsCore Package.swift declares no resources so Bundle.module
        // is not synthesized.
        NSLocalizedString(key, comment: "")
    }
}
