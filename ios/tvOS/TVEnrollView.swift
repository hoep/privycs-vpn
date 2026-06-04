import SwiftUI
import PrivycsCore

/// Onboarding / enrollment screen. Two paths to the same `(gatewayURL, token)`
/// pair:
///   1. **Device-code** ("link a TV"): show a short code + the privycs.com/link
///      URL, poll until the user approves on their phone. (Gateway endpoints
///      ship later — until then `start` fails and the user uses path 2.)
///   2. **Manual fallback**: focusable fields to paste a gateway URL + token,
///      so the TV is usable today before the device-code backend exists.
struct TVEnrollView: View {
    @EnvironmentObject private var state: TVAppState

    @State private var device: TVDeviceStart?
    @State private var enrollError: String?
    @State private var polling = false
    @State private var pollTask: Task<Void, Never>?

    // Manual fallback fields.
    @State private var manualURL = ""
    @State private var manualToken = ""

    var body: some View {
        HStack(alignment: .top, spacing: 80) {
            deviceCodeColumn
            Divider()
            manualColumn
        }
        .padding(80)
        .onAppear { startDeviceCode() }
        .onDisappear { pollTask?.cancel() }
    }

    // MARK: — Device-code column

    private var deviceCodeColumn: some View {
        VStack(alignment: .leading, spacing: 32) {
            Text("tv.enroll.title", tableName: nil)
                .font(.largeTitle).bold()
            Text("tv.enroll.subtitle", tableName: nil)
                .font(.title3)
                .foregroundStyle(.secondary)

            if let device {
                VStack(alignment: .leading, spacing: 16) {
                    Text("tv.enroll.go_to", tableName: nil)
                        .font(.title2)
                    Text(device.verificationURI)
                        .font(.title).bold()
                        .foregroundStyle(.tint)
                    Text("tv.enroll.enter_code", tableName: nil)
                        .font(.title2)
                    Text(device.userCode)
                        .font(.system(size: 72, weight: .bold, design: .monospaced))
                        .tracking(8)
                        .accessibilityLabel(device.userCode)
                }
                if polling {
                    ProgressView().padding(.top, 8)
                }
            } else if let enrollError {
                Text(enrollError)
                    .font(.headline)
                    .foregroundStyle(.red)
                Button {
                    startDeviceCode()
                } label: {
                    Label(String(localized: "tv.enroll.retry"), systemImage: "arrow.clockwise")
                }
            } else {
                ProgressView()
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    // MARK: — Manual fallback column

    private var manualColumn: some View {
        VStack(alignment: .leading, spacing: 24) {
            Text("tv.enroll.manual_title", tableName: nil)
                .font(.title).bold()
            Text("tv.enroll.manual_hint", tableName: nil)
                .font(.body)
                .foregroundStyle(.secondary)

            TextField(String(localized: "tv.enroll.gateway_url"), text: $manualURL)
                .textContentType(.URL)
            TextField(String(localized: "tv.enroll.token"), text: $manualToken)

            Button {
                applyManual()
            } label: {
                Text("tv.enroll.link", tableName: nil)
                    .frame(maxWidth: .infinity)
            }
            .disabled(manualURL.isEmpty || manualToken.isEmpty)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
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
                    // Expected until the gateway ships the endpoints — the
                    // manual fallback stays available.
                    self.enrollError = String(localized: "tv.enroll.device_code_unavailable")
                }
            }
        }
    }

    private func pollLoop(client: TVDeviceEnrollment, start: TVDeviceStart) async {
        await MainActor.run { self.polling = true }
        defer { Task { @MainActor in self.polling = false } }
        var interval = UInt64(max(1, start.interval)) * 1_000_000_000
        let deadline = Date().addingTimeInterval(TimeInterval(start.expiresIn))
        while !Task.isCancelled, Date() < deadline {
            try? await Task.sleep(nanoseconds: interval)
            if Task.isCancelled { return }
            do {
                switch try await client.poll(deviceCode: start.deviceCode) {
                case .pending:
                    continue
                case .slowDown:
                    interval += 2_000_000_000   // back off per RFC 8628
                case .expired:
                    await MainActor.run { self.enrollError = String(localized: "tv.enroll.code_expired") }
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
    }

    private func applyManual() {
        let url = manualURL.trimmingCharacters(in: .whitespacesAndNewlines)
        let token = manualToken.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !url.isEmpty, !token.isEmpty else { return }
        Task { await state.applyEnrollment(gatewayURL: url, token: token) }
    }
}
