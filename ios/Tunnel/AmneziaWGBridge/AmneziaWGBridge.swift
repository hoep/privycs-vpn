import Foundation
import NetworkExtension
import PrivycsCore
import os

/// AmneziaWG packet tunnel bridge. Verwendet amneziawg-go als
/// XCFramework via gomobile bind (siehe
/// `Scripts/build-amneziawg-xcframework.sh`).
///
/// Phase 2-stub — Bridge interface fest, native call-out kommt
/// in Phase 4 wenn das XCFramework gebaut ist. Bis dahin wirft
/// `TunnelError.bridgeNotImplemented`.
public final class AmneziaWGBridge: TunnelProtocolBridge, @unchecked Sendable {
    private unowned let provider: NEPacketTunnelProvider
    private let logger = Logger(subsystem: "com.privycs.vpn.tunnel", category: "AWGBridge")

    public init(provider: NEPacketTunnelProvider) {
        self.provider = provider
    }

    public func start(providerConfig: [String: Any]) async throws {
        // TODO Phase 4: gomobile XCFramework integration
        // 1. parse AWG-config (analog WireGuard + obfuscation params S1/S2/H1-H4/Jc/Jmin/Jmax)
        // 2. call into amneziawg-go via `import AmneziaWG` (XCFramework module)
        // 3. configure NEPacketTunnelNetworkSettings
        // 4. start packet pump loop
        throw TunnelError.bridgeNotImplemented(.amneziawg)
    }

    public func stop(reason: NEProviderStopReason) async {
        logger.info("AWG stop (no-op, bridge not implemented)")
    }
}
