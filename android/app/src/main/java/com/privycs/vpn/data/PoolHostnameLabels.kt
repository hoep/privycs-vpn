package com.privycs.vpn.data

/**
 * Hostname-pattern labels: city + country + flag for VPN provider
 * hostnames like "at-vie-wg-001" or "us-dal-wg-704".
 *
 * Port of desktop's pool_city_codes.go + ad-hoc country-name lookup.
 * The naming convention is "<cc>-<city3>-<protocol>-<n>" where
 * <city3> is typically an IATA airport code. Hand-curated list of
 * ~100 codes that actually appear across Mullvad, IVPN, Proton,
 * AzireVPN, PIA - shipping the full 17000+ IATA dump would bloat
 * the APK for zero practical benefit.
 *
 * Why this lives in `data/`: ConnectScreen + WidgetProvider both
 * read it. Putting it next to the Pool data avoids duplicating the
 * same lookup logic across multiple UI surfaces.
 */
object PoolHostnameLabels {

    /**
     * Parses "<cc>-<city3>-..." and returns the city full name, or
     * "" if the host doesn't match the pattern OR the city code is
     * not in the curated table. Mirrors desktop's
     * `cityFromHostnamePattern`.
     */
    fun cityFromHostname(name: String?): String {
        if (name.isNullOrEmpty()) return ""
        val parts = name.split("-")
        if (parts.size < 2) return ""
        return CITY_CODES[parts[1].lowercase()].orEmpty()
    }

    /**
     * Country full name from ISO-3166-1 alpha-2 code, localized to the
     * given locale (default = the active app/device locale), or "" if the
     * code is unknown. The JDK ships every country name in all our languages
     * via CLDR, so we prefer [java.util.Locale.getDisplayCountry] and fall
     * back to the curated English table only for codes CLDR can't resolve.
     */
    fun countryNameFromCode(cc: String?, locale: java.util.Locale = java.util.Locale.getDefault()): String {
        if (cc.isNullOrEmpty()) return ""
        val code = cc.uppercase()
        val display = java.util.Locale("", code).getDisplayCountry(locale)
        // getDisplayCountry echoes the code back when CLDR doesn't know it.
        if (display.isNotEmpty() && !display.equals(code, ignoreCase = true)) return display
        return COUNTRY_NAMES[code].orEmpty()
    }

    /**
     * Country flag emoji from ISO-3166-1 alpha-2 code, or "" if
     * the input is malformed. Uses the Regional Indicator Symbol
     * trick: each ASCII letter A-Z is offset to its corresponding
     * REGIONAL INDICATOR SYMBOL LETTER (U+1F1E6 + (letter - 'A')).
     * Android (API 26+) renders the pair as a flag glyph.
     *
     * No SVG assets, no Twemoji bundle - the OS font handles it.
     * Saves ~600KB of flag SVGs across the APK.
     */
    fun flagEmojiFromCode(cc: String?): String {
        if (cc.isNullOrEmpty() || cc.length != 2) return ""
        val upper = cc.uppercase()
        if (!upper.all { it in 'A'..'Z' }) return ""
        val first = 0x1F1E6 + (upper[0].code - 'A'.code)
        val second = 0x1F1E6 + (upper[1].code - 'A'.code)
        val sb = StringBuilder()
        sb.appendCodePoint(first)
        sb.appendCodePoint(second)
        return sb.toString()
    }

    private val CITY_CODES = mapOf(
        // Europe - DACH
        "vie" to "Vienna", "fra" to "Frankfurt", "ber" to "Berlin",
        "muc" to "Munich", "dus" to "Düsseldorf", "ham" to "Hamburg",
        "zrh" to "Zurich", "gva" to "Geneva",
        // Europe - West
        "par" to "Paris", "mrs" to "Marseille", "lon" to "London",
        "mnc" to "Manchester", "glw" to "Glasgow", "mad" to "Madrid",
        "bcn" to "Barcelona", "mil" to "Milan", "rom" to "Rome",
        "ams" to "Amsterdam", "bru" to "Brussels",
        // Europe - Nordic
        "sto" to "Stockholm", "got" to "Gothenburg", "mma" to "Malmö",
        "osl" to "Oslo", "cph" to "Copenhagen", "hel" to "Helsinki",
        // Europe - East
        "prg" to "Prague", "war" to "Warsaw", "buh" to "Bucharest",
        "sof" to "Sofia", "bud" to "Budapest", "ath" to "Athens",
        "lis" to "Lisbon", "dub" to "Dublin", "tll" to "Tallinn",
        "rix" to "Riga", "vno" to "Vilnius", "beg" to "Belgrade",
        "zag" to "Zagreb", "lju" to "Ljubljana", "bts" to "Bratislava",
        "kiv" to "Kyiv",
        // North America - US
        "nyc" to "New York", "chi" to "Chicago", "lax" to "Los Angeles",
        "sea" to "Seattle", "sjc" to "San Jose", "mia" to "Miami",
        "dal" to "Dallas", "den" to "Denver", "atl" to "Atlanta",
        "phx" to "Phoenix", "bos" to "Boston", "iad" to "Washington",
        "slc" to "Salt Lake City",
        // North America - Canada / Mexico
        "yyz" to "Toronto", "yvr" to "Vancouver", "ymq" to "Montreal",
        "mex" to "Mexico City",
        // South America
        "sao" to "São Paulo", "gru" to "São Paulo", "eze" to "Buenos Aires",
        "scl" to "Santiago", "bog" to "Bogotá", "lim" to "Lima",
        // Asia - East
        "tok" to "Tokyo", "nrt" to "Tokyo", "osa" to "Osaka",
        "sel" to "Seoul", "icn" to "Seoul", "hkg" to "Hong Kong",
        "tpe" to "Taipei",
        // Asia - South-East
        "sin" to "Singapore", "kul" to "Kuala Lumpur", "bkk" to "Bangkok",
        "jkt" to "Jakarta", "mnl" to "Manila", "hnd" to "Hanoi",
        "sgn" to "Ho Chi Minh City",
        // Asia - South
        "bom" to "Mumbai", "del" to "Delhi", "blr" to "Bangalore",
        // Oceania
        "syd" to "Sydney", "mel" to "Melbourne", "per" to "Perth",
        "akl" to "Auckland",
        // Africa
        "jnb" to "Johannesburg", "cpt" to "Cape Town", "lag" to "Lagos",
        "nai" to "Nairobi", "cai" to "Cairo",
        // Middle East
        "dxb" to "Dubai", "tlv" to "Tel Aviv", "ist" to "Istanbul"
    )

    private val COUNTRY_NAMES = mapOf(
        // Europe
        "AT" to "Austria", "BE" to "Belgium", "BG" to "Bulgaria",
        "HR" to "Croatia", "CZ" to "Czechia", "DK" to "Denmark",
        "EE" to "Estonia", "FI" to "Finland", "FR" to "France",
        "DE" to "Germany", "GR" to "Greece", "HU" to "Hungary",
        "IE" to "Ireland", "IT" to "Italy", "LV" to "Latvia",
        "LT" to "Lithuania", "LU" to "Luxembourg", "MT" to "Malta",
        "NL" to "Netherlands", "PL" to "Poland", "PT" to "Portugal",
        "RO" to "Romania", "SK" to "Slovakia", "SI" to "Slovenia",
        "ES" to "Spain", "SE" to "Sweden", "GB" to "United Kingdom",
        "UK" to "United Kingdom", "CH" to "Switzerland", "NO" to "Norway",
        "IS" to "Iceland", "AL" to "Albania", "BA" to "Bosnia",
        "MK" to "North Macedonia", "ME" to "Montenegro", "RS" to "Serbia",
        "MD" to "Moldova", "UA" to "Ukraine", "BY" to "Belarus",
        "RU" to "Russia", "TR" to "Turkey", "XK" to "Kosovo",
        "LI" to "Liechtenstein", "MC" to "Monaco", "SM" to "San Marino",
        "AD" to "Andorra",
        // North America
        "US" to "United States", "CA" to "Canada", "MX" to "Mexico",
        // South America
        "BR" to "Brazil", "AR" to "Argentina", "CL" to "Chile",
        "CO" to "Colombia", "PE" to "Peru", "VE" to "Venezuela",
        "EC" to "Ecuador", "BO" to "Bolivia", "PY" to "Paraguay",
        "UY" to "Uruguay",
        // Asia-Pacific
        "JP" to "Japan", "KR" to "South Korea", "CN" to "China",
        "TW" to "Taiwan", "HK" to "Hong Kong", "MO" to "Macau",
        "TH" to "Thailand", "VN" to "Vietnam", "PH" to "Philippines",
        "ID" to "Indonesia", "MY" to "Malaysia", "SG" to "Singapore",
        "IN" to "India", "PK" to "Pakistan", "BD" to "Bangladesh",
        "AU" to "Australia", "NZ" to "New Zealand",
        // Africa
        "ZA" to "South Africa", "EG" to "Egypt", "NG" to "Nigeria",
        "KE" to "Kenya", "MA" to "Morocco", "DZ" to "Algeria",
        "TN" to "Tunisia",
        // Middle East
        "AE" to "UAE", "SA" to "Saudi Arabia", "IL" to "Israel",
        "QA" to "Qatar", "KW" to "Kuwait", "BH" to "Bahrain",
        "OM" to "Oman", "JO" to "Jordan", "LB" to "Lebanon",
        "IR" to "Iran", "IQ" to "Iraq",
        // Caribbean
        "DO" to "Dominican Republic", "JM" to "Jamaica", "BS" to "Bahamas",
        "BB" to "Barbados", "TT" to "Trinidad", "CU" to "Cuba", "PR" to "Puerto Rico"
    )
}
