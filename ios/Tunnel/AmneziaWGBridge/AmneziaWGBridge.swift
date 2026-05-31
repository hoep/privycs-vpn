import Foundation
import NetworkExtension
import PrivycsCore
import os
#if canImport(AmneziaWG)
import AmneziaWG
#endif

/// AmneziaWG packet tunnel bridge. Verwendet amneziawg-go als
/// XCFramework via gomobile bind. Build mit
/// `Scripts/build-amneziawg-xcframework.sh`.
///
/// AmneziaWG ist WireGuard + Traffic-Obfuskation. Config-Format ist
/// das Standard wg-quick INI, PLUS zusätzliche Felder unter
/// `[Interface]` (Jc, Jmin, Jmax, S1, S2, H1, H2, H3, H4) die die
/// Obfuskations-Parameter steuern.
///
/// Phase 4-state: Bridge ist verdrahtet, XCFramework wird zum Build-
/// Zeitpunkt auf Mac/CI gebaut via gomobile. Wenn die XCFramework
/// nicht da ist (z.B. Linux-CI für Unit-Tests), canImport(AmneziaWG)
/// gate skipped die native calls.
public final class AmneziaWGBridge: TunnelProtocolBridge, @unchecked Sendable {
    private unowned let provider: NEPacketTunnelProvider
    private let logger = Logger(subsystem: "com.privycs.vpn.tunnel", category: "AWGBridge")

#if canImport(AmneziaWG)
    private var device: AmneziaAmneziaWGDevice?
    private var tunFd: Int32 = -1
#endif

    public init(provider: NEPacketTunnelProvider) {
        self.provider = provider
    }

    public func start(providerConfig: [String: Any]) async throws {
#if canImport(AmneziaWG)
        guard let raw = providerConfig["config_content"] as? String, !raw.isEmpty else {
            throw TunnelError.missingProviderConfig
        }
        let parsed = try AmneziaWGConfig.parse(raw)
        try await applyNetworkSettings(parsed)

        // packetFlow.value(forKeyPath: "socket.fileDescriptor") ist
        // der dokumentierte Weg an den raw tunFd zu kommen — siehe
        // Apple's WireGuard sample code.
        guard let fd = provider.packetFlow.value(forKeyPath: "socket.fileDescriptor") as? Int32 else {
            throw TunnelError.nativeFault("Couldn't obtain tun FD from NEPacketTunnelFlow")
        }
        tunFd = fd

        let uapi = parsed.uapiConfig
        var nsError: NSError?
        guard let dev = AmneziaNewDevice(tunFd, uapi, &nsError) else {
            let msg = nsError?.localizedDescription ?? "unknown"
            throw TunnelError.nativeFault("AmneziaWG NewDevice: \(msg)")
        }
        device = dev
        logger.info("AmneziaWG: device started, fd=\(self.tunFd)")
#else
        throw TunnelError.bridgeNotImplemented(.amneziawg)
#endif
    }

    public func stop(reason: NEProviderStopReason) async {
#if canImport(AmneziaWG)
        device?.close()
        device = nil
        if tunFd >= 0 {
            close(tunFd)
            tunFd = -1
        }
        logger.info("AmneziaWG: device stopped, reason=\(reason.rawValue)")
#endif
    }

#if canImport(AmneziaWG)
    private func applyNetworkSettings(_ config: AmneziaWGConfig) async throws {
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: config.peerEndpoint)
        if !config.ipv4Addresses.isEmpty {
            let ipv4 = NEIPv4Settings(
                addresses: config.ipv4Addresses,
                subnetMasks: config.ipv4SubnetMasks
            )
            ipv4.includedRoutes = config.includedV4Routes.map {
                NEIPv4Route(destinationAddress: $0.0, subnetMask: $0.1)
            }
            settings.ipv4Settings = ipv4
        }
        if !config.ipv6Addresses.isEmpty {
            let ipv6 = NEIPv6Settings(
                addresses: config.ipv6Addresses,
                networkPrefixLengths: config.ipv6PrefixLengths
            )
            ipv6.includedRoutes = config.includedV6Routes.map {
                NEIPv6Route(destinationAddress: $0.0, networkPrefixLength: $0.1)
            }
            settings.ipv6Settings = ipv6
        }
        if !config.dnsServers.isEmpty {
            settings.dnsSettings = NEDNSSettings(servers: config.dnsServers)
        }
        settings.mtu = config.mtu as NSNumber
        try await provider.setTunnelNetworkSettings(settings)
    }
#endif
}
