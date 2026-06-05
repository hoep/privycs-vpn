import Foundation
import PrivycsCore
import Engine // gomobile xcframework (engine/ffi), prefix=Pvcs

/// Shadow-mode bridge to the cross-platform Smart Decision Engine — the same Go
/// core (engine/ffi) the desktop and Android run, packaged here as the gomobile
/// `Engine.xcframework`.
///
/// Mirrors desktop/engine_bridge.go + Android EngineShadow.kt: it OBSERVES the
/// real connection lifecycle (connect/disconnect, via the AppState status
/// stream) and the tunnel-health transitions (TunnelHealthService), runs the
/// engine's pure FSM, and exposes the explainable decision log for the
/// EngineDecisionsView — while driving NOTHING (the engine's spokes are no-ops
/// on the Go side). Zero behaviour change; flipping to active selection is a
/// later slice, exactly as on the other platforms.
///
/// Every reference to a gomobile-generated symbol lives in this one file so a
/// generated-binding name mismatch is a single-file fix. Expected Swift surface
/// (prefix=Pvcs): `PvcsFfiNewSession(_:) -> PvcsFfiSession?`, and on PvcsFfiSession:
/// `observeConnect()`, `observeDisconnect()`, `observeHealth(_:)`,
/// `pollDecisions() -> String`, `close()`.
@MainActor
final class EngineShadow {
    private var session: PvcsFfiSession?
    private var orderJSON = ""

    /// Build or refresh the session from the protocol-failover order. No-op
    /// when the order is unchanged. The candidate tokens are the VpnProtocol
    /// rawValues ("amneziawg"/"wireguard"/"openvpn"/"ipsec"), matching the
    /// desktop/Android shadow stores.
    func ensure(order: [VpnProtocol]) {
        let js = Self.orderToJSON(order)
        if session != nil && js == orderJSON { return }
        session?.close()
        session = PvcsFfiNewSession(js)
        orderJSON = js
    }

    func observeConnect(_ proto: String, country: String, awgAvailable: Bool) {
        // gomobile labels every arg after the first: observeConnect(_:country:awgAvailable:)
        session?.observeConnect(proto, country: country, awgAvailable: awgAvailable)
    }

    func observeDisconnect() { session?.observeDisconnect() }

    func observeHealth(_ health: TunnelHealthPill.Health?) {
        guard let health else { return } // nil = inactive → ignore
        let token: String
        switch health {
        case .healthy: token = "healthy"
        case .degraded: token = "degraded"
        case .recovering: token = "recovering"
        }
        session?.observeHealth(token)
    }

    /// Recent decisions (newest last); empty on any error.
    func decisions() -> [EngineDecision] {
        guard let raw = session?.pollDecisions(),
              let data = raw.data(using: .utf8) else { return [] }
        return (try? JSONDecoder().decode([EngineDecision].self, from: data)) ?? []
    }

    private static func orderToJSON(_ order: [VpnProtocol]) -> String {
        let src = order.isEmpty
            ? [VpnProtocol.amneziawg, .wireguard, .openvpn, .ipsec]
            : order
        let tokens = src.map { "\"\($0.rawValue)\"" }
        return "[" + tokens.joined(separator: ",") + "]"
    }
}

/// Wire shape of one engine decision — identical JSON to the desktop
/// EngineDecisionDTO and Android EngineDecision so all platforms share one
/// render contract. `key` is the stable i18n key (e.g. "decision.connecting");
/// never pre-translated text. Tolerant decoding so a future field can't break
/// older payloads.
struct EngineDecision: Codable, Identifiable {
    let at: String
    let from: String
    let to: String
    let rule: String
    let active: String
    let chosen: String
    let key: String
    let args: [String]
    let reason: String
    let reasonArgs: [String]

    var id: String { at + "|" + key + "|" + to }

    private enum CodingKeys: String, CodingKey {
        case at, from, to, rule, active, chosen, key, args, reason, reasonArgs
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        at = try c.decodeIfPresent(String.self, forKey: .at) ?? ""
        from = try c.decodeIfPresent(String.self, forKey: .from) ?? ""
        to = try c.decodeIfPresent(String.self, forKey: .to) ?? ""
        rule = try c.decodeIfPresent(String.self, forKey: .rule) ?? ""
        active = try c.decodeIfPresent(String.self, forKey: .active) ?? ""
        chosen = try c.decodeIfPresent(String.self, forKey: .chosen) ?? ""
        key = try c.decodeIfPresent(String.self, forKey: .key) ?? ""
        args = try c.decodeIfPresent([String].self, forKey: .args) ?? []
        reason = try c.decodeIfPresent(String.self, forKey: .reason) ?? ""
        reasonArgs = try c.decodeIfPresent([String].self, forKey: .reasonArgs) ?? []
    }
}
