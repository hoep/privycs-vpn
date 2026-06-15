#if os(macOS)
import SwiftUI
import AppKit

/// macOS stand-in for the iOS `ShareSheet` (App/Components/ShareSheet.swift,
/// excluded from the macOS target). Bridges to AppKit's NSSharingServicePicker
/// so the reused views' `.sheet { ShareSheet(items:) }` call sites compile and
/// share backup files / config text the same way.
struct ShareSheet: NSViewRepresentable {
    let items: [Any]

    func makeNSView(context: Context) -> NSView {
        let host = NSView()
        DispatchQueue.main.async {
            let picker = NSSharingServicePicker(items: items)
            picker.show(relativeTo: host.bounds, of: host, preferredEdge: .minY)
        }
        return host
    }

    func updateNSView(_ nsView: NSView, context: Context) {}
}
#endif
