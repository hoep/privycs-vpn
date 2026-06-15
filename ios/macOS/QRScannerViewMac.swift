#if os(macOS)
import SwiftUI
import PrivycsCore

/// macOS stand-in for the iOS camera `QRScannerView` (App/Views/QRScannerView.swift,
/// excluded from the macOS target — UIViewControllerRepresentable + the iOS
/// camera capture stack don't exist on macOS). Rather than a dead stub, this
/// keeps the feature usable on the Mac: the user pastes the QR payload (an
/// enrollment URL or config text — e.g. copied from a phone) and it's handed to
/// the same `onScan` callback the camera path uses. A real AVFoundation/CoreMedia
/// macOS scanner (with the camera entitlement) is a later polish item.
struct QRScannerView: View {
    let onScan: (String) -> Void
    @State private var pasted = ""

    var body: some View {
        VStack(spacing: 16) {
            Image(systemName: "qrcode.viewfinder")
                .font(.system(size: 48))
                .foregroundStyle(PrivycsColor.accent)
            Text(loc("Paste the QR code link or contents"))
                .font(.headline)
                .multilineTextAlignment(.center)
            Text(loc("On Mac, copy the enrollment link or config from your phone and paste it here."))
                .font(.caption)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            TextEditor(text: $pasted)
                .font(PrivycsFont.mono(13))
                .frame(minHeight: 90)
                .overlay(RoundedRectangle(cornerRadius: 8)
                    .stroke(PrivycsColor.outline, lineWidth: 1))
            Button(loc("Use")) {
                let v = pasted.trimmingCharacters(in: .whitespacesAndNewlines)
                if !v.isEmpty { onScan(v) }
            }
            .buttonStyle(.borderedProminent)
            .disabled(pasted.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(PrivycsColor.background)
    }
}
#endif
