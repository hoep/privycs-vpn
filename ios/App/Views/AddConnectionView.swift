import SwiftUI
import UniformTypeIdentifiers
import PrivycsCore

/// Add-Screen — File-Import (wg-quick conf, .ovpn, .sswan,
/// .mobileconfig), QR-scan, Gateway-pull. Phase 2-baseline: nur
/// File-Import; QR + Gateway-pull kommen in Phase 3.
struct AddConnectionView: View {
    @EnvironmentObject private var appState: AppState
    @State private var showFileImporter = false
    @State private var importErrorMessage: String?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    Button {
                        showFileImporter = true
                    } label: {
                        Label("Import from file", systemImage: "doc.badge.plus")
                    }
                    Button {
                        // Phase 3: AVFoundation QR scanner
                    } label: {
                        Label("Scan QR code", systemImage: "qrcode.viewfinder")
                    }
                    .disabled(true)
                    Button {
                        // Phase 3: gateway-pull configs
                    } label: {
                        Label("Pull from Privycs Gateway", systemImage: "cloud.bolt")
                    }
                    .disabled(true)
                } footer: {
                    Text("Supported formats: .conf (WireGuard/AmneziaWG), .ovpn (OpenVPN), .sswan / .mobileconfig (IPSec)")
                }
                if let err = importErrorMessage {
                    Section {
                        Text(err).foregroundStyle(.red).font(.caption)
                    }
                }
            }
            .navigationTitle("Add Config")
            .fileImporter(
                isPresented: $showFileImporter,
                allowedContentTypes: [
                    UTType.data, // generic catch-all; we filter on extension
                ],
                allowsMultipleSelection: true
            ) { result in
                Task { await handleImport(result) }
            }
        }
    }

    private func handleImport(_ result: Result<[URL], Error>) async {
        importErrorMessage = nil
        switch result {
        case .failure(let err):
            importErrorMessage = err.localizedDescription
        case .success(let urls):
            for url in urls {
                guard url.startAccessingSecurityScopedResource() else { continue }
                defer { url.stopAccessingSecurityScopedResource() }
                do {
                    let raw = try String(contentsOf: url, encoding: .utf8)
                    let proto = detectProtocol(filename: url.lastPathComponent, content: raw)
                    let conn = SavedConnection(
                        id: UUID().uuidString,
                        name: url.deletingPathExtension().lastPathComponent,
                        protocols: [
                            ProtocolConfig(
                                id: UUID().uuidString,
                                protocol: proto,
                                filename: url.lastPathComponent,
                                configContent: raw,
                                serverAddress: extractServerAddress(content: raw, protocol: proto)
                            ),
                        ]
                    )
                    try await appState.connectionRepo.save(conn)
                } catch {
                    importErrorMessage = error.localizedDescription
                }
            }
            appState.connections = (try? await appState.connectionRepo.loadAll()) ?? []
        }
    }

    private func detectProtocol(filename: String, content: String) -> VpnProtocol {
        let ext = (filename as NSString).pathExtension.lowercased()
        switch ext {
        case "ovpn": return .openvpn
        case "sswan", "mobileconfig": return .ipsec
        case "conf":
            return content.contains("Jc =") || content.contains("S1 =") ? .amneziawg : .wireguard
        default:
            return .wireguard
        }
    }

    private func extractServerAddress(content: String, `protocol`: VpnProtocol) -> String {
        // Quick-n-dirty extractor — proper parser kommt in Phase 3.
        for line in content.split(separator: "\n") {
            let l = line.trimmingCharacters(in: .whitespaces)
            if l.lowercased().hasPrefix("endpoint") {
                return String(l.split(separator: "=").last?.trimmingCharacters(in: .whitespaces) ?? "")
            }
            if l.lowercased().hasPrefix("remote") {
                let parts = l.split(separator: " ", maxSplits: 2).map(String.init)
                if parts.count >= 2 {
                    return parts[1] + (parts.count > 2 ? ":\(parts[2])" : "")
                }
            }
        }
        return ""
    }
}
