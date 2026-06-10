import Foundation
import SwiftUI

/// Runtime in-app language override. iOS normally resolves
/// `LocalizedStringKey` against the system language only; to let the user
/// pick a language inside the app (and have it take effect immediately,
/// including the tab bar — no restart), we swap `Bundle.main`'s class for
/// one whose `localizedString(forKey:…)` reads the chosen `.lproj`.
///
/// The selected code is mirrored into UserDefaults so the swizzled bundle
/// (which runs outside the main actor) can read it synchronously, and so
/// the choice survives launch before settings finish loading.
@MainActor
final class LanguageManager: ObservableObject {
    static let shared = LanguageManager()

    static let defaultsKey = "app_language_override"

    /// "" = follow the system language; otherwise an ISO code (de/es/fr/it/pt/en).
    /// `@Published` so SwiftUI re-renders (via `.id`) when it changes.
    @Published private(set) var code: String

    private init() {
        code = UserDefaults.standard.string(forKey: Self.defaultsKey) ?? ""
        LanguageManager.installSwizzleOnce()
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
        object_setClass(Bundle.main, LocalizedBundle.self)
    }
}

/// `Bundle.main` is reclassed to this so every `NSLocalizedString` /
/// `LocalizedStringKey` lookup routes through the user-chosen `.lproj`.
/// Falls back to the default behaviour when no override is set or the
/// language bundle can't be found.
private final class LocalizedBundle: Bundle, @unchecked Sendable {
    override func localizedString(forKey key: String, value: String?, table tableName: String?) -> String {
        let code = LanguageManager.currentCode
        guard !code.isEmpty,
              let path = Bundle.main.path(forResource: code, ofType: "lproj"),
              let langBundle = Bundle(path: path) else {
            return super.localizedString(forKey: key, value: value, table: tableName)
        }
        return langBundle.localizedString(forKey: key, value: value, table: tableName)
    }
}

extension LanguageManager {
    /// Bundle for the chosen override language, or `.main` when following the
    /// system language. Used by `loc(_:)` to localize `String(localized:)`
    /// values against the in-app choice.
    nonisolated static var localeBundle: Bundle {
        let code = currentCode
        guard !code.isEmpty,
              let path = Bundle.main.path(forResource: code, ofType: "lproj"),
              let b = Bundle(path: path) else { return .main }
        return b
    }
}

/// Localize respecting the in-app language override.
///
/// `String(localized:)` (unlike `Text`/`NSLocalizedString`) bypasses
/// LanguageManager's `Bundle` swizzle and resolves against the SYSTEM language.
/// That left ~111 strings — Connect-screen details ("Connections", "Endpoint",
/// "Last handshake"), relative times ("1 minute ago"), the tunnel-health pill
/// ("Healthy"), … — stuck in the device language while the rest of the UI
/// followed the in-app choice. Routing through the chosen `.lproj` bundle fixes
/// it. Falls back to `.main` (system language) when no override is set.
func loc(_ key: String.LocalizationValue) -> String {
    String(localized: key, bundle: LanguageManager.localeBundle)
}
