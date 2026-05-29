package com.privycs.vpn.util

import java.net.InetAddress

/**
 * DNS-Override input handling shared between SettingsScreen,
 * PoolDetailScreen and the connect-time inject paths.
 *
 * Splitter, validator, well-known-provider hint table, and a
 * "what is this resolver?" lookup so the UI can label a pasted
 * "1.1.1.1" as Cloudflare and suggest enabling Android Private DNS
 * for DoT encryption (which Privycs cannot toggle itself - that
 * is a system-level setting under Network → Private DNS).
 *
 * Mirrors desktop dns_override.go's parseDnsServers / IsValidDnsServer
 * / DnsProviders so cross-platform users get identical preset options
 * and validation behaviour.
 */
object DnsValidator {

    /**
     * Split a comma- or whitespace-separated string of IP servers
     * into a clean list. Strips IPv6 web-style brackets
     * ("[2001:db8::1]" → "2001:db8::1") and trims any trailing
     * ":port" suffix when the entry is unambiguously not raw IPv6
     * (single colon = IPv4:port; multiple colons = bare IPv6 we
     * leave alone). Caller decides downstream formatting (WG comma,
     * OpenVPN one-per-line, strongSwan space, /etc/resolv.conf
     * line-per-server).
     */
    fun parseServers(input: String): List<String> {
        if (input.isBlank()) return emptyList()
        return input.split(',', ' ', '\t', '\n')
            .map { it.trim() }
            .map { normalizeEntry(it) }
            .filter { it.isNotEmpty() }
    }

    private fun normalizeEntry(raw: String): String {
        val s = raw.trim()
        if (s.isEmpty()) return s
        // [ipv6]:port or [ipv6]
        if (s.startsWith("[")) {
            val end = s.indexOf(']')
            if (end > 0) return s.substring(1, end)
            return s // malformed; let validator flag
        }
        // Exactly one colon = ipv4:port; chop. IPv6 has multiple
        // colons so we leave it untouched.
        if (s.count { it == ':' } == 1) {
            return s.substringBefore(':')
        }
        return s
    }

    /**
     * Returns the list of invalid (= not a numeric IPv4 / IPv6)
     * entries from the user's input. Empty list = all valid.
     * Used by Settings UI for inline error display before save.
     */
    fun invalidEntries(input: String): List<String> {
        return parseServers(input).filter { !isValidIp(it) }
    }

    /**
     * Pure-Kotlin numeric IP check. Avoids InetAddress.getByName
     * (which would do a DNS lookup for hostnames) - we explicitly
     * reject hostname-style inputs since the inject pipeline only
     * accepts numeric addresses.
     */
    fun isValidIp(s: String): Boolean {
        if (s.isBlank()) return false
        return try {
            // InetAddress.parseNumericAddress is API 29+; use the
            // older path: getByName only does DNS for hostnames,
            // numeric inputs return immediately. Then check that
            // the canonical form does not contain DNS-resolved
            // round-trip junk.
            val addr = InetAddress.getByName(s)
            // hostAddress should round-trip; if input was a
            // hostname, getByName would have done a lookup which
            // we don't want to count as valid.
            // Cheap heuristic: numeric IP literal contains only
            // digits, dots, colons, hex letters.
            val numericChars = s.all {
                it.isDigit() || it == '.' || it == ':' || it in 'a'..'f' || it in 'A'..'F'
            }
            numericChars && addr.hostAddress != null
        } catch (e: Exception) {
            false
        }
    }

    /**
     * Public DNS provider catalogue. Servers are dual-stack; DotHost
     * is the system Private DNS hostname for Android Settings →
     * Network → Private DNS. Note describes the privacy / blocking
     * trade-off so the Settings dropdown can show it as a subtitle.
     *
     * Keep the list in sync with desktop dns_override.go's
     * dnsProvidersList. Order is the dropdown order.
     */
    data class Provider(
        val id: String,
        val label: String,
        val servers: List<String>,
        val dotHost: String,
        val note: String,
    )

    val providers: List<Provider> = listOf(
        Provider(
            "cloudflare", "Cloudflare",
            listOf("1.1.1.1", "1.0.0.1", "2606:4700:4700::1111", "2606:4700:4700::1001"),
            "cloudflare-dns.com",
            "Fast, no logging beyond 24h",
        ),
        Provider(
            "cloudflare-malware", "Cloudflare (block malware)",
            listOf("1.1.1.2", "1.0.0.2", "2606:4700:4700::1112", "2606:4700:4700::1002"),
            "security.cloudflare-dns.com",
            "Blocks known malware domains",
        ),
        Provider(
            "cloudflare-family", "Cloudflare (block malware + adult)",
            listOf("1.1.1.3", "1.0.0.3", "2606:4700:4700::1113", "2606:4700:4700::1003"),
            "family.cloudflare-dns.com",
            "Family-safe filtering",
        ),
        Provider(
            "google", "Google",
            listOf("8.8.8.8", "8.8.4.4", "2001:4860:4860::8888", "2001:4860:4860::8844"),
            "dns.google",
            "Reliable, logs queries",
        ),
        Provider(
            "quad9", "Quad9 (block malware)",
            listOf("9.9.9.9", "149.112.112.112", "2620:fe::fe", "2620:fe::9"),
            "dns.quad9.net",
            "Swiss, malware blocking, no logging",
        ),
        Provider(
            "adguard", "AdGuard (block ads + trackers)",
            listOf("94.140.14.14", "94.140.15.15", "2a10:50c0::ad1:ff", "2a10:50c0::ad2:ff"),
            "dns.adguard-dns.com",
            "Default - blocks ads and trackers",
        ),
        Provider(
            "adguard-family", "AdGuard Family (block ads + trackers + adult)",
            listOf("94.140.14.15", "94.140.15.16", "2a10:50c0::bad1:ff", "2a10:50c0::bad2:ff"),
            "family.adguard-dns.com",
            "Family-safe content filtering on top of ad blocking",
        ),
        Provider(
            "adguard-unfiltered", "AdGuard (no filtering)",
            listOf("94.140.14.140", "94.140.14.141", "2a10:50c0::1:ff", "2a10:50c0::2:ff"),
            "unfiltered.adguard-dns.com",
            "Pass-through, no blocking",
        ),
        Provider(
            "mullvad", "Mullvad",
            listOf("194.242.2.2", "2a07:e340::2"),
            "dns.mullvad.net",
            "Logging-free, run by Mullvad VPN",
        ),
        Provider(
            "mullvad-adblock", "Mullvad (block ads + trackers)",
            listOf("194.242.2.3", "2a07:e340::3"),
            "adblock.dns.mullvad.net",
            "Mullvad with content blocking",
        ),
    )

    /**
     * Detect the best-matching provider for a given input string.
     * Matches when every parsed server in the input belongs to a
     * provider's canonical server list AND the input is fully
     * within that list (no extra servers from another provider).
     * Returns null when no exact match.
     */
    fun detectProvider(input: String): Provider? {
        val parsed = parseServers(input)
        if (parsed.isEmpty()) return null
        val want = parsed.mapNotNull {
            try { InetAddress.getByName(it).hostAddress } catch (_: Exception) { null }
        }.toSet()
        if (want.isEmpty()) return null
        return providers.firstOrNull { p ->
            val canonical = p.servers.mapNotNull {
                try { InetAddress.getByName(it).hostAddress } catch (_: Exception) { null }
            }.toSet()
            want.all { it in canonical }
        }
    }

    /**
     * Best-effort DNS resolution test. Resolves [host] (default
     * cloudflare.com) and returns the resolved addresses + elapsed
     * milliseconds. Uses the OS resolver, which inside the VPN
     * tunnel returns the tunnel's DNS results and outside returns
     * system-DNS results - either is informative diagnostic data.
     *
     * Synchronous; callers should run it on Dispatchers.IO.
     */
    data class TestResult(
        val host: String,
        val addresses: List<String>,
        val durationMs: Long,
        // v1.0.5.25: human-readable label for the resolver in effect.
        // Filled in by the caller (it knows the user's DNS-override
        // and the detectProvider() result); we just round-trip it
        // through the TestResult so the UI has a single source for
        // its success-line render.
        val resolverLabel: String = "",
        val error: String? = null,
    )

    fun testResolution(
        host: String = "cloudflare.com",
        resolverLabel: String = "",
    ): TestResult {
        val target = host.ifBlank { "cloudflare.com" }
        val start = System.currentTimeMillis()
        return try {
            val addrs = InetAddress.getAllByName(target).map { it.hostAddress.orEmpty() }
            TestResult(target, addrs, System.currentTimeMillis() - start, resolverLabel)
        } catch (e: Exception) {
            TestResult(target, emptyList(), System.currentTimeMillis() - start, resolverLabel, e.message ?: "lookup failed")
        }
    }
}
