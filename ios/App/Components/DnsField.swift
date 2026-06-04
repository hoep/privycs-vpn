import SwiftUI
import Foundation
import PrivycsCore

/// Reusable DNS-override editor — a monospace text field plus a preset
/// menu (the canonical `DnsPresets` list, Android `DnsPresetPicker`
/// parity) and a one-shot resolver test. Used for the global, the
/// per-connection, and the per-pool overrides so the option set and
/// behaviour stay identical everywhere.
struct DnsField: View {
    @Binding var value: String
    /// Called whenever the value is committed (submit / preset / clear).
    var onCommit: () -> Void = {}

    @State private var testing = false
    @State private var testResult: String?
    @State private var testOK = false

    private var detected: DnsProvider? { DnsPresets.detect(value) }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                TextField("e.g. 1.1.1.1, 9.9.9.9", text: $value)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .font(.system(size: 14, design: .monospaced))
                    .onSubmit { onCommit() }
                Menu {
                    Button("Clear (use default)") { value = ""; onCommit() }
                    Divider()
                    ForEach(DnsPresets.providers) { p in
                        Button("\(p.label) — \(p.note)") {
                            value = p.serversJoined
                            onCommit()
                        }
                    }
                } label: {
                    Image(systemName: "list.bullet.circle")
                        .font(.system(size: 18))
                        .foregroundStyle(PrivycsColor.teal)
                }
            }

            if let d = detected {
                Text("Preset: \(d.label) · \(d.note)")
                    .font(.caption2).foregroundStyle(.secondary)
            }

            HStack(spacing: 10) {
                Button {
                    Task { await runTest() }
                } label: {
                    if testing {
                        ProgressView().controlSize(.mini)
                    } else {
                        Label("Test DNS", systemImage: "bolt.horizontal.circle").font(.caption)
                    }
                }
                .buttonStyle(.bordered)
                .disabled(testing)

                if let r = testResult {
                    Text(r)
                        .font(.caption2.monospaced())
                        .foregroundStyle(testOK ? PrivycsColor.connected : PrivycsColor.error)
                        .lineLimit(2)
                }
            }
        }
    }

    private func runTest() async {
        testing = true
        testResult = nil
        defer { testing = false }
        let host = "one.one.one.one"
        let start = Date()
        let addrs = await DnsField.resolve(host)
        let ms = Int(Date().timeIntervalSince(start) * 1000)
        if let first = addrs.first {
            testOK = true
            testResult = "\(host) → \(first) (\(ms) ms)"
        } else {
            testOK = false
            testResult = String(localized: "Resolution failed")
        }
    }

    /// Resolve a hostname via POSIX getaddrinfo on a background queue and
    /// return the numeric addresses. iOS resolves through the active
    /// network / VPN resolver — the app cannot pin a specific server, so
    /// this verifies that resolution works and how fast (Android's DNS
    /// test is likewise reachability-based, not server-pinned).
    nonisolated static func resolve(_ host: String) async -> [String] {
        await withCheckedContinuation { (cont: CheckedContinuation<[String], Never>) in
            DispatchQueue.global().async {
                var res: UnsafeMutablePointer<addrinfo>?
                let err = getaddrinfo(host, nil, nil, &res)
                guard err == 0, res != nil else { cont.resume(returning: []); return }
                defer { freeaddrinfo(res) }
                var out: [String] = []
                var p = res
                while let cur = p {
                    var buf = [CChar](repeating: 0, count: Int(NI_MAXHOST))
                    if getnameinfo(cur.pointee.ai_addr, cur.pointee.ai_addrlen,
                                   &buf, socklen_t(buf.count), nil, 0, NI_NUMERICHOST) == 0 {
                        let s = String(cString: buf)
                        if !out.contains(s) { out.append(s) }
                    }
                    p = cur.pointee.ai_next
                }
                cont.resume(returning: out)
            }
        }
    }
}
