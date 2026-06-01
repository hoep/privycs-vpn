import Foundation

/// Geo-Nearest member picker — Android `PoolPicker.pickGeoNearest`
/// semantics (region-based, no coordinates):
///
/// 1. Members in the user's exact country → pick a RANDOM one.
/// 2. Else members on the user's continent → pick a RANDOM one.
/// 3. Else any eligible member at random.
///
/// Random tie-break within each cohort matches Android (SecureRandom),
/// so the same user in the same country spreads load instead of always
/// hitting member[0]. (Android `PoolMember` carries no lat/lon, so the
/// earlier iOS haversine path was a divergence — removed for parity.)
public enum GeoNearestPicker {

    public static func pick(
        from members: [PoolMember],
        userCountry: String,
        userLatLon: (Double, Double)? = nil   // accepted for source compat; unused (region-based)
    ) -> PoolMember? {
        guard !members.isEmpty else { return nil }

        let uc = userCountry.uppercased()

        // 1. Same country → random within cohort.
        if !uc.isEmpty {
            let same = members.filter { $0.country.uppercased() == uc }
            if let m = same.randomElement() { return m }
        }

        // 2. Same continent → random within cohort.
        if !uc.isEmpty, let continent = ContinentLookup.continent(of: uc) {
            let cohort = members.filter { mem in
                guard !mem.country.isEmpty,
                      let c = ContinentLookup.continent(of: mem.country.uppercased())
                else { return false }
                return c == continent
            }
            if let m = cohort.randomElement() { return m }
        }

        // 3. Any eligible member at random.
        return members.randomElement()
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
