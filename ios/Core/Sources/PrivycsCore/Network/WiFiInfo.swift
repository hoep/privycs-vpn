import Foundation
#if os(iOS)
import NetworkExtension
#elseif os(macOS)
import CoreWLAN
#endif

/// Current Wi-Fi SSID + BSSID. iOS uses NEHotspotNetwork (Access-WiFi-Information
/// entitlement + foreground); macOS uses CoreWLAN (Location-gated since macOS 14,
/// entitlement com.apple.security.personal-information.location + the app's
/// Location prompt). Lives in its own file that imports ONLY the Wi-Fi framework —
/// importing NetworkExtension into NetworkMonitor.swift (which uses Network's
/// NWPath/NWInterface) makes those types ambiguous, because NetworkExtension
/// re-exports Network.
enum WiFiInfo {
    #if os(iOS)
    static func current() async -> (ssid: String, bssid: String) {
        await withCheckedContinuation { (cont: CheckedContinuation<(ssid: String, bssid: String), Never>) in
            NEHotspotNetwork.fetchCurrent { net in
                cont.resume(returning: (net?.ssid ?? "", net?.bssid ?? ""))
            }
        }
    }
    #elseif os(macOS)
    static func current() async -> (ssid: String, bssid: String) {
        // CoreWLAN is synchronous. Returns ("","") on an ethernet-only Mac
        // (no Wi-Fi interface) or until Location is granted (macOS 14+).
        guard let iface = CWWiFiClient.shared().interface() else { return ("", "") }
        return (iface.ssid() ?? "", iface.bssid() ?? "")
    }
    #else
    static func current() async -> (ssid: String, bssid: String) { ("", "") }
    #endif
}
