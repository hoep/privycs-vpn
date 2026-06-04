import Foundation

/// Hostname-pattern labels: city + country + flag for VPN provider
/// hostnames like "at-vie-wg-001". Field-for-field port of Android's
/// `PoolHostnameLabels` (same curated tables). Naming convention is
/// "<cc>-<city3>-<protocol>-<n>" where <city3> is an IATA airport code.
public enum PoolHostnameLabels {

    /// City full name from "<cc>-<city3>-..." (else "").
    public static func cityFromHostname(_ name: String?) -> String {
        guard let name, !name.isEmpty else { return "" }
        let parts = name.split(separator: "-", omittingEmptySubsequences: false)
        guard parts.count >= 2 else { return "" }
        return cityCodes[parts[1].lowercased()] ?? ""
    }

    /// Country full name from ISO-3166-1 alpha-2 (else ""), localized to the
    /// given locale. CLDR ships every country name in all our languages, so we
    /// prefer the OS localization (pass the in-app-language locale to honor the
    /// user's language pick) and fall back to the curated English table for any
    /// code CLDR can't resolve.
    public static func countryNameFromCode(_ cc: String?, locale: Locale = .current) -> String {
        guard let cc, !cc.isEmpty else { return "" }
        let code = cc.uppercased()
        if let localized = locale.localizedString(forRegionCode: code), !localized.isEmpty {
            return localized
        }
        return countryNames[code] ?? ""
    }

    /// Country flag emoji via Regional Indicator Symbols (else "").
    public static func flagEmoji(_ cc: String?) -> String {
        guard let cc, cc.count == 2 else { return "" }
        let upper = cc.uppercased()
        guard upper.unicodeScalars.allSatisfy({ $0.value >= 65 && $0.value <= 90 }) else { return "" }
        var out = ""
        for u in upper.unicodeScalars {
            if let scalar = UnicodeScalar(0x1F1E6 + (Int(u.value) - 65)) {
                out.unicodeScalars.append(scalar)
            }
        }
        return out
    }

    private static let cityCodes: [String: String] = [
        "vie": "Vienna", "fra": "Frankfurt", "ber": "Berlin", "muc": "Munich",
        "dus": "Düsseldorf", "ham": "Hamburg", "zrh": "Zurich", "gva": "Geneva",
        "par": "Paris", "mrs": "Marseille", "lon": "London", "mnc": "Manchester",
        "glw": "Glasgow", "mad": "Madrid", "bcn": "Barcelona", "mil": "Milan",
        "rom": "Rome", "ams": "Amsterdam", "bru": "Brussels",
        "sto": "Stockholm", "got": "Gothenburg", "mma": "Malmö", "osl": "Oslo",
        "cph": "Copenhagen", "hel": "Helsinki",
        "prg": "Prague", "war": "Warsaw", "buh": "Bucharest", "sof": "Sofia",
        "bud": "Budapest", "ath": "Athens", "lis": "Lisbon", "dub": "Dublin",
        "tll": "Tallinn", "rix": "Riga", "vno": "Vilnius", "beg": "Belgrade",
        "zag": "Zagreb", "lju": "Ljubljana", "bts": "Bratislava", "kiv": "Kyiv",
        "nyc": "New York", "chi": "Chicago", "lax": "Los Angeles", "sea": "Seattle",
        "sjc": "San Jose", "mia": "Miami", "dal": "Dallas", "den": "Denver",
        "atl": "Atlanta", "phx": "Phoenix", "bos": "Boston", "iad": "Washington",
        "slc": "Salt Lake City",
        "yyz": "Toronto", "yvr": "Vancouver", "ymq": "Montreal", "mex": "Mexico City",
        "sao": "São Paulo", "gru": "São Paulo", "eze": "Buenos Aires", "scl": "Santiago",
        "bog": "Bogotá", "lim": "Lima",
        "tok": "Tokyo", "nrt": "Tokyo", "osa": "Osaka", "sel": "Seoul", "icn": "Seoul",
        "hkg": "Hong Kong", "tpe": "Taipei",
        "sin": "Singapore", "kul": "Kuala Lumpur", "bkk": "Bangkok", "jkt": "Jakarta",
        "mnl": "Manila", "hnd": "Hanoi", "sgn": "Ho Chi Minh City",
        "bom": "Mumbai", "del": "Delhi", "blr": "Bangalore",
        "syd": "Sydney", "mel": "Melbourne", "per": "Perth", "akl": "Auckland",
        "jnb": "Johannesburg", "cpt": "Cape Town", "lag": "Lagos", "nai": "Nairobi",
        "cai": "Cairo",
        "dxb": "Dubai", "tlv": "Tel Aviv", "ist": "Istanbul",
    ]

    private static let countryNames: [String: String] = [
        "AT": "Austria", "BE": "Belgium", "BG": "Bulgaria", "HR": "Croatia",
        "CZ": "Czechia", "DK": "Denmark", "EE": "Estonia", "FI": "Finland",
        "FR": "France", "DE": "Germany", "GR": "Greece", "HU": "Hungary",
        "IE": "Ireland", "IT": "Italy", "LV": "Latvia", "LT": "Lithuania",
        "LU": "Luxembourg", "MT": "Malta", "NL": "Netherlands", "PL": "Poland",
        "PT": "Portugal", "RO": "Romania", "SK": "Slovakia", "SI": "Slovenia",
        "ES": "Spain", "SE": "Sweden", "GB": "United Kingdom", "UK": "United Kingdom",
        "CH": "Switzerland", "NO": "Norway", "IS": "Iceland", "AL": "Albania",
        "BA": "Bosnia", "MK": "North Macedonia", "ME": "Montenegro", "RS": "Serbia",
        "MD": "Moldova", "UA": "Ukraine", "BY": "Belarus", "RU": "Russia",
        "TR": "Turkey", "XK": "Kosovo", "LI": "Liechtenstein", "MC": "Monaco",
        "SM": "San Marino", "AD": "Andorra",
        "US": "United States", "CA": "Canada", "MX": "Mexico",
        "BR": "Brazil", "AR": "Argentina", "CL": "Chile", "CO": "Colombia",
        "PE": "Peru", "VE": "Venezuela", "EC": "Ecuador", "BO": "Bolivia",
        "PY": "Paraguay", "UY": "Uruguay",
        "JP": "Japan", "KR": "South Korea", "CN": "China", "TW": "Taiwan",
        "HK": "Hong Kong", "MO": "Macau", "TH": "Thailand", "VN": "Vietnam",
        "PH": "Philippines", "ID": "Indonesia", "MY": "Malaysia", "SG": "Singapore",
        "IN": "India", "PK": "Pakistan", "BD": "Bangladesh", "AU": "Australia",
        "NZ": "New Zealand",
        "ZA": "South Africa", "EG": "Egypt", "NG": "Nigeria", "KE": "Kenya",
        "MA": "Morocco", "DZ": "Algeria", "TN": "Tunisia",
        "AE": "UAE", "SA": "Saudi Arabia", "IL": "Israel", "QA": "Qatar",
        "KW": "Kuwait", "BH": "Bahrain", "OM": "Oman", "JO": "Jordan",
        "LB": "Lebanon", "IR": "Iran", "IQ": "Iraq",
        "DO": "Dominican Republic", "JM": "Jamaica", "BS": "Bahamas",
        "BB": "Barbados", "TT": "Trinidad", "CU": "Cuba", "PR": "Puerto Rico",
    ]
}
