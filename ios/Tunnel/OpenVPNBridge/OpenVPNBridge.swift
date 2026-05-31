import Foundation
import NetworkExtension
import PrivycsCore
import os
#if canImport(OpenVPNAdapter)
import OpenVPNAdapter
#endif

/// OpenVPN packet tunnel bridge via OpenVPNAdapter (Apache 2 via
/// OpenVPN3, License-clean für App-Store-Distribution — siehe
/// LICENSE_NOTES.md).
///
/// Phase 2-MVP — connect/disconnect mit basic config + stats stub.
///
/// **OpenVPN 2.x → 3.x Compat-Layer**: vor dem Übergeben an
/// OpenVPNAdapter wird der `.ovpn` raw-Inhalt durch
/// `OVPNCompatPreprocessor.preprocess(_:)` geschickt (kommt in
/// einem späteren Phase-2.5-Commit). Strippt unsupported
/// directives, normalisiert Legacy-Syntax.
public final class OpenVPNBridge: TunnelProtocolBridge, @unchecked Sendable {
    private unowned let provider: NEPacketTunnelProvider
    private let logger = Logger(subsystem: "com.privycs.vpn.tunnel", category: "OVPNBridge")

#if canImport(OpenVPNAdapter)
    private var adapter: OpenVPNAdapter?
#endif

    public init(provider: NEPacketTunnelProvider) {
        self.provider = provider
    }

    public func start(providerConfig: [String: Any]) async throws {
#if canImport(OpenVPNAdapter)
        guard let raw = providerConfig["config_content"] as? String, !raw.isEmpty else {
            throw TunnelError.missingProviderConfig
        }
        // TODO Phase 2.5: hier OVPNCompatPreprocessor.preprocess(raw) → cleaned + warnings
        let configData = raw.data(using: .utf8)!

        let adapter = OpenVPNAdapter()
        self.adapter = adapter
        let config = OpenVPNConfiguration()
        config.fileContent = configData

        do {
            _ = try adapter.apply(configuration: config)
        } catch {
            throw TunnelError.nativeFault("OpenVPN config apply: \(error)")
        }
        // OpenVPNAdapter braucht einen delegate — Phase 2.5 implementiert
        // den als separater object damit die Bridge mit ihrer Lifecycle
        // klar bleibt.
        throw TunnelError.bridgeNotImplemented(.openvpn)
#else
        throw TunnelError.bridgeNotImplemented(.openvpn)
#endif
    }

    public func stop(reason: NEProviderStopReason) async {
        logger.info("OVPN stop")
#if canImport(OpenVPNAdapter)
        adapter?.disconnect()
#endif
    }
}
