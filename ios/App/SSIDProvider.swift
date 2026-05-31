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
#endif

    override init() {
        super.init()
#if canImport(CoreLocation)
        locationManager.delegate = self
#endif
    }

    func requestPermissionIfNeeded() {
#if canImport(CoreLocation)
        switch locationManager.authorizationStatus {
        case .notDetermined:
            locationManager.requestWhenInUseAuthorization()
        default:
            break
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
        // Nothing to do — the next SSID-fetch attempt will see the
        // updated permission state via fetchCurrent's callback.
    }
}
#endif
