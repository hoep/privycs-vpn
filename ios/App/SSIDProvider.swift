import Foundation
#if canImport(NetworkExtension)
import NetworkExtension
#endif
#if canImport(CoreLocation)
import CoreLocation
#endif

/// SSID-Detection helper. Apple restricts WiFi SSID Zugriff stark:
///
/// - **iOS 13+**: `NEHotspotNetwork.fetchCurrent` (gibt es nur mit
///   entitlement + Location-Permission AND eines der drei flags:
///   hat-Hotspot-config gemacht, hat NEHotspotHelper installiert,
///   ODER VPN-config installiert).
/// - **CoreLocation**: für VPN-apps der dokumentierte Pfad — wir
///   prompten zwar nach Location-Permission, nutzen aber nur die
///   SSID des aktuellen WLANs (NICHT die Koordinaten).
///
/// Wenn Permission verweigert → SSID ist leer → NetworkRules mit
/// SSIDMode.all matchen weiter, SSIDMode.only/except matchen NICHT.
/// User-experience-Gracieuse degradation.
@MainActor
final class SSIDProvider: NSObject, ObservableObject {

#if canImport(CoreLocation)
    private let locationManager = CLLocationManager()
    /// Published so the UI can warn when SSID/BSSID rules need location but it
    /// isn't granted. Updated on init + every authorization change.
    @Published private(set) var authorizationStatus: CLAuthorizationStatus = .notDetermined
#endif

    /// True only when location is granted — i.e. iOS will actually hand out the
    /// current Wi-Fi SSID, so SSID/BSSID rules can match. When false, those
    /// rules silently never match and the UI should warn.
    var isAuthorized: Bool {
#if canImport(CoreLocation)
        #if os(macOS)
        // macOS has no `.authorizedWhenInUse`; a granted Mac maps to Always.
        return authorizationStatus == .authorizedAlways
        #else
        return authorizationStatus == .authorizedWhenInUse || authorizationStatus == .authorizedAlways
        #endif
#else
        return false
#endif
    }

    override init() {
        super.init()
#if canImport(CoreLocation)
        locationManager.delegate = self
        authorizationStatus = locationManager.authorizationStatus
#endif
    }

    func requestPermissionIfNeeded() {
#if canImport(CoreLocation)
        authorizationStatus = locationManager.authorizationStatus
        if authorizationStatus == .notDetermined {
            locationManager.requestWhenInUseAuthorization()
        }
#endif
    }

    func currentSSID() async -> String {
#if canImport(NetworkExtension) && os(iOS)
        // iOS 14+ API: fetchCurrent ist async-style aber via
        // completion-handler. wrap in continuation.
        return await withCheckedContinuation { (cont: CheckedContinuation<String, Never>) in
            NEHotspotNetwork.fetchCurrent { network in
                cont.resume(returning: network?.ssid ?? "")
            }
        }
#else
        return ""
#endif
    }
}

#if canImport(CoreLocation)
extension SSIDProvider: CLLocationManagerDelegate {
    nonisolated func locationManagerDidChangeAuthorization(_ manager: CLLocationManager) {
        let status = manager.authorizationStatus
        Task { @MainActor in self.authorizationStatus = status }
    }
}
#endif
