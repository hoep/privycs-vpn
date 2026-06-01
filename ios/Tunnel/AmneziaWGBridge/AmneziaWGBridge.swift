import Foundation
import NetworkExtension
import PrivycsCore
import os
#if canImport(WireGuardKit)
import WireGuardKit
#endif

/// AmneziaWG packet-tunnel bridge.
///
/// AmneziaWG = WireGuard + traffic obfuscation. We link the
/// `amnezia-vpn/amneziawg-apple` fork as the `WireGuardKit` module
/// (its `libwg-go.a` is built from amneziawg-go), so its
/// `TunnelConfiguration` parser understands the AWG obfuscation keys
/// (Jc / Jmin / Jmax / S1-S4 / H1-H4 / I1-I5) in the wg-quick config and
/// the Go backend runs them. That means this bridge is identical to
/// `WireGuardBridge` — ONE backend for both protocols, exactly like
/// Android's unified amneziawg GoBackend (a vanilla config with no AWG
/// keys simply runs as standard WireGuard).
///
/// This replaces the earlier unfinished gomobile-`AmneziaNewDevice`
/// path (which called an API that didn't exist in amneziawg-go and was
/// gated out of every shipped build → "bridge not implemented").
public final class AmneziaWGBridge: TunnelProtocolBridge, @unchecked Sendable {
    private unowned let provider: NEPacketTunnelProvider
    private let logger = Logger(subsystem: "com.privycs.vpn.tunnel", category: "AWGBridge")

#if canImport(WireGuardKit)
    private var adapter: WireGuardAdapter?
#endif
    private var localAddress = ""
    private var serverEndpoint = ""

    public init(provider: NEPacketTunnelProvider) {
        self.provider = provider
    }

    public func start(providerConfig: [String: Any]) async throws {
#if canImport(WireGuardKit)
        guard let raw = providerConfig["config_content"] as? String, !raw.isEmpty else {
            throw TunnelError.missingProviderConfig
        }
        // The amnezia fork's parser keeps the AWG obfuscation keys; a
        // config without them parses as plain WireGuard.
        guard var tunnelConfig = try? TunnelConfiguration(fromWgQuickConfig: raw, called: "privycs-awg") else {
            throw TunnelError.nativeFault("AmneziaWG config parse failed")
        }
        applyDNSOverride(providerConfig, to: &tunnelConfig)
        self.localAddress = tunnelConfig.interface.addresses
            .map { "\($0.address)" }.joined(separator: ", ")
        self.serverEndpoint = tunnelConfig.peers.first?.endpoint.map { "\($0)" } ?? ""

        let adapter = WireGuardAdapter(with: provider) { [weak self] _, message in
            self?.logger.info("AWG: \(message, privacy: .public)")
        }
        self.adapter = adapter
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            adapter.start(tunnelConfiguration: tunnelConfig) { error in
                if let error {
                    cont.resume(throwing: TunnelError.nativeFault("AmneziaWG start: \(error)"))
                } else {
                    cont.resume(returning: ())
                }
            }
        }
#else
        throw TunnelError.bridgeNotImplemented(.amneziawg)
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

    public func currentStats() async -> BridgeStats {
#if canImport(WireGuardKit)
        guard let adapter else {
            return BridgeStats(localAddress: localAddress, serverEndpoint: serverEndpoint)
        }
        let uapi: String? = await withCheckedContinuation { cont in
            adapter.getRuntimeConfiguration { cont.resume(returning: $0) }
        }
        var rx: Int64 = 0, tx: Int64 = 0
        if let uapi {
            for line in uapi.split(separator: "\n") {
                if line.hasPrefix("rx_bytes=") {
                    rx += Int64(line.dropFirst("rx_bytes=".count)) ?? 0
                } else if line.hasPrefix("tx_bytes=") {
                    tx += Int64(line.dropFirst("tx_bytes=".count)) ?? 0
                }
            }
        }
        return BridgeStats(rx: rx, tx: tx, localAddress: localAddress, serverEndpoint: serverEndpoint)
#else
        return BridgeStats(localAddress: localAddress, serverEndpoint: serverEndpoint)
#endif
    }
}
