package com.privycs.vpn.data

import android.content.Context
import android.net.Uri
import android.util.Log
import com.privycs.vpn.data.models.PoolImportProgress
import com.privycs.vpn.data.models.PoolImportResult
import com.privycs.vpn.data.models.PoolMember
import com.privycs.vpn.data.models.ProtocolConfig
import com.privycs.vpn.data.models.SkippedFile
import com.privycs.vpn.data.models.VpnProtocol
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.coroutines.withContext
import java.io.ByteArrayOutputStream
import java.io.InputStream
import java.time.Instant
import java.util.UUID
import java.util.zip.ZipEntry
import java.util.zip.ZipInputStream

/**
 * Pool importer — Android port of pool_import.go.
 *
 * Pipeline (one-pass, single source of truth):
 *   1. Extract: unpack ZIPs in-memory, surface direct configs.
 *   2. Parse: detect protocol + extract endpoint hostname.
 *   3. Resolve: parallel hostname resolution (Coroutine semaphore).
 *   4. Country lookup: per-host via the supplied resolver.
 *   5. Assemble: build PoolMember list + skipped reasons.
 *
 * Progress emitted via SharedFlow so the Compose UI subscribes
 * once and gets every event. Earlier drafts had two parallel
 * paths (importFromUris with progress + importToResult without)
 * which read each file twice; consolidated.
 */
class PoolImporter(
    private val context: Context,
    private val countryResolver: CountryResolver?
) {

    /**
     * Country resolver interface. App injects a real implementation
     * (HostnameCountryResolver works without an external DB) or
     * null for "no country lookup at all".
     */
    interface CountryResolver {
        suspend fun countryCode(host: String): String
    }

    private val _progress = MutableSharedFlow<PoolImportProgress>(replay = 1)
    val progress: SharedFlow<PoolImportProgress> = _progress.asSharedFlow()

    /**
     * Imports from Android Uris, returning the assembled result.
     * Progress emits to the shared `progress` flow during the run.
     *
     * UI binds the flow once to render its progress sheet, then
     * awaits this method. There is no "two pipelines" pattern -
     * one pass, one source of truth.
     */
    suspend fun importFromUris(uris: List<Uri>): PoolImportResult = withContext(Dispatchers.IO) {
        val skipped = mutableListOf<SkippedFile>()
        val members = mutableListOf<PoolMember>()

        emit(PoolImportProgress(stage = PoolImportProgress.Stage.EXTRACTING))

        // Stage 1: extract.
        val entries = mutableListOf<ImportEntry>()
        for (uri in uris) {
            val displayName = queryFileName(uri) ?: "unknown"
            val ext = displayName.substringAfterLast('.', "").lowercase()
            try {
                when (ext) {
                    "zip" -> {
                        context.contentResolver.openInputStream(uri)?.use { stream ->
                            entries.addAll(extractZipFromStream(stream))
                        }
                    }
                    "conf", "ovpn", "sswan" -> {
                        context.contentResolver.openInputStream(uri)?.use { stream ->
                            entries.add(ImportEntry(name = displayName, content = stream.readBytes()))
                        }
                    }
                    else -> skipped.add(SkippedFile(displayName, "unsupported extension"))
                }
            } catch (e: Exception) {
                skipped.add(SkippedFile(displayName, "read failed: ${e.message}"))
            }
        }

        emit(PoolImportProgress(stage = PoolImportProgress.Stage.PARSING, total = entries.size))

        // Stage 2: parse — detect protocol + extract endpoint.
        data class Parsed(val entry: ImportEntry, val protocol: VpnProtocol, val host: String)
        val parsedList = mutableListOf<Parsed>()
        for ((i, e) in entries.withIndex()) {
            val content = String(e.content, Charsets.UTF_8)
            val protocol = detectProtocolFromFilename(e.name, content)
            if (protocol == null) {
                skipped.add(SkippedFile(e.name, "unsupported extension"))
                continue
            }
            val host = extractEndpointHost(protocol, content)
            if (host.isEmpty()) {
                skipped.add(SkippedFile(e.name, "no endpoint in config"))
                continue
            }
            parsedList.add(Parsed(e, protocol, host))
            emit(
                PoolImportProgress(
                    stage = PoolImportProgress.Stage.PARSING,
                    current = i + 1,
                    total = entries.size,
                    imported = parsedList.size,
                    skipped = skipped.size
                )
            )
        }

        // Stage 3 + 4: parallel host → country resolution.
        val resolveTotal = parsedList.size
        emit(
            PoolImportProgress(
                stage = PoolImportProgress.Stage.RESOLVING,
                current = 0,
                total = resolveTotal,
                imported = members.size,
                skipped = skipped.size
            )
        )

        val countries = resolveCountriesParallel(parsedList.map { it.host to it.entry.name })

        // Stage 5: assemble in original order.
        for ((i, p) in parsedList.withIndex()) {
            val cc = countries.getOrElse(i) { "" }
            members.add(
                PoolMember(
                    id = UUID.randomUUID().toString(),
                    name = p.entry.name.substringBeforeLast('.'),
                    config = ProtocolConfig(
                        protocol = p.protocol,
                        configContent = String(p.entry.content, Charsets.UTF_8),
                        filename = p.entry.name,
                        serverAddress = p.host,
                        addedAt = Instant.now().toString()
                    ),
                    country = cc,
                    region = PoolPicker.regionForCountry(cc),
                    active = true
                )
            )
        }

        emit(
            PoolImportProgress(
                stage = PoolImportProgress.Stage.DONE,
                current = resolveTotal,
                total = resolveTotal,
                imported = members.size,
                skipped = skipped.size
            )
        )

        Log.i(TAG, "imported ${members.size}, skipped ${skipped.size} from ${uris.size} uris")
        PoolImportResult(members = members, skipped = skipped)
    }

    /**
     * Resolves countries for each (host, filename) pair in parallel.
     * Filename is used as a secondary signal for the
     * HostnameCountryResolver pattern — it captures the country
     * code in commercial provider naming when the endpoint
     * hostname (load-balancer DNS) does not.
     */
    private suspend fun resolveCountriesParallel(items: List<Pair<String, String>>): List<String> = coroutineScope {
        val semaphore = Semaphore(DNS_CONCURRENCY)
        items.map { (host, filename) ->
            async(Dispatchers.IO) {
                semaphore.withPermit {
                    if (countryResolver == null) return@async ""
                    try {
                        // Try host first (cheaper if it works);
                        // fall back to filename if the resolver
                        // exposes a filename method.
                        val byHost = countryResolver.countryCode(host)
                        if (byHost.isNotEmpty()) return@async byHost
                        // HostnameCountryResolver has a filename
                        // overload; reflect-or-instanceof to use it.
                        if (countryResolver is HostnameCountryResolver) {
                            countryResolver.countryCodeFromFilename(filename)
                        } else {
                            ""
                        }
                    } catch (e: Exception) {
                        ""
                    }
                }
            }
        }.awaitAll()
    }

    private suspend fun emit(p: PoolImportProgress) {
        _progress.emit(p)
    }

    private fun queryFileName(uri: Uri): String? = try {
        context.contentResolver.query(
            uri,
            arrayOf(android.provider.OpenableColumns.DISPLAY_NAME),
            null, null, null
        )?.use { c ->
            if (c.moveToFirst()) c.getString(0) else null
        }
    } catch (e: Exception) {
        null
    } ?: uri.lastPathSegment

    private fun extractZipFromStream(input: InputStream): List<ImportEntry> {
        val out = mutableListOf<ImportEntry>()
        ZipInputStream(input).use { zip ->
            var entry: ZipEntry? = zip.nextEntry
            while (entry != null) {
                if (!entry.isDirectory) {
                    val name = entry.name.substringAfterLast('/')
                    val ext = name.substringAfterLast('.', "").lowercase()
                    if (ext in listOf("conf", "ovpn", "sswan")) {
                        // Read up to MAX_CONFIG_BYTES then stop. Prevents
                        // ZIP-bomb and out-of-memory on entries with
                        // malformed size headers (size = -1).
                        val baos = ByteArrayOutputStream()
                        val buf = ByteArray(8192)
                        var read = zip.read(buf)
                        var total = 0L
                        while (read >= 0 && total <= MAX_CONFIG_BYTES) {
                            baos.write(buf, 0, read)
                            total += read.toLong()
                            read = zip.read(buf)
                        }
                        if (total <= MAX_CONFIG_BYTES) {
                            out.add(ImportEntry(name = name, content = baos.toByteArray()))
                        }
                    }
                }
                entry = zip.nextEntry
            }
        }
        return out
    }

    private fun detectProtocolFromFilename(name: String, content: String): VpnProtocol? =
        when (name.substringAfterLast('.', "").lowercase()) {
            // .conf is shared between vanilla WG and AmneziaWG — same
            // grammar, AWG just has additional [Interface] keys.
            // Content-detect at import so pool members land in the
            // right protocol slot.
            "conf" -> if (com.privycs.vpn.data.models.TunnelVariant.detect(content) ==
                com.privycs.vpn.data.models.TunnelVariant.AMNEZIAWG)
                VpnProtocol.AMNEZIAWG
            else
                VpnProtocol.WIREGUARD
            "ovpn" -> VpnProtocol.OPENVPN
            "sswan" -> VpnProtocol.IPSEC
            else -> null
        }

    private fun extractEndpointHost(protocol: VpnProtocol, content: String): String {
        val raw = when (protocol) {
            // AWG and vanilla WG share the .conf Endpoint grammar.
            VpnProtocol.WIREGUARD, VpnProtocol.AMNEZIAWG -> extractWireGuardEndpoint(content)
            VpnProtocol.OPENVPN -> extractOpenVPNEndpoint(content)
            VpnProtocol.IPSEC -> extractIPSecEndpoint(content)
        }
        return PoolProbe.stripPortIfPresent(raw)
    }

    private fun extractWireGuardEndpoint(content: String): String {
        for (line in content.lines()) {
            val t = line.trim()
            if (!t.lowercase().startsWith("endpoint")) continue
            val eq = t.indexOf('=')
            if (eq < 0) continue
            return t.substring(eq + 1).trim()
        }
        return ""
    }

    private fun extractOpenVPNEndpoint(content: String): String {
        for (line in content.lines()) {
            val t = line.trim()
            if (!t.startsWith("remote ")) continue
            val parts = t.split(Regex("\\s+"))
            if (parts.size >= 2) return parts[1]
        }
        return ""
    }

    private fun extractIPSecEndpoint(content: String): String {
        val keys = listOf("\"addr\"", "\"remote.addr\"", "\"remote_addr\"")
        for (key in keys) {
            val i = content.indexOf(key)
            if (i < 0) continue
            val rest = content.substring(i + key.length)
            val colon = rest.indexOf(':')
            if (colon < 0) continue
            val afterColon = rest.substring(colon + 1).trimStart()
            if (afterColon.isEmpty() || afterColon[0] != '"') continue
            val end = afterColon.indexOf('"', startIndex = 1)
            if (end <= 0) continue
            return afterColon.substring(1, end)
        }
        return ""
    }

    private data class ImportEntry(val name: String, val content: ByteArray)

    companion object {
        private const val TAG = "PoolImporter"
        // 8 instead of the original 20: large pool imports (600+
        // members) on low-end devices saw memory pressure with 20
        // concurrent DNS lookups when the OkHttp connection pool
        // and per-coroutine context allocations stacked up. 8 is
        // still well above the typical 1-2 inflight on healthy
        // resolvers and below the Android NetworkSecurityPolicy
        // soft cap that some MIUI/EMUI builds enforce.
        private const val DNS_CONCURRENCY = 8
        private const val MAX_CONFIG_BYTES = 1L * 1024 * 1024
    }
}
