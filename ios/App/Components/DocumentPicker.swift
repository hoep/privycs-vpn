import SwiftUI
import UniformTypeIdentifiers

/// iOS-15-safe file picker.
///
/// SwiftUI's `.fileImporter` (like `.sheet`) presented from WITHIN an
/// already-presented sheet mis-targets dismissal on iOS 15: picking a file
/// tears down the *parent* sheet instead of just the picker. In the pool flow
/// — AddConnectionView `.sheet` → AddPoolView `.fileImporter` — that meant the
/// user could open the picker and choose a `.zip`, but the moment the picker
/// closed the whole AddPoolView sheet vanished, so nothing could be saved
/// ("kann importieren, beendet nur den Dialog"). AddConnectionView's own
/// importer sits at the tab root (no parent sheet) and is unaffected, which is
/// why single-config import always worked.
///
/// This bridges `UIDocumentPickerViewController` and presents it through pure
/// UIKit (`present(_:)` on the host controller), bypassing the SwiftUI
/// presentation reconciler entirely — UIKit nests modals correctly. `asCopy:
/// true` hands back app-owned temp copies, so no security-scoped-resource
/// dance is needed to read them.
///
/// Attach as a zero-size `.background(...)`; the binding drives presentation.
struct DocumentPicker: UIViewControllerRepresentable {
    @Binding var isPresented: Bool
    let contentTypes: [UTType]
    let allowsMultiple: Bool
    let onPick: ([URL]) -> Void

    func makeUIViewController(context: Context) -> UIViewController {
        UIViewController() // invisible host; lives inside the sheet's hierarchy
    }

    func updateUIViewController(_ host: UIViewController, context: Context) {
        context.coordinator.parent = self
        guard isPresented,
              context.coordinator.picker == nil,
              host.presentedViewController == nil else { return }

        let picker = UIDocumentPickerViewController(forOpeningContentTypes: contentTypes, asCopy: true)
        picker.allowsMultipleSelection = allowsMultiple
        picker.delegate = context.coordinator
        context.coordinator.picker = picker
        // Present on the next runloop tick so the host is attached to a window.
        DispatchQueue.main.async { [weak host, weak picker] in
            guard let host, let picker, host.presentedViewController == nil else { return }
            host.present(picker, animated: true)
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    final class Coordinator: NSObject, UIDocumentPickerDelegate {
        var parent: DocumentPicker
        weak var picker: UIDocumentPickerViewController?

        init(_ parent: DocumentPicker) { self.parent = parent }

        func documentPicker(_ controller: UIDocumentPickerViewController, didPickDocumentsAt urls: [URL]) {
            picker = nil
            parent.isPresented = false
            parent.onPick(urls)
        }

        func documentPickerWasCancelled(_ controller: UIDocumentPickerViewController) {
            picker = nil
            parent.isPresented = false
        }
    }
}
