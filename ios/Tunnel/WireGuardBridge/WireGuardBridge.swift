import Foundation
import NetworkExtension
import PrivycsCore
import os
#if canImport(WireGuardKit)
import WireGuardKit
#endif

/// WireGuardKit-based packet tunnel bridge. Liest `wg-quick`-style
/// Config aus providerConfig["config_content"], parsed über
/// WireGuardKit's TunnelConfiguration, dispatcht zu device manager.
///
/// Phase 2-MVP — connect/disconnect, kein traffic-stats-streaming
/// noch (Phase 3 wenn UI dafür baut).
public final class WireGuardBridge: TunnelProtocolBridge, @unchecked Sendable {
    private unowned let provider: NEPacketTunnelProvider
    private let logger = Logger(subsystem: "com.privycs.vpn.tunnel", category: "WGBridge")

#if canImport(WireGuardKit)
    private var adapter: WireGuardAdapter?
#endif

    public init(provider: NEPacketTunnelProvider) {
        self.provider = provider
    }

    public func start(providerConfig: [String: Any]) async throws {
#if canImport(WireGuardKit)
        guard let raw = providerConfig["config_content"] as? String, !raw.isEmpty else {
            throw TunnelError.missingProviderConfig
        }
        // Parse via WireGuardKit's standard ini-config parser.
        guard let tunnelConfig = try? TunnelConfiguration(fromWgQuickConfig: raw, called: "privycs") else {
            throw TunnelError.nativeFault("WireGuard config parse failed")
        }
        let adapter = WireGuardAdapter(with: provider) { [weak self] _, message in
            self?.logger.info("WG: \(message, privacy: .public)")
        }
        self.adapter = adapter
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            adapter.start(tunnelConfiguration: tunnelConfig) { error in
                if let error {
                    cont.resume(throwing: TunnelError.nativeFault("WireGuard start: \(error)"))
                } else {
                    cont.resume(returning: ())
                }
            }
        }
#else
        throw TunnelError.bridgeNotImplemented(.wireguard)
#endif
    }

    public func stop(reason: NEProviderStopReason) async {
#if canImport(WireGuardKit)
        guard let adapter else { return }
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            adapter.stop { _ in cont.resume() }
        }
#endif
    }
}
