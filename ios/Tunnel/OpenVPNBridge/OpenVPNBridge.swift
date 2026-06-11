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
    /// Serialises continuation access across the adapter-delegate callbacks and
    /// the timeout watchdog (this type is `@unchecked Sendable`).
    private let contLock = NSLock()

    private var adapter: OpenVPNAdapter?
    private var packetFlow: NEPacketTunnelFlow?

    /// Captured from the adapter's tunnel network settings at connect, so
    /// currentStats() can surface them on the Connect screen (server endpoint +
    /// assigned VPN IP) like the WG/AWG bridges do.
    private var localAddress = ""
    private var serverEndpoint = ""

    /// Seconds to wait for a terminal connect event before failing the start.
    /// Without this the CheckedContinuation only resumes on connected /
    /// disconnected / error — if OpenVPN3 never emits any of those (e.g. it
    /// silently stalls in IKE/TLS negotiation) the extension wedges forever.
    private let connectTimeout: TimeInterval = 20

    /// Resume the connect continuation exactly once. The delegate callbacks and
    /// the timeout watchdog can race; whoever wins resumes, the rest no-op.
    private func resumeConnect(_ result: Result<Void, Error>) {
        contLock.lock()
        let cont = connectContinuation
        connectContinuation = nil
        contLock.unlock()
        guard let cont else { return }
        switch result {
        case .success: cont.resume(returning: ())
        case .failure(let e): cont.resume(throwing: e)
        }
    }

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

        // Watchdog: if no terminal event (connected / disconnected / error)
        // arrives within `connectTimeout`, fail the start so the extension can
        // report failure instead of wedging forever. Fires through the same
        // single-resume path as the delegate callbacks (whoever wins, wins).
        let watchdog = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64((self?.connectTimeout ?? 20) * 1_000_000_000))
            guard !Task.isCancelled else { return }
            self?.logger.error("OVPN connect timed out — no terminal event")
            self?.resumeConnect(.failure(TunnelError.nativeFault("OpenVPN connect timed out")))
        }
        defer { watchdog.cancel() }

        // Wait for the delegate to fire its connected callback. The
        // bridge resumes the continuation in the `.connected` event OR with
        // throw on error / disconnect-before-connect / the timeout above.
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            contLock.lock()
            connectContinuation = cont
            contLock.unlock()
            adapter.connect(using: provider.packetFlow)
        }
    }

    public func stop(reason: NEProviderStopReason) async {
        logger.info("OVPN stop reason=\(reason.rawValue)")
        adapter?.disconnect()
        adapter = nil
    }

    /// Live traffic + endpoint/VPN-IP for the Connect screen + widget. rx/tx
    /// from OpenVPNAdapter.transportStatistics (bytesIn/bytesOut); endpoint +
    /// localAddress captured when the adapter configured the tunnel settings.
    public func currentStats() async -> BridgeStats {
        guard let adapter else {
            return BridgeStats(localAddress: localAddress, serverEndpoint: serverEndpoint)
        }
        let s = adapter.transportStatistics
        return BridgeStats(rx: Int64(s.bytesIn), tx: Int64(s.bytesOut),
                           localAddress: localAddress, serverEndpoint: serverEndpoint)
    }
}

extension OpenVPNBridge: OpenVPNAdapterDelegate {
    public func openVPNAdapter(
        _ adapter: OpenVPNAdapter,
        configureTunnelWithNetworkSettings settings: NEPacketTunnelNetworkSettings?,
        completionHandler: @escaping (Error?) -> Void
    ) {
        if let settings {
            // Capture endpoint + assigned VPN IP(s) for the Connect-screen detail
            // rows (OpenVPN previously reported neither — only WG/AWG did).
            serverEndpoint = settings.tunnelRemoteAddress
            var addrs: [String] = []
            if let v4 = settings.ipv4Settings?.addresses { addrs += v4 }
            if let v6 = settings.ipv6Settings?.addresses { addrs += v6 }
            localAddress = addrs.joined(separator: ", ")
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
            resumeConnect(.success(()))
        case .disconnected:
            resumeConnect(.failure(TunnelError.nativeFault("OpenVPN disconnected before connect")))
        default:
            break
        }
    }

    public func openVPNAdapter(_ adapter: OpenVPNAdapter, handleError error: Error) {
        logger.error("OVPN error: \(error.localizedDescription, privacy: .public)")
        resumeConnect(.failure(error))
    }

    public func openVPNAdapter(_ adapter: OpenVPNAdapter, handleLogMessage logMessage: String) {
        logger.debug("OVPN log: \(logMessage, privacy: .public)")
    }
}
#endif
