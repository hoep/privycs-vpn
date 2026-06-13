import Foundation
import SwiftUI

/// Runtime in-app language override for tvOS — a verbatim port of the iOS
/// `LanguageManager`. tvOS normally resolves `LocalizedStringKey` against the
/// system (Apple TV) language only; this lets the user pick a language inside
/// the app and have it apply immediately (no relaunch), exactly like the phone
/// app's Settings → Language.
///
/// `Bundle.main` is reclassed so every `NSLocalizedString` / `LocalizedStringKey`
/// (i.e. `Text("key")`) lookup routes through the chosen `.lproj`. `String(
/// localized:)` bypasses that swizzle (it resolves against the SYSTEM language),
/// so the views use `loc(_:)` for those — same fix as iOS (v1.1.4.1).
@MainActor
final class TVLanguageManager: ObservableObject {
    static let shared = TVLanguageManager()

    static let defaultsKey = "app_language_override"

    /// "" = follow the system language; otherwise an ISO code (de/es/fr/it/pt/en).
    @Published private(set) var code: String

    private init() {
        code = UserDefaults.standard.string(forKey: Self.defaultsKey) ?? ""
        TVLanguageManager.installSwizzleOnce()
    }

    func set(_ newCode: String) {
        guard newCode != code else { return }
        code = newCode
        if newCode.isEmpty {
            UserDefaults.standard.removeObject(forKey: Self.defaultsKey)
        } else {
            UserDefaults.standard.set(newCode, forKey: Self.defaultsKey)
        }
    }

    /// Synchronous, actor-free read for the swizzled bundle.
    nonisolated static var currentCode: String {
        UserDefaults.standard.string(forKey: defaultsKey) ?? ""
    }

    private static var installed = false
    static func installSwizzleOnce() {
        guard !installed else { return }
        installed = true
        object_setClass(Bundle.main, TVLocalizedBundle.self)
    }

    /// Bundle for the chosen override language, or `.main` when following the
    /// system language. Used by `loc(_:)`.
    nonisolated static var localeBundle: Bundle {
        let code = currentCode
        guard !code.isEmpty,
              let path = Bundle.main.path(forResource: code, ofType: "lproj"),
              let b = Bundle(path: path) else { return .main }
        return b
    }
}

private final class TVLocalizedBundle: Bundle, @unchecked Sendable {
    override func localizedString(forKey key: String, value: String?, table tableName: String?) -> String {
        let code = TVLanguageManager.currentCode
        guard !code.isEmpty,
              let path = Bundle.main.path(forResource: code, ofType: "lproj"),
              let langBundle = Bundle(path: path) else {
            return super.localizedString(forKey: key, value: value, table: tableName)
        }
        return langBundle.localizedString(forKey: key, value: value, table: tableName)
    }
}

/// Localize respecting the in-app language override. Use this anywhere the iOS
/// app would use `String(localized:)` — it routes through the chosen `.lproj`
/// instead of the system language.
func loc(_ key: String.LocalizationValue) -> String {
    String(localized: key, bundle: TVLanguageManager.localeBundle)
}
