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
        guard let rawIn = providerConfig["config_content"] as? String, !rawIn.isEmpty else {
            throw TunnelError.missingProviderConfig
        }
        // IPv6 leak killswitch — always-on, exactly like Android's
        // PrivycsVpnService. Re-adds `::/0` to AllowedIPs when the gateway
        // sent a v4-only config, so v6 enters the tunnel instead of leaking
        // via the OS default v6 route. THIS is the fix for "AmneziaWG has
        // no IPv6": iOS previously kept the v4-only AllowedIPs verbatim.
        let v6 = IPv6KillswitchInjector.inject(rawIn, protocol: .amneziawg)
        if v6.applied { PrivycsLog.log("AWG: ipv6-killswitch injected ::/0 into AllowedIPs") }
        else { PrivycsLog.log("AWG: ipv6-killswitch skipped (\(v6.skippedReason ?? "?"))") }
        let raw = v6.patched
        // The amnezia fork's parser keeps the AWG obfuscation keys; a
        // config without them parses as plain WireGuard.
        guard var tunnelConfig = try? TunnelConfiguration(fromWgQuickConfig: raw, called: "privycs-awg") else {
            throw TunnelError.nativeFault("AmneziaWG config parse failed")
        }
        applyDNSOverride(providerConfig, to: &tunnelConfig)
        self.localAddress = tunnelConfig.interface.addresses
            .map { "\($0.address)" }.joined(separator: ", ")
        self.serverEndpoint = tunnelConfig.peers.first?.endpoint.map { "\($0)" } ?? ""

        // Diagnostic: log whether the config actually carries IPv6 — the
        // "AWG works on v4 but not v6" question is usually answered here
        // (no fd00::/AAAA address or no ::/0 in AllowedIPs = no v6 tunnel).
        let allowed = tunnelConfig.peers.flatMap { $0.allowedIPs }.map { "\($0)" }.joined(separator: ", ")
        PrivycsLog.log("AmneziaWG starting — endpoint \(serverEndpoint)")
        PrivycsLog.log("AWG addresses: \(localAddress)")
        PrivycsLog.log("AWG allowedIPs: \(allowed)")
        if !localAddress.contains(":") { PrivycsLog.log("AWG: no IPv6 interface address in config") }
        if !allowed.contains("::/0") && !allowed.contains(":") { PrivycsLog.log("AWG: no IPv6 route (::/0) in AllowedIPs") }

        let adapter = WireGuardAdapter(with: provider) { [weak self] level, message in
            self?.logger.info("AWG: \(message, privacy: .public)")
            PrivycsLog.log("AWG[\(level.rawValue)] \(message)")
        }
        self.adapter = adapter
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            adapter.start(tunnelConfiguration: tunnelConfig) { error in
                if let error {
                    PrivycsLog.log("AmneziaWG start FAILED: \(error)")
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
        var rx: Int64 = 0, tx: Int64 = 0, hs: Int64 = 0
        if let uapi {
            for line in uapi.split(separator: "\n") {
                if line.hasPrefix("rx_bytes=") {
                    rx += Int64(line.dropFirst("rx_bytes=".count)) ?? 0
                } else if line.hasPrefix("tx_bytes=") {
                    tx += Int64(line.dropFirst("tx_bytes=".count)) ?? 0
                } else if line.hasPrefix("last_handshake_time_sec=") {
                    hs = max(hs, Int64(line.dropFirst("last_handshake_time_sec=".count)) ?? 0)
                }
            }
        }
        return BridgeStats(rx: rx, tx: tx, localAddress: localAddress,
                           serverEndpoint: serverEndpoint, lastHandshakeEpoch: hs)
#else
        return BridgeStats(localAddress: localAddress, serverEndpoint: serverEndpoint)
#endif
    }
}
