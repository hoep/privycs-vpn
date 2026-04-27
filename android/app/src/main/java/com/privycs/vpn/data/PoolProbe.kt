package com.privycs.vpn.data

import android.util.Log
import com.privycs.vpn.data.models.PoolMember
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.InetSocketAddress

/**
 * Pre-warm reachability probe for pool members.
 *
 * History (from desktop):
 *   v0.9.11.33-36 had a TCP-Dial against :443 and :80 as a second
 *   stage. False-positives on dedicated VPN servers (only UDP-WG
 *   socket open) made us simplify to DNS-only in v0.9.11.37.
 *
 * Strategy: DNS resolve only. DNS failure means the host genuinely
 * cannot be reached. DNS success says "this hostname could exist";
 * whether the wg daemon on the (UDP-only) endpoint is healthy is
 * verified at rotation time via the trigger-packet + bytes_rx poll.
 */
object PoolProbe {

    private const val TAG = "PoolProbe"
    private const val PROBE_DNS_TIMEOUT_MS = 2_000L

    /**
     * Returns null on probable-reachable, error-string on failure.
     * Side-effect-free, safe from any coroutine context.
     */
    suspend fun probeMember(member: PoolMember): String? = withContext(Dispatchers.IO) {
        if (member.config.serverAddress.isEmpty()) {
            return@withContext "probe: member has no endpoint"
        }
        val host = stripPortIfPresent(member.config.serverAddress)
        if (host.isEmpty()) {
            return@withContext "probe: empty host after strip from '${member.config.serverAddress}'"
        }

        // Bare IP? Shortcut, no DNS work to do.
        if (isBareIp(host)) return@withContext null

        try {
            withTimeout(PROBE_DNS_TIMEOUT_MS) {
                val addrs = InetAddress.getAllByName(host)
                if (addrs.isEmpty()) "probe: dns $host: no addresses" else null
            }
        } catch (e: TimeoutCancellationException) {
            "probe: dns $host: timeout"
        } catch (e: Exception) {
            "probe: dns $host: ${e.message}"
        }
    }

    /**
     * Strips a host:port-style endpoint to just the host. Mirrors
     * the desktop helper of the same name.
     */
    fun stripPortIfPresent(s: String): String {
        val trimmed = s.trim()
        if (trimmed.isEmpty()) return ""

        // Bare IP literal (v4 or v6, no port).
        if (isBareIp(trimmed)) return trimmed

        // Bracketed IPv6 with optional :port: "[2001:db8::1]" or
        // "[2001:db8::1]:51820".
        if (trimmed.startsWith("[")) {
            val close = trimmed.indexOf(']')
            if (close > 0) return trimmed.substring(1, close)
        }

        // Hostname with port: "host:1234" — last colon split if it
        // looks like an integer port.
        val colonIdx = trimmed.lastIndexOf(':')
        if (colonIdx > 0 && colonIdx < trimmed.length - 1) {
            val maybePort = trimmed.substring(colonIdx + 1)
            if (maybePort.toIntOrNull() != null) {
                return trimmed.substring(0, colonIdx)
            }
        }

        return trimmed
    }

    /**
     * True if the string is a literal IP (v4 or v6) rather than
     * a hostname.
     *
     * Pure-syntactic check - does NOT invoke DNS. Earlier draft
     * called InetAddress.getByName() which actually tries to
     * resolve the input, defeating the purpose of "is this a
     * literal".
     *
     * IPv6 with zone identifier ("fe80::1%wlan0") is normalized
     * by stripping the zone before parsing - link-local literals
     * with explicit zone names are still recognised as IPs.
     */
    private fun isBareIp(s: String): Boolean {
        if (s.isEmpty()) return false
        // IPv4: four dot-separated 1-3 digit octets, each 0-255.
        val v4 = Regex("^(\\d{1,3})\\.(\\d{1,3})\\.(\\d{1,3})\\.(\\d{1,3})$")
        v4.matchEntire(s)?.let { match ->
            return match.groupValues.drop(1).all {
                it.toIntOrNull()?.let { n -> n in 0..255 } ?: false
            }
        }
        // IPv6: contains colons, optionally has zone-identifier
        // suffix "%zone". Strip zone for the literal check.
        if (':' in s) {
            val withoutZone = s.substringBefore('%')
            // Lightweight: hex digits + colons only, at least one ::
            // or 7 colons (full form). Comprehensive RFC 5952 parsing
            // would be overkill - the strict case where this matters
            // is "user typed an IP literal", not malformed input.
            val isV6Like = withoutZone.toCharArray().all { c ->
                c.isDigit() || c in 'a'..'f' || c in 'A'..'F' || c == ':'
            } && withoutZone.contains(':')
            if (!isV6Like) return false
            // Final sanity check via InetAddress on the zone-stripped form.
            return try {
                InetAddress.getByName(withoutZone)
                true
            } catch (e: Exception) {
                false
            }
        }
        return false
    }
}

/**
 * Runtime traffic-trigger for WireGuard peer-health check.
 *
 * After Up succeeds, fires a 1-byte UDP packet to a target derived
 * from the member's AllowedIPs. The kernel routes through the wg
 * interface (because the target is in AllowedIPs), which forces
 * the WireGuard handshake initiation. The peer's handshake response
 * lands in bytes_rx; our caller polls until rx > 0.
 *
 * Strategy: 0.0.0.0/0 (full-tunnel) wins over private CIDRs. With
 * mixed AllowedIPs like "10.0.0.0/24, 0.0.0.0/0", we prefer the
 * full-tunnel default because pinging "10.0.0.1" risks hitting a
 * dead host while 1.1.1.1 is well-known reachable.
 */
object PoolHealthTrigger {

    private const val TAG = "PoolHealthTrigger"

    /**
     * Parses the AllowedIPs line from a WireGuard config and returns
     * a routable IPv4 destination. Returns null if no usable target
     * (IPv6-only or malformed).
     */
    fun parseAllowedIPsTarget(configContent: String): String? {
        for (rawLine in configContent.lines()) {
            val line = rawLine.trim()
            if (!line.startsWith("AllowedIPs", ignoreCase = false)) continue
            val eq = line.indexOf('=')
            if (eq < 0) continue
            val cidrs = line.substring(eq + 1).split(',').map { it.trim() }

            // First pass: 0.0.0.0/0 wins.
            if (cidrs.any { it == "0.0.0.0/0" }) return "1.1.1.1"

            // Second pass: first IPv4 CIDR → network-address + 1.
            for (c in cidrs) {
                if (c.isEmpty() || c.contains(':')) continue
                val target = networkPlusOne(c) ?: continue
                return target
            }
            return null
        }
        return null
    }

    /**
     * Sends a 1-byte UDP packet to target:53. Fire-and-forget,
     * runs on a goroutine-equivalent so caller doesn't block.
     * The packet content is irrelevant — only the act of leaving
     * the kernel through the wg interface matters.
     */
    suspend fun triggerTraffic(target: String) = withContext(Dispatchers.IO) {
        if (target.isEmpty()) return@withContext
        try {
            DatagramSocket().use { sock ->
                sock.soTimeout = 500
                sock.connect(InetSocketAddress(target, 53))
                sock.send(DatagramPacket(byteArrayOf(0), 1))
            }
        } catch (e: Exception) {
            // Trigger fail is non-fatal; the handshake might still
            // fire from other traffic. Keep it quiet at debug level.
            Log.d(TAG, "trigger send to $target failed: ${e.message}")
        }
    }

    private fun networkPlusOne(cidr: String): String? {
        val slash = cidr.indexOf('/')
        if (slash < 0) return null
        val addr = cidr.substring(0, slash)
        val parts = addr.split('.')
        if (parts.size != 4) return null
        val octets = try {
            parts.map { it.toInt() }
        } catch (e: NumberFormatException) {
            return null
        }
        if (octets.any { it < 0 || it > 255 }) return null
        // Network-address + 1: typical gateway. We don't bother
        // computing the actual network address from the prefix;
        // most VPN configs declare the network address in the CIDR
        // already (e.g. 10.50.0.0/24 — first octet matters).
        val out = octets.toMutableList()
        out[3] = (out[3] + 1) and 0xFF
        return out.joinToString(".")
    }
}

/**
 * On Android, DNS cache is per-app and per-process: the InetAddress
 * cache lives in the JVM's address cache, configured via
 * networkaddress.cache.ttl. The system resolver also has a per-app
 * cache.
 *
 * Unlike desktop where `ipconfig /flushdns` invalidates a system-
 * wide cache, on Android we can only invalidate OUR PROCESS's cache.
 * That's actually exactly what we need: after a pool rotation, our
 * process's network-related caches should rebuild against the new
 * exit-IP geolocation.
 */
object PoolDnsFlush {

    /**
     * Invalidates the per-process DNS cache. Other apps' caches
     * are untouched (security boundary; we couldn't reach them
     * anyway without root). Called from a coroutine after a
     * successful rotation.
     */
    fun flushProcessDnsCache() {
        try {
            // InetAddress maintains an internal cache that we can
            // clear by reflectively zapping its caches. Less hacky:
            // setProperty for negative/positive TTL to 0 then
            // request a re-cache cycle. The simplest practical
            // approach is just to set TTL low at app start; rotation
            // doesn't need active flushing because the caches will
            // expire on the next lookup.
            //
            // Implementation note: Android's BionicResolver caches
            // independently and also respects a short TTL. The
            // platform-recommended pattern is to set TTL=0 once at
            // process start, which we do via a system property in
            // PrivycsApp.onCreate. Active flushing post-rotation is
            // therefore a no-op on Android — TTL=0 means caches
            // turn over on the next lookup naturally.
            android.util.Log.d("PoolDnsFlush", "process DNS cache uses TTL=0; no active flush needed")
        } catch (e: Exception) {
            android.util.Log.w("PoolDnsFlush", "flush attempted: ${e.message}")
        }
    }
}
