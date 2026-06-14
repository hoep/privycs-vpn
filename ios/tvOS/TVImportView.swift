import SwiftUI
import CoreImage.CIFilterBuiltins
import PrivycsCore

/// "Add a config / restore" sheet. The Apple TV can't scan a QR (no camera), so
/// it SHOWS one: the phone scans it (camera → browser form, path A) or the
/// Privycs iPhone app scans it and pushes (path B). Both POST to the on-device
/// `TVImportServer` on the local network — no gateway, no Files app.
struct TVImportView: View {
    @EnvironmentObject private var state: TVAppState
    @StateObject private var server = TVImportServer()
    @Environment(\.dismiss) private var dismiss

    @State private var resultText: String?
    @State private var resultOK = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            if let resultText {
                resultCard(resultText)
            } else {
                HStack(alignment: .top, spacing: 40) {
                    qrCard
                    stepsCard
                }
            }
            Spacer(minLength: 0)
            Button { dismiss() } label: {
                Text(loc(resultText == nil ? "tv.import.cancel" : "tv.import.done"))
                    .font(TVFont.sans(22, .semibold)).foregroundStyle(TVColor.onSurface)
                    .lineLimit(1).minimumScaleFactor(0.7)
                    .padding(.vertical, 14).padding(.horizontal, 30)
            }
            .buttonStyle(.card)
            .padding(.top, 24)
        }
        .padding(60)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .tvScreenChrome(theme: state.settings.theme)
        .onAppear {
            server.onPayload = { payload in
                Task { @MainActor in applyResult(await state.handleImport(payload)) }
            }
            server.start()
        }
        .onDisappear { server.stop() }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text(loc("tv.import.kicker")).font(TVFont.mono(15)).tracking(3).foregroundStyle(TVColor.teal)
            Text(loc("tv.import.title")).font(TVFont.sans(46, .bold)).foregroundStyle(TVColor.onSurface)
            Text(loc("tv.import.subtitle")).font(TVFont.sans(22)).foregroundStyle(TVColor.onSurfaceVariant)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.bottom, 36)
    }

    private var qrCard: some View {
        VStack(spacing: 18) {
            if !server.lanURL.isEmpty {
                qrImage(server.lanURL)
                    .frame(width: 260, height: 260)
                    .padding(18)
                    .background(Color.white, in: RoundedRectangle(cornerRadius: 18))
                HStack(spacing: 10) {
                    Text(loc("tv.import.pin")).font(TVFont.sans(18)).foregroundStyle(TVColor.onSurfaceVariant)
                    Text(server.pin).font(TVFont.mono(28, .bold)).tracking(4).foregroundStyle(TVColor.onSurface)
                }
            } else if let err = server.lastError {
                Text(err).font(TVFont.sans(18)).foregroundStyle(TVColor.error)
                    .multilineTextAlignment(.center)
            } else {
                ProgressView().tint(TVColor.teal)
                Text(loc("tv.import.no_lan")).font(TVFont.sans(16))
                    .foregroundStyle(TVColor.onSurfaceVariant).multilineTextAlignment(.center)
            }
        }
        .padding(30)
        .frame(width: 360)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 26))
        .overlay(RoundedRectangle(cornerRadius: 26).stroke(TVColor.outline, lineWidth: 1))
    }

    private var stepsCard: some View {
        VStack(alignment: .leading, spacing: 22) {
            step("1", loc("tv.import.step_scan"))
            if !server.lanURL.isEmpty {
                Text(server.lanURL.replacingOccurrences(of: "http://", with: ""))
                    .font(TVFont.mono(22, .semibold)).foregroundStyle(TVColor.teal)
                    .lineLimit(1).minimumScaleFactor(0.5)
                    .padding(.leading, 42)
            }
            step("2", loc("tv.import.step_paste"))
            if server.isRunning {
                HStack(spacing: 12) {
                    ProgressView().tint(TVColor.teal)
                    Text(loc("tv.import.waiting")).font(TVFont.mono(17)).foregroundStyle(TVColor.onSurfaceVariant)
                }
                .padding(.top, 6)
            }
        }
        .padding(34)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 26))
        .overlay(RoundedRectangle(cornerRadius: 26).stroke(TVColor.outline, lineWidth: 1))
    }

    private func resultCard(_ text: String) -> some View {
        HStack(spacing: 18) {
            Image(systemName: resultOK ? "checkmark.circle.fill" : "xmark.circle.fill")
                .font(.system(size: 44)).foregroundStyle(resultOK ? TVColor.okFg : TVColor.error)
            Text(text).font(TVFont.sans(24, .semibold)).foregroundStyle(TVColor.onSurface)
                .fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .padding(34)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 26))
    }

    private func step(_ n: String, _ text: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 14) {
            Text(n).font(TVFont.mono(16, .bold)).foregroundStyle(TVColor.onTeal)
                .frame(width: 28, height: 28).background(TVColor.teal, in: Circle())
            Text(text).font(TVFont.sans(20, .medium)).foregroundStyle(TVColor.onSurface)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private func applyResult(_ r: TVAppState.TVImportResult) {
        switch r {
        case .config:
            resultOK = true;  resultText = loc("tv.import.ok_config")
        case .backup(let n):
            resultOK = true;  resultText = "\(loc("tv.import.ok_backup")) (\(n))"
        case .pool(let n, _):
            resultOK = n > 0; resultText = "\(loc("tv.import.ok_pool")) (\(n))"
        case .unsupported:
            resultOK = false; resultText = loc("tv.import.unsupported")
        case .failure(let msg):
            resultOK = false; resultText = msg
        }
        server.stop()
    }

    // MARK: — QR

    private func qrImage(_ s: String) -> Image {
        let ctx = CIContext()
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(s.utf8)
        filter.correctionLevel = "M"
        if let out = filter.outputImage?.transformed(by: CGAffineTransform(scaleX: 10, y: 10)),
           let cg = ctx.createCGImage(out, from: out.extent) {
            return Image(decorative: cg, scale: 1, orientation: .up).interpolation(.none).resizable()
        }
        return Image(systemName: "qrcode").resizable()
    }
}
