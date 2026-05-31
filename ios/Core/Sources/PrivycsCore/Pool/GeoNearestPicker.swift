import Foundation

/// Geo-Nearest Member-Picker. Heuristik (gleiche Reihenfolge wie
/// Android `GeoNearest`):
///
/// 1. Member im gleichen Country wie User → pick erstes oder
///    distance-sortiert wenn LatLon verfügbar.
/// 2. Sonst Member auf gleichem Kontinent (via ISO-Continent-Lookup).
/// 3. Sonst kürzeste Great-Circle-Distance wenn LatLon verfügbar.
/// 4. Sonst alphabetisch erstes Country-Member (Stable Fallback).
public enum GeoNearestPicker {

    public static func pick(
        from members: [PoolMember],
        userCountry: String,
        userLatLon: (Double, Double)?
    ) -> PoolMember? {
        guard !members.isEmpty else { return nil }

        // 1. Same country
        let uc = userCountry.uppercased()
        if !uc.isEmpty {
            let same = members.filter { $0.country.uppercased() == uc }
            if !same.isEmpty {
                if let userLatLon, let m = closest(in: same, to: userLatLon) {
                    return m
                }
                return same[0]
            }
        }

        // 2. Same continent
        if !uc.isEmpty, let continent = ContinentLookup.continent(of: uc) {
            let sameContinent = members.filter { mem in
                guard !mem.country.isEmpty,
                      let c = ContinentLookup.continent(of: mem.country.uppercased())
                else { return false }
                return c == continent
            }
            if !sameContinent.isEmpty {
                if let userLatLon, let m = closest(in: sameContinent, to: userLatLon) {
                    return m
                }
                return sameContinent[0]
            }
        }

        // 3. Globally closest by distance
        if let userLatLon, let m = closest(in: members, to: userLatLon) {
            return m
        }

        // 4. Alphabetical fallback (stable, deterministic)
        return members.sorted { $0.country.localizedCompare($1.country) == .orderedAscending }.first
    }

    /// Great-Circle-Distanz (Haversine). Members ohne valid
    /// LatLon werden ausgeschlossen.
    private static func closest(
        in members: [PoolMember],
        to userLatLon: (Double, Double)
    ) -> PoolMember? {
        let withCoords = members.filter { !$0.latitude.isNaN && !$0.longitude.isNaN }
        guard !withCoords.isEmpty else { return nil }
        return withCoords.min { a, b in
            let da = haversineKm(
                lat1: userLatLon.0, lon1: userLatLon.1,
                lat2: a.latitude, lon2: a.longitude
            )
            let db = haversineKm(
                lat1: userLatLon.0, lon1: userLatLon.1,
                lat2: b.latitude, lon2: b.longitude
            )
            return da < db
        }
    }

    private static func haversineKm(
        lat1: Double, lon1: Double,
        lat2: Double, lon2: Double
    ) -> Double {
        let R = 6371.0
        let dLat = (lat2 - lat1) * .pi / 180
        let dLon = (lon2 - lon1) * .pi / 180
        let a = sin(dLat / 2) * sin(dLat / 2)
            + cos(lat1 * .pi / 180) * cos(lat2 * .pi / 180)
            * sin(dLon / 2) * sin(dLon / 2)
        let c = 2 * atan2(sqrt(a), sqrt(1 - a))
        return R * c
    }
}

/// Minimaler ISO 3166-1 Alpha-2 → Kontinent-Lookup. Genug Coverage
/// für die typischen VPN-Provider-Server-Standorte (~95% Treffer).
/// Bei Bedarf erweitern, aber bewusst klein gehalten — nicht
/// embedded MMDB.
enum ContinentLookup {
    static func continent(of countryCode: String) -> String? {
        return ccToContinent[countryCode]
    }

    private static let ccToContinent: [String: String] = [
        // Europe
        "AT": "EU", "BE": "EU", "BG": "EU", "CH": "EU", "CY": "EU", "CZ": "EU",
        "DE": "EU", "DK": "EU", "EE": "EU", "ES": "EU", "FI": "EU", "FR": "EU",
        "GB": "EU", "GR": "EU", "HR": "EU", "HU": "EU", "IE": "EU", "IS": "EU",
        "IT": "EU", "LI": "EU", "LT": "EU", "LU": "EU", "LV": "EU", "MD": "EU",
        "MT": "EU", "NL": "EU", "NO": "EU", "PL": "EU", "PT": "EU", "RO": "EU",
        "RS": "EU", "SE": "EU", "SI": "EU", "SK": "EU", "UA": "EU",

        // North America
        "US": "NA", "CA": "NA", "MX": "NA", "PA": "NA", "CR": "NA",

        // South America
        "AR": "SA", "BR": "SA", "CL": "SA", "CO": "SA", "PE": "SA", "VE": "SA",
        "UY": "SA",

        // Asia
        "AE": "AS", "BD": "AS", "CN": "AS", "HK": "AS", "ID": "AS", "IL": "AS",
        "IN": "AS", "JP": "AS", "KR": "AS", "KH": "AS", "MY": "AS", "PH": "AS",
        "SG": "AS", "TH": "AS", "TR": "AS", "TW": "AS", "VN": "AS",

        // Africa
        "EG": "AF", "GH": "AF", "KE": "AF", "MA": "AF", "NG": "AF", "ZA": "AF",

        // Oceania
        "AU": "OC", "NZ": "OC", "FJ": "OC",

        // Antarctica is intentionally out — no VPN servers there.
    ]
}
