import Foundation
import NetworkExtension
import PrivycsCore
import os
#if canImport(OpenVPNAdapter)
import OpenVPNAdapter
#endif

/// OpenVPN packet tunnel bridge via OpenVPNAdapter (Apache 2 via
/// OpenVPN3). Vor dem Übergeben an OVPN3 läuft der Config-Inhalt
/// durch `OVPNCompatPreprocessor` — strippt unsupported directives,
/// normalisiert Legacy-Syntax.
public final class OpenVPNBridge: NSObject, TunnelProtocolBridge, @unchecked Sendable {
    private unowned let provider: NEPacketTunnelProvider
    private let logger = Logger(subsystem: "com.privycs.vpn.tunnel", category: "OVPNBridge")
    private var connectContinuation: CheckedContinuation<Void, Error>?

#if canImport(OpenVPNAdapter)
    private var adapter: OpenVPNAdapter?
    private var packetFlow: NEPacketTunnelFlow?
#endif

    public init(provider: NEPacketTunnelProvider) {
        self.provider = provider
        super.init()
    }

    public func start(providerConfig: [String: Any]) async throws {
#if canImport(OpenVPNAdapter)
        guard let raw = providerConfig["config_content"] as? String, !raw.isEmpty else {
            throw TunnelError.missingProviderConfig
        }
        let preprocessed = OVPNCompatPreprocessor().preprocess(raw)
        if !preprocessed.warnings.isEmpty {
            for w in preprocessed.warnings {
                logger.warning("OVPN compat warning: \(String(describing: w), privacy: .public)")
            }
        }
        guard let configData = preprocessed.cleanedConfig.data(using: .utf8) else {
            throw TunnelError.nativeFault("OVPN config UTF-8 encoding")
        }

        let adapter = OpenVPNAdapter()
        adapter.delegate = self
        self.adapter = adapter

        let config = OpenVPNConfiguration()
        config.fileContent = configData

        do {
            _ = try adapter.apply(configuration: config)
        } catch {
            throw TunnelError.nativeFault("OpenVPN config apply: \(error.localizedDescription)")
        }

        // Wait for the delegate to fire its connected callback. The
        // bridge resumes the continuation in `openVPNAdapterDidConnect`
        // OR with throw on error / disconnect-before-connect.
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            connectContinuation = cont
            adapter.connect(using: provider.packetFlow)
        }
#else
        throw TunnelError.bridgeNotImplemented(.openvpn)
#endif
    }

    public func stop(reason: NEProviderStopReason) async {
        logger.info("OVPN stop reason=\(reason.rawValue)")
#if canImport(OpenVPNAdapter)
        adapter?.disconnect()
        adapter = nil
#endif
    }
}

#if canImport(OpenVPNAdapter)
extension OpenVPNBridge: OpenVPNAdapterDelegate {
    public func openVPNAdapter(
        _ adapter: OpenVPNAdapter,
        configureTunnelWithNetworkSettings settings: NEPacketTunnelNetworkSettings?,
        completionHandler: @escaping (Error?) -> Void
    ) {
        if let settings {
            provider.setTunnelNetworkSettings(settings, completionHandler: completionHandler)
        } else {
            completionHandler(nil)
        }
    }

    public func openVPNAdapter(
        _ adapter: OpenVPNAdapter,
        handleEvent event: OpenVPNAdapterEvent,
        message: String?
    ) {
        logger.info("OVPN event=\(event.rawValue) msg=\(message ?? "", privacy: .public)")
        switch event {
        case .connected:
            connectContinuation?.resume(returning: ())
            connectContinuation = nil
        case .disconnected:
            connectContinuation?.resume(throwing: TunnelError.nativeFault("OpenVPN disconnected before connect"))
            connectContinuation = nil
        default:
            break
        }
    }

    public func openVPNAdapter(_ adapter: OpenVPNAdapter, handleError error: Error) {
        logger.error("OVPN error: \(error.localizedDescription, privacy: .public)")
        connectContinuation?.resume(throwing: error)
        connectContinuation = nil
    }

    public func openVPNAdapter(_ adapter: OpenVPNAdapter, handleLogMessage logMessage: String) {
        logger.debug("OVPN log: \(logMessage, privacy: .public)")
    }
}
#endif
