import Foundation
import Network

/// What the TV received over the local network — either a raw VPN config (manual
/// import, no gateway) or an encrypted Privycs backup blob (restore).
struct TVImportPayload: Sendable {
    enum Kind: String, Sendable { case config, backup }
    let kind: Kind
    let name: String        // connection name (config) — ignored for backup
    let content: String     // .conf text, or the backup JSON envelope
    let passphrase: String   // backup only
}

/// Tiny on-device HTTP listener that lets a config / backup reach the Apple TV
/// over the LOCAL NETWORK without the gateway and without a Files app — the TV
/// can't scan a QR (no camera), so it *shows* one instead. Two senders hit the
/// SAME endpoint:
///   • A — any phone's camera opens `http://<tv-ip>:<port>/link?pin=NNNN` in the
///         browser → a paste/upload form (served by GET) → POST.
///   • B — the Privycs iPhone app scans the same QR and POSTs the active config
///         or a backup programmatically.
///
/// PIN-gated, ephemeral (runs only while the import screen is open), bound to the
/// LAN. Advertises a Bonjour service so tvOS shows the Local Network prompt and
/// the listener is discoverable.
///
/// Not @MainActor: the connection handlers run on `queue` (a background queue),
/// so they stay nonisolated; only the `@Published` UI state + `onPayload` are
/// bounced back to the main thread.
final class TVImportServer: ObservableObject {
    @Published private(set) var isRunning = false
    @Published private(set) var lanURL = ""     // the URL encoded into the QR
    @Published private(set) var pin = ""
    @Published var lastError: String?

    /// Delivered on the MAIN thread when a valid (PIN-checked) payload arrives.
    var onPayload: ((TVImportPayload) -> Void)?

    private var listener: NWListener?
    private let queue = DispatchQueue(label: "com.privycs.tv.import")
    private var currentPIN = ""                  // queue-side copy (set once at start)
    static let bonjourType = "_privycs-tv._tcp"

    func start() {
        guard listener == nil else { return }
        let newPIN = String(format: "%04d", Int.random(in: 0...9999))
        currentPIN = newPIN
        setMain { $0.pin = newPIN; $0.lastError = nil }
        do {
            let params = NWParameters.tcp
            params.allowLocalEndpointReuse = true
            let l = try NWListener(using: params)        // ephemeral port
            l.service = NWListener.Service(name: "Privycs TV", type: Self.bonjourType)
            l.newConnectionHandler = { [weak self] conn in self?.accept(conn) }
            l.stateUpdateHandler = { [weak self] state in
                switch state {
                case .ready:
                    self?.publishURL()
                case .failed(let err):
                    self?.setMain { $0.lastError = err.localizedDescription; $0.isRunning = false }
                default: break
                }
            }
            l.start(queue: queue)
            listener = l
            setMain { $0.isRunning = true }
        } catch {
            setMain { $0.lastError = error.localizedDescription; $0.isRunning = false }
        }
    }

    func stop() {
        listener?.cancel()
        listener = nil
        setMain { $0.isRunning = false; $0.lanURL = "" }
    }

    /// Mutate the published UI state on the main thread.
    private func setMain(_ mutate: @escaping (TVImportServer) -> Void) {
        DispatchQueue.main.async { [weak self] in guard let self else { return }; mutate(self) }
    }

    private func publishURL() {
        guard let port = listener?.port?.rawValue, let ip = Self.lanIPv4() else {
            setMain { if $0.lanURL.isEmpty { $0.lastError = "No local network address" } }
            return
        }
        let url = "http://\(ip):\(port)/link?pin=\(currentPIN)"
        setMain { $0.lanURL = url }
    }

    // MARK: — Connection handling (minimal HTTP/1.1) — runs on `queue`

    private func accept(_ conn: NWConnection) {
        conn.start(queue: queue)
        receive(conn, buffer: Data())
    }

    private func receive(_ conn: NWConnection, buffer: Data) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] data, _, isComplete, error in
            guard let self else { return }
            var buf = buffer
            if let data { buf.append(data) }
            if error != nil { conn.cancel(); return }

            guard let headerEnd = buf.range(of: Data("\r\n\r\n".utf8)) else {
                if isComplete { conn.cancel() } else { self.receive(conn, buffer: buf) }
                return
            }
            let header = String(decoding: buf[..<headerEnd.lowerBound], as: UTF8.self)
            let needed = Self.contentLength(header)
            let bodyStart = headerEnd.upperBound
            let have = buf.count - bodyStart
            if have < needed, !isComplete {
                self.receive(conn, buffer: buf)
                return
            }
            let body = Data(buf[bodyStart...])
            self.respond(conn, header: header, body: body)
        }
    }

    private func respond(_ conn: NWConnection, header: String, body: Data) {
        let firstLine = header.split(separator: "\r\n", maxSplits: 1).first.map(String.init) ?? ""
        let isPost = firstLine.uppercased().hasPrefix("POST")
        let html: String
        var status = "200 OK"

        if isPost {
            let fields = Self.parseForm(String(decoding: body, as: UTF8.self))
            if fields["pin"] != currentPIN {
                status = "403 Forbidden"
                html = Self.page(title: "Wrong PIN", body: "<p>The PIN doesn't match the one on your TV.</p>")
            } else if let kind = TVImportPayload.Kind(rawValue: fields["kind"] ?? "config"),
                      let content = fields["content"], !content.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                let payload = TVImportPayload(
                    kind: kind,
                    name: (fields["name"] ?? "").trimmingCharacters(in: .whitespacesAndNewlines),
                    content: content,
                    passphrase: fields["passphrase"] ?? ""
                )
                let cb = onPayload
                DispatchQueue.main.async { cb?(payload) }
                html = Self.page(title: "Sent ✓", body: "<p>Sent to your Apple TV. You can close this page.</p>")
            } else {
                status = "400 Bad Request"
                html = Self.page(title: "Nothing to send", body: "<p>Paste a configuration or backup first.</p>")
            }
        } else {
            html = Self.formPage(pin: currentPIN)
        }

        var resp = "HTTP/1.1 \(status)\r\n"
        resp += "Content-Type: text/html; charset=utf-8\r\n"
        let bytes = Array(html.utf8)
        resp += "Content-Length: \(bytes.count)\r\n"
        resp += "Connection: close\r\n\r\n"
        var out = Data(resp.utf8); out.append(contentsOf: bytes)
        conn.send(content: out, completion: .contentProcessed { _ in conn.cancel() })
    }

    // MARK: — Helpers (nonisolated / pure)

    private static func contentLength(_ header: String) -> Int {
        for line in header.split(separator: "\r\n") {
            let p = line.split(separator: ":", maxSplits: 1)
            if p.count == 2, p[0].trimmingCharacters(in: .whitespaces).lowercased() == "content-length" {
                return Int(p[1].trimmingCharacters(in: .whitespaces)) ?? 0
            }
        }
        return 0
    }

    /// Parse `application/x-www-form-urlencoded` into a dict.
    private static func parseForm(_ s: String) -> [String: String] {
        var out: [String: String] = [:]
        for pair in s.split(separator: "&") {
            let kv = pair.split(separator: "=", maxSplits: 1)
            guard let k = kv.first.map(String.init) else { continue }
            let v = kv.count > 1 ? String(kv[1]) : ""
            out[urlDecode(k)] = urlDecode(v)
        }
        return out
    }

    private static func urlDecode(_ s: String) -> String {
        s.replacingOccurrences(of: "+", with: " ").removingPercentEncoding ?? s
    }

    /// First non-loopback IPv4 on a Wi-Fi/Ethernet interface.
    static func lanIPv4() -> String? {
        var addr: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&addr) == 0, let first = addr else { return nil }
        defer { freeifaddrs(addr) }
        var result: String?
        var ptr: UnsafeMutablePointer<ifaddrs>? = first
        while let cur = ptr {
            let flags = Int32(cur.pointee.ifa_flags)
            if let sa = cur.pointee.ifa_addr,
               (flags & (IFF_UP | IFF_RUNNING)) == (IFF_UP | IFF_RUNNING),
               (flags & IFF_LOOPBACK) == 0, sa.pointee.sa_family == UInt8(AF_INET) {
                let name = String(cString: cur.pointee.ifa_name)
                if name.hasPrefix("en") || name.hasPrefix("eth") || name.hasPrefix("pdp") {
                    var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
                    if getnameinfo(sa, socklen_t(sa.pointee.sa_len),
                                   &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 {
                        result = String(cString: host)
                        if name.hasPrefix("en") { break }   // prefer Wi-Fi/Ethernet
                    }
                }
            }
            ptr = cur.pointee.ifa_next
        }
        return result
    }

    // MARK: — Served HTML (the browser-upload form for path A)

    private static func formPage(pin: String) -> String {
        page(title: "Send to Apple TV", body: """
        <form method="POST" action="/link">
          <input type="hidden" name="pin" value="\(pin)">
          <label>Type</label>
          <select name="kind">
            <option value="config">VPN configuration (.conf)</option>
            <option value="backup">Encrypted backup (restore)</option>
          </select>
          <label>Name (configuration only)</label>
          <input type="text" name="name" placeholder="My VPN">
          <label>Passphrase (backup only)</label>
          <input type="password" name="passphrase" placeholder="backup passphrase">
          <label>Paste the configuration or backup contents</label>
          <textarea name="content" rows="14" placeholder="[Interface] ... or the .pvcbackup contents"></textarea>
          <button type="submit">Send to Apple TV</button>
        </form>
        """)
    }

    private static func page(title: String, body: String) -> String {
        """
        <!doctype html><html><head><meta charset="utf-8">
        <meta name="viewport" content="width=device-width, initial-scale=1">
        <title>Privycs · \(title)</title>
        <style>
          body{font-family:-apple-system,system-ui,sans-serif;background:#070B0E;color:#EAF1F3;margin:0;padding:24px}
          h1{color:#00CDAB;font-size:20px}
          label{display:block;margin:14px 0 4px;color:#9DB2BD;font-size:13px}
          input,select,textarea{width:100%;box-sizing:border-box;padding:12px;border-radius:10px;border:1px solid #243038;background:#0E161C;color:#EAF1F3;font-size:16px}
          textarea{font-family:ui-monospace,monospace}
          button{margin-top:18px;width:100%;padding:14px;border:0;border-radius:12px;background:#00CDAB;color:#04130F;font-size:17px;font-weight:600}
        </style></head><body><h1>Privycs · \(title)</h1>\(body)</body></html>
        """
    }
}
