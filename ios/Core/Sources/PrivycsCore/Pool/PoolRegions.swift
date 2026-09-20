import Foundation

/// ISO 3166-1 alpha-2 → Privycs region, a 1:1 port of Android's
/// `PoolPicker.regionForCountry` (which itself mirrors desktop's
/// `geoip.Region()`). The region STRINGS matter: they are what
/// `PoolMember.region` carries in the shared pool wire format, so a pool
/// imported on Android and rotated on iOS must bucket members identically.
/// The earlier iOS-only `ContinentLookup` ("EU"/"NA"/…) was a divergence —
/// same idea, different labels and far thinner coverage.
public enum PoolRegions {

    /// Region for a country code. Unknown / empty → "Other", exactly as on
    /// Android, so unknown members still form one stable bucket.
    public static func regionForCountry(_ cc: String) -> String {
        ccToRegion[cc.uppercased()] ?? "Other"
    }

    /// The region a member belongs to: its stored `region` when the pool
    /// file carries one (Android writes it at import), else derived from the
    /// member's country. Empty both ways → "Other".
    public static func region(of member: PoolMember) -> String {
        let stored = member.region.trimmingCharacters(in: .whitespaces)
        if !stored.isEmpty { return stored }
        let derived = regionForCountry(member.country)
        return derived
    }

    private static let ccToRegion: [String: String] = [
        // Europe
        "AD": "Europe", "AL": "Europe", "AT": "Europe", "BA": "Europe",
        "BE": "Europe", "BG": "Europe", "BY": "Europe", "CH": "Europe",
        "CZ": "Europe", "DE": "Europe", "DK": "Europe", "EE": "Europe",
        "ES": "Europe", "FI": "Europe", "FO": "Europe", "FR": "Europe",
        "GB": "Europe", "GG": "Europe", "GI": "Europe", "GR": "Europe",
        "HR": "Europe", "HU": "Europe", "IE": "Europe", "IM": "Europe",
        "IS": "Europe", "IT": "Europe", "JE": "Europe", "LI": "Europe",
        "LT": "Europe", "LU": "Europe", "LV": "Europe", "MC": "Europe",
        "MD": "Europe", "ME": "Europe", "MK": "Europe", "MT": "Europe",
        "NL": "Europe", "NO": "Europe", "PL": "Europe", "PT": "Europe",
        "RO": "Europe", "RS": "Europe", "RU": "Europe", "SE": "Europe",
        "SI": "Europe", "SK": "Europe", "SM": "Europe", "TR": "Europe",
        "UA": "Europe", "UK": "Europe", "VA": "Europe", "XK": "Europe",

        // North America
        "CA": "North America", "MX": "North America", "US": "North America",

        // Asia-Pacific
        "AF": "Asia-Pacific", "AM": "Asia-Pacific", "AS": "Asia-Pacific", "AU": "Asia-Pacific",
        "AZ": "Asia-Pacific", "BD": "Asia-Pacific", "BN": "Asia-Pacific", "BT": "Asia-Pacific",
        "CK": "Asia-Pacific", "CN": "Asia-Pacific", "FJ": "Asia-Pacific", "FM": "Asia-Pacific",
        "GE": "Asia-Pacific", "GU": "Asia-Pacific", "HK": "Asia-Pacific", "ID": "Asia-Pacific",
        "IN": "Asia-Pacific", "JP": "Asia-Pacific", "KG": "Asia-Pacific", "KH": "Asia-Pacific",
        "KI": "Asia-Pacific", "KP": "Asia-Pacific", "KR": "Asia-Pacific", "KZ": "Asia-Pacific",
        "LA": "Asia-Pacific", "LK": "Asia-Pacific", "MH": "Asia-Pacific", "MM": "Asia-Pacific",
        "MN": "Asia-Pacific", "MO": "Asia-Pacific", "MP": "Asia-Pacific", "MV": "Asia-Pacific",
        "MY": "Asia-Pacific", "NC": "Asia-Pacific", "NP": "Asia-Pacific", "NR": "Asia-Pacific",
        "NU": "Asia-Pacific", "NZ": "Asia-Pacific", "PF": "Asia-Pacific", "PG": "Asia-Pacific",
        "PH": "Asia-Pacific", "PK": "Asia-Pacific", "PW": "Asia-Pacific", "SB": "Asia-Pacific",
        "SG": "Asia-Pacific", "TH": "Asia-Pacific", "TJ": "Asia-Pacific", "TL": "Asia-Pacific",
        "TM": "Asia-Pacific", "TO": "Asia-Pacific", "TV": "Asia-Pacific", "TW": "Asia-Pacific",
        "UM": "Asia-Pacific", "UZ": "Asia-Pacific", "VN": "Asia-Pacific", "VU": "Asia-Pacific",
        "WS": "Asia-Pacific",

        // South America
        "AR": "South America", "BO": "South America", "BR": "South America", "CL": "South America",
        "CO": "South America", "EC": "South America", "FK": "South America", "GF": "South America",
        "GY": "South America", "PE": "South America", "PY": "South America", "SR": "South America",
        "UY": "South America", "VE": "South America",

        // Africa
        "AO": "Africa", "BF": "Africa", "BI": "Africa", "BJ": "Africa",
        "BW": "Africa", "CD": "Africa", "CF": "Africa", "CG": "Africa",
        "CI": "Africa", "CM": "Africa", "CV": "Africa", "DJ": "Africa",
        "DZ": "Africa", "EG": "Africa", "EH": "Africa", "ER": "Africa",
        "ET": "Africa", "GA": "Africa", "GH": "Africa", "GM": "Africa",
        "GN": "Africa", "GQ": "Africa", "GW": "Africa", "KE": "Africa",
        "KM": "Africa", "LR": "Africa", "LS": "Africa", "LY": "Africa",
        "MA": "Africa", "MG": "Africa", "ML": "Africa", "MR": "Africa",
        "MU": "Africa", "MW": "Africa", "MZ": "Africa", "NA": "Africa",
        "NE": "Africa", "NG": "Africa", "RE": "Africa", "RW": "Africa",
        "SC": "Africa", "SD": "Africa", "SH": "Africa", "SL": "Africa",
        "SN": "Africa", "SO": "Africa", "SS": "Africa", "ST": "Africa",
        "SZ": "Africa", "TD": "Africa", "TG": "Africa", "TN": "Africa",
        "TZ": "Africa", "UG": "Africa", "YT": "Africa", "ZA": "Africa",
        "ZM": "Africa", "ZW": "Africa",

        // Middle East
        "AE": "Middle East", "BH": "Middle East", "IL": "Middle East", "IQ": "Middle East",
        "IR": "Middle East", "JO": "Middle East", "KW": "Middle East", "LB": "Middle East",
        "OM": "Middle East", "PS": "Middle East", "QA": "Middle East", "SA": "Middle East",
        "SY": "Middle East", "YE": "Middle East",

        // Central America
        "AG": "Central America", "AI": "Central America", "AW": "Central America", "BB": "Central America",
        "BL": "Central America", "BM": "Central America", "BQ": "Central America", "BS": "Central America",
        "BZ": "Central America", "CR": "Central America", "CU": "Central America", "CW": "Central America",
        "DM": "Central America", "DO": "Central America", "GD": "Central America", "GP": "Central America",
        "GT": "Central America", "HN": "Central America", "HT": "Central America", "JM": "Central America",
        "KN": "Central America", "KY": "Central America", "LC": "Central America", "MF": "Central America",
        "MQ": "Central America", "MS": "Central America", "NI": "Central America", "PA": "Central America",
        "PR": "Central America", "SV": "Central America", "SX": "Central America", "TC": "Central America",
        "TT": "Central America", "VC": "Central America", "VG": "Central America", "VI": "Central America",
    ]
}
