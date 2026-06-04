import Foundation
import NetworkExtension
import PrivycsCore
import os

// OpenVPNAdapter (ss-abramchuk / OpenVPN3) builds for iOS + macOS only — it
// does NOT support tvOS. The tvOS tunnel target therefore does not link
// OpenVPNAdapter, so `canImport(OpenVPNAdapter)` is false there and this
// ENTIRE file compiles out (the PacketTunnelProvider's `.openvpn` dispatch is
// likewise guarded). On iOS the tunnel links OpenVPNAdapter, so the file is
// present and iOS behavior is unchanged.
#if canImport(OpenVPNAdapter)
import OpenVPNAdapter

/// NEPacketTunnelFlow has the same two methods as the Objective-C
/// protocol `OpenVPNAdapterPacketFlow` (read/write packets) — declaring
/// the conformance here lets the adapter consume `provider.packetFlow`
/// directly without a wrapper. This is the canonical hookup from the
/// OpenVPNAdapter README.
///
/// Both types live in external modules (NetworkExtension + OpenVPNAdapter),
/// so this is a retroactive conformance. Swift 6 emits an error without
/// `@retroactive`. Single-app monopoly is fine here — no library will
/// ship a conflicting conformance.
extension NEPacketTunnelFlow: @retroactive OpenVPNAdapterPacketFlow {}

/// OpenVPN packet tunnel bridge via OpenVPNAdapter (Apache 2 via
/// OpenVPN3). Vor dem Übergeben an OVPN3 läuft der Config-Inhalt
/// durch `OVPNCompatPreprocessor` — strippt unsupported directives,
/// normalisiert Legacy-Syntax.
public final class OpenVPNBridge: NSObject, TunnelProtocolBridge, @unchecked Sendable {
    private unowned let provider: NEPacketTunnelProvider
    private let logger = Logger(subsystem: "com.privycs.vpn.tunnel", category: "OVPNBridge")
    private var connectContinuation: CheckedContinuation<Void, Error>?

    private var adapter: OpenVPNAdapter?
    private var packetFlow: NEPacketTunnelFlow?

    public init(provider: NEPacketTunnelProvider) {
        self.provider = provider
        super.init()
    }

    public func start(providerConfig: [String: Any]) async throws {
        guard let rawIn = providerConfig["config_content"] as? String, !rawIn.isEmpty else {
            throw TunnelError.missingProviderConfig
        }
        // IPv6 leak killswitch — append `route-ipv6 ::/0` + `redirect-gateway
        // ipv6` so v6 routes into the tunnel. Gated by the user's Kill Switch
        // setting (default ON if absent).
        let killSwitch = providerConfig["killSwitch"] as? Bool ?? true
        var raw: String
        if killSwitch {
            let v6 = IPv6KillswitchInjector.inject(rawIn, protocol: .openvpn)
            if v6.applied { PrivycsLog.log("OVPN: ipv6-killswitch appended route-ipv6 ::/0") }
            raw = v6.patched
        } else {
            PrivycsLog.log("OVPN: ipv6-killswitch disabled (kill switch off)")
            raw = rawIn
        }
        // DNS override (3-tier resolved by the app, passed in providerConfig) —
        // Android applies it for ALL protocols; iOS previously did WG/AWG only.
        // Drop any server-pushed DNS and inject ours so the override wins
        // (override semantics, matching Android's setDNS on the OVPN profile).
        if let dns = providerConfig["dns_override"] as? String,
           !dns.trimmingCharacters(in: .whitespaces).isEmpty {
            let servers = dns.split(whereSeparator: { $0 == "," || $0 == " " })
                .map { $0.trimmingCharacters(in: .whitespaces) }
                .filter { !$0.isEmpty }
            if !servers.isEmpty {
                var lines = ["pull-filter ignore \"dhcp-option DNS\""]
                lines += servers.map { "dhcp-option DNS \($0)" }
                raw += "\n" + lines.joined(separator: "\n") + "\n"
                PrivycsLog.log("OVPN: DNS override applied (\(servers.joined(separator: ", ")))")
            }
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
    }

    public func stop(reason: NEProviderStopReason) async {
        logger.info("OVPN stop reason=\(reason.rawValue)")
        adapter?.disconnect()
        adapter = nil
    }
}

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
