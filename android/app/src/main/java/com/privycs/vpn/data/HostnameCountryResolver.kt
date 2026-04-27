package com.privycs.vpn.data

/**
 * Hostname-pattern-based country resolver.
 *
 * Why this exists: Android does NOT bundle a GeoIP database
 * (would add ~5MB to the APK). Instead we exploit a near-universal
 * convention among commercial VPN providers: their hostnames
 * encode the country in the first 2 characters as an ISO 3166-1
 * alpha-2 code. Examples (anonymised): "xx-yyy-wg-001" where
 * "xx" is the country, "yyy" the city.
 *
 * Coverage: this works for ~95% of provider hostnames. Custom-
 * named imports (e.g., user-renamed configs, corporate VPNs) get
 * Country="" and degrade to Random within the unfiltered set —
 * same fallback semantic as desktop without MMDB.
 *
 * Validation: not every "xx-..." prefix is a real country code,
 * so we cross-check against the regionForCountry() lookup; only
 * codes the picker recognises get through.
 */
class HostnameCountryResolver : PoolImporter.CountryResolver {

    override suspend fun countryCode(host: String): String {
        // Two strategies, in order:
        // 1. Filename pattern: "<cc>-<city>-<rest>" — strongest
        //    signal, used by all major commercial providers.
        // 2. Hostname pattern: "<cc>.<provider>.com" — older
        //    style. Same first-2-chars heuristic but on dot-
        //    separated parts rather than dash-separated.
        //
        // Note: this resolver is called with the ENDPOINT host,
        // not the filename. For pattern 1 to work, the importer
        // would call us with the filename. Currently we work on
        // host only; we're prepared if the API extends.
        val lower = host.lowercase()

        // Pattern 1 fallback - some providers' endpoint hostnames
        // ARE filename-styled (e.g., "at-vie-wg-001.example.net").
        val firstSegment = lower.substringBefore('.')
        val maybeCC = firstSegment.substringBefore('-').uppercase()
        if (maybeCC.length == 2 && maybeCC.all { it.isLetter() }) {
            if (PoolPicker.regionForCountry(maybeCC) != "Other") {
                return maybeCC
            }
        }

        // Pattern 2 - direct dot-segmented: "ch.example.net"
        if (firstSegment.length == 2 && firstSegment.all { it.isLetter() }) {
            val cc = firstSegment.uppercase()
            if (PoolPicker.regionForCountry(cc) != "Other") {
                return cc
            }
        }

        return ""
    }

    /**
     * Companion variant that takes the FILENAME as input. Use this
     * when the importer has the original filename available - it
     * gives a stronger signal than the endpoint hostname (which
     * commercial providers often replace with a load-balancer DNS).
     */
    suspend fun countryCodeFromFilename(filename: String): String {
        val base = filename.substringBeforeLast('.', filename)
        val firstDash = base.substringBefore('-').uppercase()
        if (firstDash.length == 2 && firstDash.all { it.isLetter() }) {
            if (PoolPicker.regionForCountry(firstDash) != "Other") {
                return firstDash
            }
        }
        return ""
    }
}
