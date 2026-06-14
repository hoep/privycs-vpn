import Foundation
import Network

/// What the TV received over the local network — either a raw VPN config (manual
/// import, no gateway) or an encrypted Privycs backup blob (restore).
struct TVImportPayload: Sendable {
    enum Kind: String, Sendable { case config, backup, pool, poolzip, file }
    let kind: Kind
    let name: String        // connection name / uploaded filename
    let content: String     // text (config/backup/pool) · base64 (file/poolzip)
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
                      let raw = fields["content"], !raw.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                // Text payloads (config/backup/pool) may be base64-wrapped (enc=b64)
                // so large JSON survives url-encoding intact. `file`/`poolzip`
                // content stays base64 (binary) — decoded TV-side.
                var content = raw
                if fields["enc"] == "b64", kind != .file, kind != .poolzip,
                   let d = Data(base64Encoded: raw), let s = String(data: d, encoding: .utf8) {
                    content = s
                }
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
        <p class="sub">Send a VPN file or config to your Apple TV over your local network — no gateway, no cloud.</p>
        <div class="card">
          <h2>Upload a file</h2>
          <input type="file" id="vpnfile">
          <button type="button" onclick="sendFile()">Upload to Apple TV</button>
          <p class="hint">.zip = a server pool · .conf = WireGuard / AmneziaWG · .ovpn / .sswan accepted (Apple TV runs WireGuard &amp; AmneziaWG)</p>
        </div>
        <div class="card">
          <h2>Or paste</h2>
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
            <label>Contents</label>
            <textarea name="content" placeholder="[Interface] ... or the .pvcbackup contents"></textarea>
            <button type="submit">Send to Apple TV</button>
          </form>
        </div>
        <script>
        function sendFile(){
          var f=document.getElementById('vpnfile').files[0];
          if(!f){alert('Pick a file first');return;}
          var r=new FileReader();
          r.onload=function(){
            var b64=r.result.split(',')[1];
            fetch('/link',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},
              body:'kind=file&pin=\(pin)&name='+encodeURIComponent(f.name)+'&content='+encodeURIComponent(b64)})
              .then(function(){document.body.innerHTML='<div class="wrap"><h1>Sent ✓</h1><p class="sub">Sent to your Apple TV. You can close this page.</p></div>';})
              .catch(function(){alert('Upload failed');});
          };
          r.readAsDataURL(f);
        }
        </script>
        """)
    }

    /// The page is served to a phone browser, so it follows the BROWSER's
    /// dark/light preference via `prefers-color-scheme` (CSS custom properties).
    private static func page(title: String, body: String) -> String {
        """
        <!doctype html><html><head><meta charset="utf-8">
        <meta name="viewport" content="width=device-width, initial-scale=1">
        <meta name="color-scheme" content="dark light">
        <title>Privycs · \(title)</title>
        <style>
          :root{--bg:#070B0E;--card:#0E161C;--fg:#EAF1F3;--muted:#9DB2BD;--line:#243038;--teal:#00CDAB;--onTeal:#04130F}
          @media (prefers-color-scheme: light){
            :root{--bg:#EEF3F2;--card:#FFFFFF;--fg:#0E161C;--muted:#5C7280;--line:#D7E0E1;--teal:#0B9E84;--onTeal:#FFFFFF}
          }
          *{box-sizing:border-box}
          body{font-family:-apple-system,system-ui,sans-serif;background:var(--bg);color:var(--fg);margin:0;-webkit-font-smoothing:antialiased}
          .wrap{max-width:560px;margin:0 auto;padding:28px 20px}
          .brand{display:flex;align-items:center;gap:10px;margin-bottom:14px}
          .brand .dot{width:10px;height:10px;border-radius:50%;background:var(--teal);box-shadow:0 0 10px var(--teal)}
          .brand b{font-size:15px;letter-spacing:.03em}
          .brand span{font-size:11px;color:var(--teal);font-family:ui-monospace,monospace}
          h1{font-size:24px;margin:6px 0 4px}
          .sub{color:var(--muted);font-size:14px;line-height:1.45;margin:0 0 22px}
          .card{background:var(--card);border:1px solid var(--line);border-radius:16px;padding:18px;margin-bottom:18px}
          .card h2{font-size:13px;margin:0 0 12px;color:var(--teal);text-transform:uppercase;letter-spacing:.1em}
          label{display:block;margin:12px 0 5px;color:var(--muted);font-size:13px}
          input,select,textarea{width:100%;padding:12px;border-radius:10px;border:1px solid var(--line);background:var(--bg);color:var(--fg);font-size:16px}
          input[type=file]{padding:10px;background:transparent}
          textarea{font-family:ui-monospace,monospace;min-height:150px}
          button{margin-top:16px;width:100%;padding:14px;border:0;border-radius:12px;background:var(--teal);color:var(--onTeal);font-size:17px;font-weight:700}
          .hint{color:var(--muted);font-size:12px;line-height:1.5;margin:10px 0 0}
        </style></head><body><div class="wrap">
        <div class="brand"><span class="dot"></span><b>Privycs</b><span>Secure.Private.Simple.</span></div>
        <h1>\(title)</h1>
        \(body)
        </div></body></html>
        """
    }
}
