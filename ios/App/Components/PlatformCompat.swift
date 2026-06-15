import SwiftUI
#if canImport(AppKit)
import AppKit
#endif

// Cross-platform compatibility shims so the shared iPhone SwiftUI views
// compile UNCHANGED on macOS (the Mac App Store port reuses the iOS views
// verbatim — see project.yml target PrivycsVPNmacOS). Each helper maps an
// iOS-only API to its macOS equivalent (or a no-op) at the call site so the
// reused views need no per-site `#if`. iOS rendering is byte-for-byte the
// same — these only add macOS behaviour, never change the iOS path.

// MARK: - Toolbar placements

extension ToolbarItemPlacement {
    /// Leading toolbar edge — Cancel / Edit. `.navigationBarLeading` is
    /// unavailable on macOS, so map to the semantic leading placement there.
    static var pvcsLeading: ToolbarItemPlacement {
        #if os(macOS)
        return .cancellationAction
        #else
        return .navigationBarLeading
        #endif
    }

    /// Trailing toolbar edge — Save / Done / primary action.
    static var pvcsTrailing: ToolbarItemPlacement {
        #if os(macOS)
        return .primaryAction
        #else
        return .navigationBarTrailing
        #endif
    }
}

#if os(macOS)
// MARK: - macOS shims for iOS-only SwiftUI modifiers
//
// These declare overloads with Privycs-owned parameter types. The SDK
// declarations are `@available(macOS, unavailable)` (along with their
// parameter enums), so on macOS overload resolution picks these — and on iOS
// they don't exist at all (guarded out), so the real SwiftUI modifiers run.

/// `.navigationBarTitleDisplayMode(_:)` — unavailable on macOS → no-op.
enum PvcsTitleDisplayMode { case automatic, inline, large }
extension View {
    func navigationBarTitleDisplayMode(_ mode: PvcsTitleDisplayMode) -> some View { self }
}

/// `.textInputAutocapitalization(_:)` — unavailable on macOS → no-op.
enum PvcsTextInputAutocapitalization { case never, words, sentences, characters }
extension View {
    func textInputAutocapitalization(_ style: PvcsTextInputAutocapitalization?) -> some View { self }
}

/// `.keyboardType(_:)` — unavailable on macOS → no-op.
enum PvcsKeyboardType {
    case `default`, URL, emailAddress, numberPad, decimalPad, asciiCapable, numbersAndPunctuation
}
extension View {
    func keyboardType(_ type: PvcsKeyboardType) -> some View { self }
}

/// `UIPasteboard.general.string` → `NSPasteboard.general` on macOS.
struct UIPasteboard {
    static let general = UIPasteboard()
    var string: String? {
        get { NSPasteboard.general.string(forType: .string) }
        nonmutating set {
            NSPasteboard.general.clearContents()
            if let v = newValue { NSPasteboard.general.setString(v, forType: .string) }
        }
    }
}
#endif
