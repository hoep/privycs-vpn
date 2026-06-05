import SwiftUI
import UIKit

/// UIActivityViewController wrapper — the iOS 15 fallback for SwiftUI's
/// `ShareLink` (iOS 16+). Presented in a sheet so older iPads keep the
/// "share backup file" action.
struct ShareSheet: UIViewControllerRepresentable {
    let items: [Any]

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: items, applicationActivities: nil)
    }

    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}
