import Foundation

/// Single source of truth for the `NETunnelProviderProtocol.providerConfiguration`
/// dictionary the packet-tunnel extension reads. Both the main app
/// (`VPNTunnelManager`) and the home-screen widget's in-place protocol-switch
/// intent build the dict through this, so the key contract can never drift
/// between the two call sites.
///
/// Pure Foundation (no NetworkExtension import) so PrivycsCore keeps building
/// + unit-testing on Linux; the caller wraps the returned dict in an
/// `NETunnelProviderProtocol` on Apple platforms.
public enum TunnelProviderConfig {
    /// The packet-tunnel extension's bundle identifier (iOS + the tvOS port).
    public static let bundleIdentifier = "com.privycs.vpn.tunnel"

    /// Build the provider-configuration dictionary. `killSwitch` is bridged as
    /// an NSNumber-compatible Bool; all others are Strings.
    public static func make(
        protocolRaw: String,
        configContent: String,
        connectionId: String,
        configId: String,
        dnsOverride: String,
        killSwitch: Bool
    ) -> [String: Any] {
        [
            "protocol": protocolRaw,
            "config_content": configContent,
            "connection_id": connectionId,
            "config_id": configId,
            "dns_override": dnsOverride,
            "killSwitch": killSwitch,
        ]
    }

    /// Protocols the widget can switch to in place. Since v1.1.5.8 the app
    /// pre-creates a READY-TO-START manager per protocol (PTP: one
    /// NETunnelProviderManager each; IPSec: the shared NEVPNManager slot loaded
    /// with this connection's profile), so the widget switches by stop+start —
    /// no reconfigure (which the widget extension can't do reliably). All four
    /// protocols are now in-place switchable.
    public static func isInPlaceSwitchable(_ protocolRaw: String) -> Bool {
        protocolRaw == "wireguard" || protocolRaw == "amneziawg"
            || protocolRaw == "openvpn" || protocolRaw == "ipsec"
    }

    /// Human label for a protocol (used in the per-protocol manager name).
    public static func protocolLabel(_ raw: String) -> String {
        switch raw {
        case "wireguard": return "WireGuard"
        case "amneziawg": return "AmneziaWG"
        case "openvpn":   return "OpenVPN"
        case "ipsec":     return "IPSec"
        default:          return raw
        }
    }

    /// localizedDescription for the per-protocol PTP manager of a connection.
    /// The app creates ONE manager per (connection, PTP-protocol) so the widget
    /// can switch by stop+start instead of reconfiguring. App + widget MUST agree
    /// on this exact string. IPSec is NOT here — it uses the shared NEVPNManager
    /// slot, identified by the bare connection name.
    public static func ptpManagerName(connectionName: String, protocolRaw: String) -> String {
        "\(connectionName) · \(protocolLabel(protocolRaw))"
    }
}
