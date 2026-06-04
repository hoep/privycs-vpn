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

    /// Protocols the widget can reconfigure + restart in-place (packet-tunnel
    /// based). IPSec is excluded — it runs through `NEVPNManager`/IKEv2 with
    /// certificate parsing, which the widget can't reproduce, so the widget
    /// falls back to opening the app for an IPSec switch.
    public static func isInPlaceSwitchable(_ protocolRaw: String) -> Bool {
        protocolRaw == "wireguard" || protocolRaw == "amneziawg" || protocolRaw == "openvpn"
    }
}
