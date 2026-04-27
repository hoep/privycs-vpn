package com.privycs.vpn.data

import android.content.Context
import android.util.Log
import com.maxmind.db.Reader
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File
import java.io.FileOutputStream
import java.net.InetAddress

/**
 * MMDB-based country resolver. Looks up IPs in a bundled
 * GeoLite2-Country (or dbip-country-lite) MMDB database to
 * determine the country code.
 *
 * Why MMDB:
 *   - Most VPN provider configs use IP-literal endpoints, not
 *     hostnames. The HostnameCountryResolver only catches the
 *     prefix-pattern case ("xx-yyy-wg-001.conf") which is rare
 *     in practice. MMDB lookup gets ~100% coverage.
 *   - Updates are monthly. Bundle freshly with each app release.
 *   - APK size cost: ~3-6 MB depending on which MMDB. Trivial
 *     compared to OpenVPN+strongSwan native libraries already
 *     present.
 *
 * Database file:
 *   - Location: `assets/country.mmdb`
 *   - Format: MaxMind MMDB binary (works with GeoLite2-Country
 *     OR db-ip's dbip-country-lite — same binary format, both
 *     give us a "country" map with "iso_code" key).
 *   - Loading: extracted to filesDir at first use because the
 *     MaxMind reader needs a File or RandomAccessFile, not an
 *     asset InputStream. ~3-6 MB one-time disk write.
 *
 * Lookup performance: ~5-50μs per IP (binary tree walk in mmdb).
 * Cheap enough that we don't bother caching at this layer.
 */
class MmdbCountryResolver(private val context: Context) : PoolImporter.CountryResolver {

    @Volatile
    private var reader: Reader? = null

    /**
     * Lazily extracts the MMDB from assets to filesDir on first
     * call, then opens it. Subsequent calls reuse the open Reader.
     *
     * If extraction or open fails (asset missing, corrupted file),
     * we cache `null` and return "" forever after — a missing
     * MMDB degrades the picker to Random / Hostname-only, not a
     * crash.
     */
    private suspend fun getReader(): Reader? = withContext(Dispatchers.IO) {
        reader?.let { return@withContext it }
        synchronized(this@MmdbCountryResolver) {
            reader?.let { return@synchronized it }
            try {
                val target = File(context.filesDir, "country.mmdb")
                if (!target.exists() || target.length() == 0L) {
                    extractFromAssets(target)
                }
                if (!target.exists() || target.length() == 0L) {
                    Log.w(TAG, "MMDB asset missing - country lookup disabled")
                    return@synchronized null
                }
                val r = Reader(target)
                reader = r
                Log.i(TAG, "MMDB opened: ${target.length()} bytes")
                r
            } catch (e: Exception) {
                Log.e(TAG, "MMDB open failed: ${e.message}", e)
                null
            }
        }
    }

    private fun extractFromAssets(target: File) {
        // App ships the MMDB at assets/country.mmdb. We extract once
        // to filesDir because the reader API needs a real File. The
        // copy is one-shot per install (and per app upgrade if the
        // mmdb is newer).
        try {
            context.assets.open("country.mmdb").use { input ->
                FileOutputStream(target).use { out ->
                    input.copyTo(out)
                }
            }
            Log.i(TAG, "MMDB extracted to ${target.absolutePath}")
        } catch (e: Exception) {
            // Not fatal - asset may be missing in dev/CI builds.
            // Silently leave target empty; getReader() will detect.
            Log.w(TAG, "MMDB extract failed: ${e.message}")
        }
    }

    override suspend fun countryCode(host: String): String = withContext(Dispatchers.IO) {
        val r = getReader() ?: return@withContext ""
        val ip = try {
            // For literal IPs this is essentially zero-cost. For
            // hostnames it does DNS resolution; the caller's worker
            // pool ensures we don't hammer the resolver.
            InetAddress.getByName(host)
        } catch (e: Exception) {
            return@withContext ""
        }
        try {
            // MMDB's get() returns a generic Map<String, Any> for
            // the matched record. Both GeoLite2-Country and dbip-
            // country-lite expose `country.iso_code` for the ISO
            // 3166-1 alpha-2 code.
            @Suppress("UNCHECKED_CAST")
            val record = r.get(ip, Map::class.java) as? Map<String, Any?> ?: return@withContext ""
            val country = record["country"] as? Map<*, *> ?: return@withContext ""
            (country["iso_code"] as? String).orEmpty().uppercase()
        } catch (e: Exception) {
            ""
        }
    }

    /**
     * Synchronous variant for callers already on a worker thread
     * (e.g. SelfIpDetector after fetching the user's public IP).
     * Blocks until reader is open.
     */
    fun countryCodeBlocking(ip: InetAddress): String {
        val r = try {
            // Fast path: reader already open. Slow path: re-extract
            // from assets, which we run from a runBlocking just to
            // satisfy the caller; this is one-shot per install.
            reader ?: kotlinx.coroutines.runBlocking { getReader() }
        } catch (e: Exception) {
            null
        } ?: return ""
        return try {
            @Suppress("UNCHECKED_CAST")
            val record = r.get(ip, Map::class.java) as? Map<String, Any?> ?: return ""
            val country = record["country"] as? Map<*, *> ?: return ""
            (country["iso_code"] as? String).orEmpty().uppercase()
        } catch (e: Exception) {
            ""
        }
    }

    fun close() {
        try {
            reader?.close()
        } catch (e: Exception) {
            // ignore
        }
        reader = null
    }

    companion object {
        private const val TAG = "MmdbResolver"
    }
}

/**
 * Chains MMDB lookup first (more accurate for IP endpoints),
 * falls back to hostname-pattern parsing for the rare case where
 * the endpoint is a load-balancer DNS that doesn't reverse-resolve
 * to a useful country (e.g. a CDN-fronted endpoint sitting on AWS
 * IPs that map to "US" instead of the actual server location).
 */
class CombinedCountryResolver(
    private val mmdb: MmdbCountryResolver,
    private val hostname: HostnameCountryResolver
) : PoolImporter.CountryResolver {

    override suspend fun countryCode(host: String): String {
        val byMmdb = mmdb.countryCode(host)
        if (byMmdb.isNotEmpty()) return byMmdb
        return hostname.countryCode(host)
    }
}
