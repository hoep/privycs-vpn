import SwiftUI
import UIKit
import AVFoundation
import PrivycsCore

/// AVFoundation-based QR scanner. Liefert den raw String einem
/// SwiftUI-Caller über `onScan` callback. Erfordert
/// NSCameraUsageDescription im Info.plist.
struct QRScannerView: UIViewControllerRepresentable {
    let onScan: (String) -> Void

    func makeUIViewController(context: Context) -> QRScannerVC {
        let vc = QRScannerVC()
        vc.onScan = onScan
        return vc
    }

    func updateUIViewController(_ uiViewController: QRScannerVC, context: Context) {}
}

final class QRScannerVC: UIViewController, AVCaptureMetadataOutputObjectsDelegate {
    var onScan: ((String) -> Void)?
    private var captureSession: AVCaptureSession?

    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = .black
        guard let device = AVCaptureDevice.default(for: .video),
              let input = try? AVCaptureDeviceInput(device: device) else { return }
        let session = AVCaptureSession()
        session.addInput(input)
        let output = AVCaptureMetadataOutput()
        session.addOutput(output)
        output.setMetadataObjectsDelegate(self, queue: .main)
        output.metadataObjectTypes = [.qr]

        let preview = AVCaptureVideoPreviewLayer(session: session)
        preview.frame = view.bounds
        preview.videoGravity = .resizeAspectFill
        view.layer.addSublayer(preview)

        self.captureSession = session
        DispatchQueue.global(qos: .userInitiated).async {
            session.startRunning()
        }
    }

    func metadataOutput(_ output: AVCaptureMetadataOutput,
                       didOutput metadataObjects: [AVMetadataObject],
                       from connection: AVCaptureConnection) {
        guard let mo = metadataObjects.first as? AVMetadataMachineReadableCodeObject,
              let str = mo.stringValue else { return }
        captureSession?.stopRunning()
        onScan?(str)
    }
}
