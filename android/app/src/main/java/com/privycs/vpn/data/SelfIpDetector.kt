package com.privycs.vpn.data

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import okhttp3.OkHttpClient
import okhttp3.Request
import java.net.InetAddress
import java.util.concurrent.TimeUnit

/**
 * Detects the user's public IP and country.
 *
 * Cross-platform mirror of desktop's selfip package. Probes
 * three endpoints in order; first one that returns a parseable
 * IP wins. Result cached for 1 hour OR until invalidate() is
 * called (e.g. on network change).
 *
 * The whole point: when the user activates a Pool, Geo-Nearest
 * needs to know "where is this user?" to pick a server in the
 * same region. Without this detector, Geo-Nearest degrades to
 * Random.
 *
 * Probe endpoints (all via plain HTTPS, all return raw IP in body):
 *   - https://1.1.1.1/cdn-cgi/trace (Cloudflare; just look for
 *     ip= line)
 *   - https://api.ipify.org (returns raw IP)
 *   - https://ifconfig.me/ip (returns raw IP)
 *
 * Why three: any single provider can rate-limit or fail.
 * Sequential with short timeouts (3s each = 9s worst-case).
 *
 * Why not DoH: Android's DoH support is ergonomically painful
 * before API 28+. The plain-HTTPS probes hit the same hosts
 * that DoH would resolve, the response body is just an IP
 * literal, no MITM concerns because we only TRUST the result
 * for country lookup (Geo-Nearest hint, not authentication).
 */
class SelfIpDetector(
    private val context: Context,
    private val mmdb: MmdbCountryResolver
) {

    data class Result(val ip: String, val country: String, val timestampMs: Long)

    private val client = OkHttpClient.Builder()
        .connectTimeout(3, TimeUnit.SECONDS)
        .readTimeout(3, TimeUnit.SECONDS)
        .build()

    private val mutex = Mutex()
    private var cached: Result? = null

    /**
     * Internal scope for the network-change-driven invalidate
     * coroutine. Tied to the application lifetime; never cancelled
     * because the detector is a process-singleton owned by
     * PrivycsApp.
     */
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    init {
        registerNetworkCallback()
    }

    /**
     * Watches for underlying-network changes (WiFi roam, mobile↔WiFi
     * handoff). When the network handle changes, the cached country
     * is invalidated so the next countryFor() probes a fresh IP.
     *
     * Without this hook, a user moving from WiFi-in-Germany to
     * mobile-in-France keeps the German country cached for up to
     * 1 hour. Geo-Nearest then picks German servers on a French
     * connection — measurable privacy/perf regression.
     */
    private fun registerNetworkCallback() {
        try {
            val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE)
                as ConnectivityManager
            val request = NetworkRequest.Builder()
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                .build()
            cm.registerNetworkCallback(request, object : ConnectivityManager.NetworkCallback() {
                private var lastHandle: Long = -1L
                override fun onAvailable(network: Network) {
                    val h = network.networkHandle
                    if (lastHandle != -1L && lastHandle != h) {
                        Log.d(TAG, "underlying network changed - invalidating SelfIp cache")
                        scope.launch { invalidate() }
                    }
                    lastHandle = h
                }
            })
        } catch (e: SecurityException) {
            Log.w(TAG, "network callback register failed: ${e.message}")
        }
    }

    /**
     * Returns the cached country if fresh, else triggers a probe.
     * Probe is bounded by [timeoutMs] in total (sum of all 3
     * endpoint timeouts).
     *
     * On any failure (no internet, captive portal, all probes
     * timed out) returns "" — caller treats this as "no
     * geo-detection available, degrade to Random".
     */
    suspend fun countryFor(timeoutMs: Long = 9_000L): String = mutex.withLock {
        val now = System.currentTimeMillis()
        cached?.let {
            if (now - it.timestampMs < CACHE_TTL_MS) return@withLock it.country
        }
        try {
            val ip = withTimeout(timeoutMs) { probePublicIp() }
            val country = if (ip.isNotEmpty()) {
                try {
                    mmdb.countryCodeBlocking(InetAddress.getByName(ip))
                } catch (e: Exception) {
                    ""
                }
            } else ""
            cached = Result(ip, country, now)
            country
        } catch (e: TimeoutCancellationException) {
            ""
        } catch (e: Exception) {
            Log.w(TAG, "selfip probe failed: ${e.message}")
            ""
        }
    }

    /** Returns the cached result or null. Safe from any thread. */
    fun cachedResult(): Result? = cached

    /** Forces a refetch on next countryFor call. */
    suspend fun invalidate() = mutex.withLock {
        cached = null
    }

    private suspend fun probePublicIp(): String = withContext(Dispatchers.IO) {
        for (probe in PROBES) {
            try {
                val req = Request.Builder().url(probe.url).build()
                val resp = client.newCall(req).execute()
                if (!resp.isSuccessful) {
                    resp.close()
                    continue
                }
                val body = resp.body?.string().orEmpty()
                resp.close()
                val ip = probe.parse(body)
                if (ip.isNotEmpty() && ip.isParseableIp()) return@withContext ip
            } catch (e: Exception) {
                // Try next probe.
            }
        }
        ""
    }

    private fun String.isParseableIp(): Boolean = try {
        InetAddress.getByName(this)
        // Reject hostnames-that-resolved by checking the literal
        // looks ip-shaped. Cheap regex avoids a second resolution
        // round-trip.
        matches(Regex("^\\d{1,3}(\\.\\d{1,3}){3}$")) || contains(':')
    } catch (e: Exception) {
        false
    }

    private data class Probe(val url: String, val parse: (String) -> String)

    companion object {
        private const val TAG = "SelfIpDetector"
        private const val CACHE_TTL_MS = 60 * 60 * 1000L  // 1 hour

        private val PROBES = listOf(
            // Cloudflare trace returns multi-line key=value text
            // with an "ip=..." line.
            Probe("https://1.1.1.1/cdn-cgi/trace") { body ->
                body.lines().firstOrNull { it.startsWith("ip=") }
                    ?.removePrefix("ip=")?.trim().orEmpty()
            },
            // ipify returns just the IP literal in the body.
            Probe("https://api.ipify.org") { body ->
                body.trim()
            },
            // ifconfig.me/ip returns just the IP literal.
            Probe("https://ifconfig.me/ip") { body ->
                body.trim()
            }
        )
    }
}
