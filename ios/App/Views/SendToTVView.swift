import SwiftUI
import PrivycsCore

/// Path B: push a VPN config or an encrypted backup from this phone to a Privycs
/// Apple TV over the LOCAL NETWORK. The TV shows a QR (its LAN URL + a PIN); we
/// scan it and POST to the TV's on-device import server — the same endpoint the
/// browser-upload form (path A) uses. No gateway, no cloud.
///
/// Requires NSLocalNetworkUsageDescription + NSAppTransportSecurity →
/// NSAllowsLocalNetworking (cleartext HTTP to the LAN IP) in the app Info.plist.
struct SendToTVView: View {
    @EnvironmentObject private var appState: AppState
    @State private var target: URL?
    @State private var pin = ""
    @State private var showScanner = false
    @State private var mode = "config"            // "config" | "pool" | "backup"
    @State private var selectedConnID: String?
    @State private var selectedPoolID: String?
    @State private var passphrase = ""
    @State private var status: String?
    @State private var isError = false
    @State private var busy = false

    var body: some View {
        Form {
            if target == nil {
                Section {
                    Button {
                        status = nil; showScanner = true
                    } label: { Label(loc("Scan the code on your Apple TV"), systemImage: "qrcode.viewfinder") }
                } footer: {
                    Text(loc("On the Apple TV open Configs → Add / restore. Scan the code it shows."))
                }
            } else {
                Section(loc("Apple TV")) {
                    Label(target?.host ?? "", systemImage: "appletv")
                    Button(loc("Scan a different code")) { target = nil; pin = "" }
                        .font(.callout)
                }
                Section {
                    Picker(loc("What to send"), selection: $mode) {
                        Text(loc("A connection")).tag("config")
                        if !appState.pools.isEmpty { Text(loc("A pool")).tag("pool") }
                        Text(loc("Encrypted backup")).tag("backup")
                    }
                    if mode == "config" {
                        Picker(loc("Connection"), selection: $selectedConnID) {
                            ForEach(appState.connections) { c in Text(c.name).tag(Optional(c.id)) }
                        }
                    } else if mode == "pool" {
                        Picker(loc("Pool"), selection: $selectedPoolID) {
                            ForEach(appState.pools) { p in Text(p.name).tag(Optional(p.id)) }
                        }
                    } else {
                        SecureField(loc("Backup passphrase"), text: $passphrase)
                            .textInputAutocapitalization(.never).autocorrectionDisabled()
                    }
                }
                Section {
                    Button {
                        Task { await send() }
                    } label: {
                        if busy { ProgressView() }
                        else { Label(loc("Send to Apple TV"), systemImage: "paperplane.fill") }
                    }
                    .disabled(busy || !canSend)
                }
            }
            if let status {
                Section {
                    Label(status, systemImage: isError ? "xmark.circle.fill" : "checkmark.circle.fill")
                        .foregroundStyle(isError ? PrivycsColor.error : PrivycsColor.connected)
                        .font(.callout)
                }
            }
        }
        .navigationTitle(loc("Send to Apple TV"))
        .navigationBarTitleDisplayMode(.inline)
        .sheet(isPresented: $showScanner) {
            QRScannerView { raw in
                showScanner = false
                Task { @MainActor in handleScan(raw) }
            }
            .ignoresSafeArea()
        }
        .onAppear {
            if selectedConnID == nil { selectedConnID = appState.connections.first?.id }
            if selectedPoolID == nil { selectedPoolID = appState.pools.first?.id }
        }
    }

    private var canSend: Bool {
        guard target != nil else { return false }
        switch mode {
        case "config": return selectedConnID != nil
        case "pool":   return poolWGCount > 0
        default:       return passphrase.count >= 4
        }
    }

    /// WireGuard/AmneziaWG members in the selected pool — only these can run on
    /// the Apple TV (no OpenVPN/IPSec there).
    private var poolWGCount: Int {
        guard let p = appState.pools.first(where: { $0.id == selectedPoolID }) else { return 0 }
        return p.members.filter { $0.config.protocol == .wireguard || $0.config.protocol == .amneziawg }.count
    }

    private func handleScan(_ raw: String) {
        guard let comps = URLComponents(string: raw),
              comps.scheme?.hasPrefix("http") == true,
              comps.path.contains("link"),
              let p = comps.queryItems?.first(where: { $0.name == "pin" })?.value,
              let url = comps.url else {
            status = loc("That isn't a Privycs Apple TV code."); isError = true; return
        }
        target = url; pin = p; status = nil; isError = false
    }

    private func send() async {
        guard let url = target else { return }
        busy = true; defer { busy = false }
        do {
            var fields: [String: String] = ["pin": pin]
            var successMsg = loc("Sent ✓ — check your Apple TV.")
            switch mode {
            case "config":
                guard let conn = appState.connections.first(where: { $0.id == selectedConnID }),
                      let cfg = conn.resolvedActiveConfig() else {
                    status = loc("Pick a connection to send."); isError = true; return
                }
                fields["kind"] = "config"
                fields["name"] = conn.name
                fields["content"] = cfg.configContent
            case "pool":
                guard let pool = appState.pools.first(where: { $0.id == selectedPoolID }) else {
                    status = loc("Pick a connection to send."); isError = true; return
                }
                // Send the whole Pool object — the Apple TV runs the SAME rotation
                // engine (it filters to WG/AWG members on its side).
                let data = try JSONEncoder().encode(pool)
                fields["kind"] = "pool"
                fields["content"] = String(decoding: data, as: UTF8.self)
                successMsg = String(format: loc("Sent ✓ — %lld server(s) on your Apple TV."), poolWGCount)
            default:
                let data = try await appState.exportBackup(password: passphrase)
                fields["kind"] = "backup"
                fields["content"] = String(decoding: data, as: UTF8.self)
                fields["passphrase"] = passphrase
            }
            try await post(fields, to: url)
            status = successMsg; isError = false
        } catch {
            status = error.localizedDescription; isError = true
        }
    }

    private func post(_ fields: [String: String], to url: URL) async throws {
        var allowed = CharacterSet.urlQueryAllowed
        allowed.remove(charactersIn: "+&=")
        let body = fields.map { k, v in
            "\(k)=\(v.addingPercentEncoding(withAllowedCharacters: allowed) ?? "")"
        }.joined(separator: "&")
        var req = URLRequest(url: url, timeoutInterval: 15)
        req.httpMethod = "POST"
        req.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        req.httpBody = body.data(using: .utf8)
        let (_, resp) = try await URLSession.shared.data(for: req)
        guard let http = resp as? HTTPURLResponse, (200...299).contains(http.statusCode) else {
            throw NSError(domain: "SendToTV", code: 1,
                          userInfo: [NSLocalizedDescriptionKey: loc("The Apple TV rejected the data (check the PIN).")])
        }
    }
}
