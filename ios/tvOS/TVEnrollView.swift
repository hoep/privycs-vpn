import SwiftUI
import CoreImage.CIFilterBuiltins
import PrivycsCore

/// Enrollment screen. Primary path = device-code "link a TV": the phone scans the
/// QR (or opens the short URL) and enters the code. Secondary = manual gateway
/// URL + token. Uses the Privycs design system, readable at 10-foot distance.
struct TVEnrollView: View {
    @EnvironmentObject private var state: TVAppState

    @State private var device: TVDeviceStart?
    @State private var enrollError: String?
    @State private var polling = false
    @State private var pollTask: Task<Void, Never>?

    @State private var manualURL = ""
    @State private var manualToken = ""

    var body: some View {
        VStack(spacing: 28) {
            Text("tv.enroll.title", tableName: nil)
                .font(.system(size: 34, weight: .bold))
                .foregroundStyle(TVColor.onSurface)
            Text("tv.enroll.subtitle", tableName: nil)
                .font(.system(size: 20))
                .foregroundStyle(TVColor.onSurfaceVariant)

            HStack(alignment: .top, spacing: 48) {
                deviceCodeCard
                manualCard
            }
        }
        .padding(.horizontal, 80)
        .padding(.vertical, 50)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .onAppear { startDeviceCode() }
        .onDisappear { pollTask?.cancel() }
    }

    // MARK: — Device-code card (QR + code)

    private var deviceCodeCard: some View {
        VStack(spacing: 22) {
            if let device {
                HStack(alignment: .center, spacing: 32) {
                    qrView(device.verificationURIComplete)
                    VStack(alignment: .leading, spacing: 14) {
                        Text(String(localized: "tv.enroll.scan_or_open", defaultValue: "Scan with your phone — or open:"))
                            .font(.system(size: 21, weight: .medium))
                            .foregroundStyle(TVColor.onSurface)
                        Text(shortURL(device.verificationURI))
                            .font(.system(size: 24, weight: .bold))
                            .foregroundStyle(TVColor.teal)
                            .lineLimit(1).minimumScaleFactor(0.5)
                        Text("tv.enroll.enter_code", tableName: nil)
                            .font(.system(size: 19))
                            .foregroundStyle(TVColor.onSurfaceVariant)
                            .padding(.top, 4)
                        Text(device.userCode)
                            .font(.system(size: 52, weight: .bold, design: .monospaced))
                            .tracking(6)
                            .foregroundStyle(TVColor.onSurface)
                            .lineLimit(1).minimumScaleFactor(0.6)
                            .accessibilityLabel(device.userCode)
                        if polling {
                            HStack(spacing: 10) {
                                ProgressView()
                                Text(String(localized: "tv.enroll.waiting", defaultValue: "Waiting for approval…"))
                                    .font(.system(size: 17)).foregroundStyle(TVColor.onSurfaceVariant)
                            }
                            .padding(.top, 6)
                        }
                    }
                }
            } else if let enrollError {
                VStack(spacing: 16) {
                    Text(enrollError).font(.system(size: 20))
                        .foregroundStyle(TVColor.error).multilineTextAlignment(.center)
                    Button { startDeviceCode() } label: {
                        Label(String(localized: "tv.enroll.retry"), systemImage: "arrow.clockwise")
                            .font(.system(size: 20, weight: .semibold))
                    }
                }
            } else {
                ProgressView().frame(maxWidth: .infinity, minHeight: 260)
            }
        }
        .padding(32)
        .frame(maxWidth: .infinity, minHeight: 360)
        .background(TVColor.surface, in: RoundedRectangle(cornerRadius: 24))
        .overlay(RoundedRectangle(cornerRadius: 24).stroke(TVColor.outline, lineWidth: 1))
    }

    private func qrView(_ string: String) -> some View {
        Group {
            if let img = makeQR(string) {
                img.interpolation(.none).resizable().scaledToFit()
            } else {
                Image(systemName: "qrcode").resizable().scaledToFit().foregroundStyle(.black)
            }
        }
        .frame(width: 240, height: 240)
        .padding(16)
        .background(Color.white, in: RoundedRectangle(cornerRadius: 16))
    }

    // MARK: — Manual fallback card

    private var manualCard: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text("tv.enroll.manual_title", tableName: nil)
                .font(.system(size: 24, weight: .bold)).foregroundStyle(TVColor.onSurface)
            Text("tv.enroll.manual_hint", tableName: nil)
                .font(.system(size: 17)).foregroundStyle(TVColor.onSurfaceVariant)

            TextField(String(localized: "tv.enroll.gateway_url"), text: $manualURL)
                .textContentType(.URL).font(.system(size: 20))
            TextField(String(localized: "tv.enroll.token"), text: $manualToken)
                .font(.system(size: 20))

            Button { applyManual() } label: {
                Text("tv.enroll.link", tableName: nil)
                    .font(.system(size: 20, weight: .semibold))
                    .frame(maxWidth: .infinity)
            }
            .disabled(manualURL.isEmpty || manualToken.isEmpty)
        }
        .padding(32)
        .frame(width: 520, alignment: .leading)
        .background(TVColor.surface, in: RoundedRectangle(cornerRadius: 24))
        .overlay(RoundedRectangle(cornerRadius: 24).stroke(TVColor.outline, lineWidth: 1))
    }

    // MARK: — Helpers

    private func shortURL(_ s: String) -> String {
        s.replacingOccurrences(of: "https://", with: "")
         .replacingOccurrences(of: "http://", with: "")
    }

    private func makeQR(_ string: String) -> Image? {
        let ctx = CIContext()
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(string.utf8)
        filter.correctionLevel = "M"
        guard let out = filter.outputImage?.transformed(by: CGAffineTransform(scaleX: 10, y: 10)),
              let cg = ctx.createCGImage(out, from: out.extent) else { return nil }
        return Image(decorative: cg, scale: 1, orientation: .up)
    }

    // MARK: — Actions

    private func startDeviceCode() {
        enrollError = nil
        device = nil
        pollTask?.cancel()
        pollTask = Task {
            let client = state.makeEnrollmentClient()
            do {
                let start = try await client.start(client: .appletv,
                                                   appVersion: PrivycsCoreInfo.version)
                await MainActor.run { self.device = start }
                await pollLoop(client: client, start: start)
            } catch {
                await MainActor.run {
                    self.enrollError = String(localized: "tv.enroll.device_code_unavailable")
                }
            }
        }
    }

    private func pollLoop(client: TVDeviceEnrollment, start: TVDeviceStart) async {
        await MainActor.run { self.polling = true }
        defer { Task { @MainActor in self.polling = false } }
        var interval = UInt64(max(1, start.interval)) * 1_000_000_000
        // Rotate ~15s BEFORE the server expires the code so a valid code + QR is
        // always on screen — the user never hits "this code has expired".
        let deadline = Date().addingTimeInterval(TimeInterval(max(30, start.expiresIn - 15)))
        while !Task.isCancelled, Date() < deadline {
            try? await Task.sleep(nanoseconds: interval)
            if Task.isCancelled { return }
            do {
                switch try await client.poll(deviceCode: start.deviceCode) {
                case .pending:
                    continue
                case .slowDown:
                    interval += 2_000_000_000
                case .expired:
                    await MainActor.run { self.startDeviceCode() }   // auto-rotate to a fresh code
                    return
                case let .approved(token, gatewayURL, _):
                    await state.applyEnrollment(gatewayURL: gatewayURL, token: token)
                    return
                }
            } catch {
                await MainActor.run { self.enrollError = error.localizedDescription }
                return
            }
        }
        // Code is about to expire without approval → fetch a fresh one (rotate).
        if !Task.isCancelled {
            await MainActor.run { self.startDeviceCode() }
        }
    }

    private func applyManual() {
        let url = manualURL.trimmingCharacters(in: .whitespacesAndNewlines)
        let token = manualToken.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !url.isEmpty, !token.isEmpty else { return }
        Task { await state.applyEnrollment(gatewayURL: url, token: token) }
    }
}
