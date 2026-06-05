import SwiftUI

/// Universal label-on-left, value/content-on-right row — an iOS-15-safe
/// replacement for `LabeledContent` (iOS 16+). Plain HStack, works everywhere.
struct LabeledRow<Content: View>: View {
    let label: LocalizedStringKey
    @ViewBuilder let content: () -> Content

    init(_ label: LocalizedStringKey, @ViewBuilder content: @escaping () -> Content) {
        self.label = label
        self.content = content
    }

    var body: some View {
        HStack {
            Text(label)
            Spacer()
            content()
        }
    }
}

extension LabeledRow where Content == Text {
    /// Convenience for a plain string value (rendered secondary, like LabeledContent).
    init(_ label: LocalizedStringKey, value: String) {
        self.init(label) { Text(value).foregroundStyle(.secondary) }
    }
}
