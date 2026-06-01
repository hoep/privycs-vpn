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
    private var localAddress: String = ""
    private var serverEndpoint: String = ""

    public init(provider: NEPacketTunnelProvider) {
        self.provider = provider
    }

    public func start(providerConfig: [String: Any]) async throws {
#if canImport(WireGuardKit)
        guard let rawIn = providerConfig["config_content"] as? String, !rawIn.isEmpty else {
            throw TunnelError.missingProviderConfig
        }
        // IPv6 leak killswitch — always-on, matching Android. Re-adds `::/0`
        // to AllowedIPs for v4-only gateway configs so v6 routes into the
        // tunnel instead of leaking via the OS default v6 route.
        let v6 = IPv6KillswitchInjector.inject(rawIn, protocol: .wireguard)
        if v6.applied { PrivycsLog.log("WG: ipv6-killswitch injected ::/0 into AllowedIPs") }
        let raw = v6.patched
        // Parse via WireGuardKit's standard ini-config parser.
        guard var tunnelConfig = try? TunnelConfiguration(fromWgQuickConfig: raw, called: "privycs") else {
            throw TunnelError.nativeFault("WireGuard config parse failed")
        }
        // 3-tier DNS override (pool→connection→global) applied by the app.
        applyDNSOverride(providerConfig, to: &tunnelConfig)
        // Stash interface address + peer endpoint for the stats channel.
        self.localAddress = tunnelConfig.interface.addresses
            .map { "\($0.address)" }.joined(separator: ", ")
        self.serverEndpoint = tunnelConfig.peers.first?.endpoint.map { "\($0)" } ?? ""

        let adapter = WireGuardAdapter(with: provider) { [weak self] level, message in
            // Route the WireGuard backend's verbose log (handshakes,
            // endpoint resolution, errors) into BOTH os_log AND the
            // shared file the in-app Logs viewer reads.
            self?.logger.info("WG: \(message, privacy: .public)")
            PrivycsLog.log("WG[\(level.rawValue)] \(message)")
        }
        self.adapter = adapter
        PrivycsLog.log("WireGuard starting — endpoint \(serverEndpoint), addr \(localAddress)")
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            adapter.start(tunnelConfiguration: tunnelConfig) { error in
                if let error {
                    PrivycsLog.log("WireGuard start FAILED: \(error)")
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

    public func currentStats() async -> BridgeStats {
#if canImport(WireGuardKit)
        guard let adapter else {
            return BridgeStats(localAddress: localAddress, serverEndpoint: serverEndpoint)
        }
        // getRuntimeConfiguration returns the UAPI dump; sum rx_bytes /
        // tx_bytes across all peers.
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

#if canImport(WireGuardKit)
/// Apply the app-resolved 3-tier DNS override (pool→connection→global)
/// onto a parsed WireGuard/AmneziaWG config — WireGuardAdapter then
/// pushes these as the tunnel's NEDNSSettings. Shared by both bridges.
/// No-op when the override is empty (keeps the config's own DNS).
func applyDNSOverride(_ providerConfig: [String: Any], to config: inout TunnelConfiguration) {
    guard let dns = providerConfig["dns_override"] as? String, !dns.isEmpty else { return }
    let servers = dns
        .split(whereSeparator: { $0 == "," || $0 == " " || $0 == "\n" })
        .map { $0.trimmingCharacters(in: .whitespaces) }
        .filter { !$0.isEmpty }
        .compactMap { DNSServer(from: $0) }
    if !servers.isEmpty { config.interface.dns = servers }
}
#endif
