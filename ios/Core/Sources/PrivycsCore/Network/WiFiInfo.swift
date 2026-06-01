import Foundation
#if os(iOS)
import NetworkExtension
#endif

/// Current Wi-Fi SSID + BSSID via NEHotspotNetwork. Lives in its own file
/// that imports ONLY NetworkExtension — importing NetworkExtension into
/// NetworkMonitor.swift (which uses Network's NWPath/NWInterface) makes
/// those types ambiguous, because NetworkExtension re-exports Network.
/// Requires the Access-WiFi-Information entitlement + app foreground (iOS).
enum WiFiInfo {
    #if os(iOS)
    static func current() async -> (ssid: String, bssid: String) {
        await withCheckedContinuation { (cont: CheckedContinuation<(ssid: String, bssid: String), Never>) in
            NEHotspotNetwork.fetchCurrent { net in
                cont.resume(returning: (net?.ssid ?? "", net?.bssid ?? ""))
            }
        }
    }
    #else
    static func current() async -> (ssid: String, bssid: String) { ("", "") }
    #endif
}
