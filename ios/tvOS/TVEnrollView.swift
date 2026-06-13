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
        VStack(alignment: .leading, spacing: 0) {
            brandHeader
            VStack(alignment: .leading, spacing: 10) {
                Text(loc("tv.enroll.kicker"))
                    .font(TVFont.mono(15)).tracking(3).foregroundStyle(TVColor.teal)
                Text("tv.enroll.title", tableName: nil)
                    .font(TVFont.sans(46, .bold)).foregroundStyle(TVColor.onSurface)
                Text("tv.enroll.subtitle", tableName: nil)
                    .font(TVFont.sans(22)).foregroundStyle(TVColor.onSurfaceVariant)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.bottom, 38)

            HStack(alignment: .top, spacing: 40) {
                deviceCodeCard
                manualCard
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 80)
        .padding(.vertical, 56)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .onAppear { startDeviceCode() }
        .onDisappear { pollTask?.cancel() }
    }

    private var brandHeader: some View {
        HStack(spacing: 16) {
            Image("ic_privycs_logo").resizable().scaledToFit().frame(width: 60, height: 60)
                .clipShape(RoundedRectangle(cornerRadius: 15))
            VStack(alignment: .leading, spacing: 2) {
                Text("Privycs").font(TVFont.sans(30, .bold)).foregroundStyle(TVColor.onSurface)
                Text("VPN").font(TVFont.mono(14)).foregroundStyle(TVColor.teal)
            }
            Spacer()
        }
        .padding(.bottom, 30)
    }

    // MARK: — Device-code card (QR + code)

    private var deviceCodeCard: some View {
        VStack(alignment: .leading, spacing: 24) {
            cardTitle(icon: "qrcode", text: loc("tv.enroll.device_title"))
            if let device {
                HStack(alignment: .top, spacing: 34) {
                    qrView(device.verificationURIComplete)
                    VStack(alignment: .leading, spacing: 18) {
                        stepRow("1", loc("tv.enroll.scan_or_open"))
                        Text(shortURL(device.verificationURI))
                            .font(TVFont.mono(26, .bold))
                            .foregroundStyle(TVColor.teal)
                            .lineLimit(1).minimumScaleFactor(0.5)
                            .padding(.leading, 40)
                        stepRow("2", loc("tv.enroll.enter_code"))
                        Text(device.userCode)
                            .font(TVFont.mono(54, .bold))
                            .tracking(8)
                            .foregroundStyle(TVColor.onSurface)
                            .lineLimit(1).minimumScaleFactor(0.6)
                            .padding(.leading, 40)
                            .accessibilityLabel(device.userCode)
                        if polling {
                            HStack(spacing: 12) {
                                ProgressView().tint(TVColor.teal)
                                Text(loc("tv.enroll.waiting"))
                                    .font(TVFont.mono(17)).foregroundStyle(TVColor.onSurfaceVariant)
                            }
                            .padding(.top, 4).padding(.leading, 40)
                        }
                    }
                }
            } else if let enrollError {
                VStack(alignment: .leading, spacing: 18) {
                    Text(enrollError).font(TVFont.sans(20)).foregroundStyle(TVColor.error)
                        .fixedSize(horizontal: false, vertical: true)
                    Button { startDeviceCode() } label: {
                        HStack(spacing: 12) {
                            Image(systemName: "arrow.clockwise").font(.system(size: 22, weight: .semibold))
                            Text("tv.enroll.retry", tableName: nil).font(TVFont.sans(22, .semibold))
                        }
                        .foregroundStyle(TVColor.teal)
                        .padding(.horizontal, 28).padding(.vertical, 16)
                    }
                    .buttonStyle(.card)
                }
                .frame(maxWidth: .infinity, minHeight: 260, alignment: .leading)
            } else {
                ProgressView().tint(TVColor.teal).frame(maxWidth: .infinity, minHeight: 260)
            }
        }
        .padding(34)
        .frame(maxWidth: .infinity, minHeight: 400, alignment: .topLeading)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 26))
        .overlay(RoundedRectangle(cornerRadius: 26).stroke(TVColor.outline, lineWidth: 1))
    }

    private func qrView(_ string: String) -> some View {
        Group {
            if let img = makeQR(string) {
                img.interpolation(.none).resizable().scaledToFit()
            } else {
                Image(systemName: "qrcode").resizable().scaledToFit().foregroundStyle(.black)
            }
        }
        .frame(width: 230, height: 230)
        .padding(18)
        .background(Color.white, in: RoundedRectangle(cornerRadius: 18))
    }

    // MARK: — Manual fallback card

    private var manualCard: some View {
        VStack(alignment: .leading, spacing: 18) {
            cardTitle(icon: "keyboard", text: loc("tv.enroll.manual_title"))
            Text("tv.enroll.manual_hint", tableName: nil)
                .font(TVFont.sans(18)).foregroundStyle(TVColor.onSurfaceVariant)
                .fixedSize(horizontal: false, vertical: true)

            TextField(loc("tv.enroll.gateway_url"), text: $manualURL)
                .textContentType(.URL).font(TVFont.mono(20))
            TextField(loc("tv.enroll.token"), text: $manualToken)
                .font(TVFont.mono(20))

            Button { applyManual() } label: {
                Text("tv.enroll.link", tableName: nil)
                    .font(TVFont.sans(22, .semibold))
                    .foregroundStyle(canLinkManually ? TVColor.teal : TVColor.onSurfaceVariant)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 16)
            }
            .buttonStyle(.card)
            .disabled(!canLinkManually)
        }
        .padding(34)
        .frame(width: 520, alignment: .leading)
        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 26))
        .overlay(RoundedRectangle(cornerRadius: 26).stroke(TVColor.outline, lineWidth: 1))
    }

    private var canLinkManually: Bool {
        !manualURL.trimmingCharacters(in: .whitespaces).isEmpty &&
        !manualToken.trimmingCharacters(in: .whitespaces).isEmpty
    }

    private func cardTitle(icon: String, text: String) -> some View {
        HStack(spacing: 12) {
            Image(systemName: icon).font(.system(size: 24, weight: .semibold)).foregroundStyle(TVColor.teal)
            Text(text).font(TVFont.sans(26, .bold)).foregroundStyle(TVColor.onSurface)
        }
    }

    private func stepRow(_ n: String, _ text: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 14) {
            Text(n).font(TVFont.mono(16, .bold)).foregroundStyle(TVColor.onTeal)
                .frame(width: 26, height: 26).background(TVColor.teal, in: Circle())
            Text(text).font(TVFont.sans(20, .medium)).foregroundStyle(TVColor.onSurface)
        }
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
                    self.enrollError = loc("tv.enroll.device_code_unavailable")
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
