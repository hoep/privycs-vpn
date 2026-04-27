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
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.flowOn
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.coroutines.withContext
import java.io.ByteArrayOutputStream
import java.io.InputStream
import java.net.InetAddress
import java.time.Instant
import java.util.UUID
import java.util.zip.ZipEntry
import java.util.zip.ZipInputStream

/**
 * Pool importer — Android port of pool_import.go.
 *
 * Differences from desktop:
 *   - Uri-based input (not filesystem paths). User picks a file via
 *     ACTION_OPEN_DOCUMENT, system grants read URI permission, we
 *     read the InputStream.
 *   - DNS resolution worker pool via Coroutines + Semaphore (20
 *     concurrent), not a goroutine-channel pattern. Same throughput.
 *   - Country lookup: there is no embedded MMDB on Android (would
 *     bloat APK by ~5MB). Instead we do best-effort lookup and
 *     leave Country="" if no resolution available; the picker
 *     degrades gracefully (Random within unfiltered set).
 *
 * Progress: emitted via Flow so the Compose UI can collect with
 * collectAsState() and update the import-progress sheet without
 * polling.
 */
class PoolImporter(
    private val context: Context,
    private val countryResolver: CountryResolver? = null
) {

    /**
     * Country resolver interface. App injects a real implementation
     * if MMDB is available, else null is fine.
     */
    interface CountryResolver {
        suspend fun countryCode(host: String): String
    }

    /**
     * Imports from a list of Android Uris. Each Uri may point at
     * a .zip archive or a directly-selected .conf/.ovpn/.sswan.
     * Progress is emitted via the returned Flow.
     */
    fun importFromUris(uris: List<Uri>): Flow<PoolImportProgress> = flow {
        val state = mutableImportState()

        emit(PoolImportProgress(stage = PoolImportProgress.Stage.EXTRACTING))

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
                    else -> {
                        state.skipped.add(SkippedFile(displayName, "unsupported extension"))
                    }
                }
            } catch (e: Exception) {
                state.skipped.add(SkippedFile(displayName, "read failed: ${e.message}"))
            }
        }

        emit(PoolImportProgress(stage = PoolImportProgress.Stage.PARSING, total = entries.size))

        // Stage 2: parse — detect protocol + extract endpoint.
        data class Parsed(val entry: ImportEntry, val protocol: VpnProtocol, val host: String)
        val parsedList = mutableListOf<Parsed>()
        for ((i, e) in entries.withIndex()) {
            val protocol = detectProtocolFromFilename(e.name)
            if (protocol == null) {
                state.skipped.add(SkippedFile(e.name, "unsupported extension"))
                continue
            }
            val host = extractEndpointHost(protocol, String(e.content, Charsets.UTF_8))
            if (host.isEmpty()) {
                state.skipped.add(SkippedFile(e.name, "no endpoint in config"))
                continue
            }
            parsedList.add(Parsed(e, protocol, host))
            emit(
                PoolImportProgress(
                    stage = PoolImportProgress.Stage.PARSING,
                    current = i + 1,
                    total = entries.size,
                    imported = parsedList.size,
                    skipped = state.skipped.size
                )
            )
        }

        // Stage 3: resolve hostnames in parallel for the country
        // lookup. Bounded semaphore caps concurrency so we don't
        // exhaust the DNS resolver.
        val resolveTotal = parsedList.size
        emit(
            PoolImportProgress(
                stage = PoolImportProgress.Stage.RESOLVING,
                current = 0,
                total = resolveTotal,
                imported = state.members.size,
                skipped = state.skipped.size
            )
        )

        val countries = resolveCountriesParallel(parsedList.map { it.host })

        // Assemble members in original order.
        for ((i, p) in parsedList.withIndex()) {
            val cc = countries.getOrElse(i) { "" }
            val member = PoolMember(
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
            state.members.add(member)
        }

        emit(
            PoolImportProgress(
                stage = PoolImportProgress.Stage.DONE,
                current = resolveTotal,
                total = resolveTotal,
                imported = state.members.size,
                skipped = state.skipped.size
            )
        )

        Log.i(TAG, "imported ${state.members.size}, skipped ${state.skipped.size} from ${uris.size} uris")
    }.flowOn(Dispatchers.IO)

    /**
     * Synchronous-from-flow assembly path used by the UI's
     * "create pool" button after the import flow completes.
     * The flow's last emit doesn't carry the actual member list;
     * this re-runs the work and returns the full result.
     */
    suspend fun importToResult(uris: List<Uri>): PoolImportResult = withContext(Dispatchers.IO) {
        val state = mutableImportState()
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
                    else -> state.skipped.add(SkippedFile(displayName, "unsupported extension"))
                }
            } catch (e: Exception) {
                state.skipped.add(SkippedFile(displayName, "read failed: ${e.message}"))
            }
        }

        data class Parsed(val entry: ImportEntry, val protocol: VpnProtocol, val host: String)
        val parsedList = mutableListOf<Parsed>()
        for (e in entries) {
            val protocol = detectProtocolFromFilename(e.name)
            if (protocol == null) {
                state.skipped.add(SkippedFile(e.name, "unsupported extension"))
                continue
            }
            val host = extractEndpointHost(protocol, String(e.content, Charsets.UTF_8))
            if (host.isEmpty()) {
                state.skipped.add(SkippedFile(e.name, "no endpoint in config"))
                continue
            }
            parsedList.add(Parsed(e, protocol, host))
        }

        val countries = resolveCountriesParallel(parsedList.map { it.host })
        for ((i, p) in parsedList.withIndex()) {
            val cc = countries.getOrElse(i) { "" }
            state.members.add(
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

        PoolImportResult(members = state.members, skipped = state.skipped)
    }

    private suspend fun resolveCountriesParallel(hosts: List<String>): List<String> = coroutineScope {
        val semaphore = Semaphore(DNS_CONCURRENCY)
        hosts.map { host ->
            async(Dispatchers.IO) {
                semaphore.withPermit {
                    if (countryResolver == null) return@async ""
                    try {
                        countryResolver.countryCode(host)
                    } catch (e: Exception) {
                        ""
                    }
                }
            }
        }.awaitAll()
    }

    private fun queryFileName(uri: Uri): String? {
        return try {
            context.contentResolver.query(uri, arrayOf(android.provider.OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
                if (c.moveToFirst()) c.getString(0) else null
            }
        } catch (e: Exception) {
            null
        } ?: uri.lastPathSegment
    }

    private fun extractZipFromStream(input: InputStream): List<ImportEntry> {
        val out = mutableListOf<ImportEntry>()
        ZipInputStream(input).use { zip ->
            var entry: ZipEntry? = zip.nextEntry
            while (entry != null) {
                if (!entry.isDirectory) {
                    val name = entry.name.substringAfterLast('/')
                    val ext = name.substringAfterLast('.', "").lowercase()
                    if (ext in listOf("conf", "ovpn", "sswan")) {
                        if (entry.size <= MAX_CONFIG_BYTES) {
                            val baos = ByteArrayOutputStream()
                            val buf = ByteArray(8192)
                            var read = zip.read(buf)
                            while (read >= 0) {
                                baos.write(buf, 0, read)
                                if (baos.size() > MAX_CONFIG_BYTES) break
                                read = zip.read(buf)
                            }
                            if (baos.size() <= MAX_CONFIG_BYTES) {
                                out.add(ImportEntry(name = name, content = baos.toByteArray()))
                            }
                        }
                    }
                }
                entry = zip.nextEntry
            }
        }
        return out
    }

    private fun detectProtocolFromFilename(name: String): VpnProtocol? =
        when (name.substringAfterLast('.', "").lowercase()) {
            "conf" -> VpnProtocol.WIREGUARD
            "ovpn" -> VpnProtocol.OPENVPN
            "sswan" -> VpnProtocol.IPSEC
            else -> null
        }

    private fun extractEndpointHost(protocol: VpnProtocol, content: String): String {
        val raw = when (protocol) {
            VpnProtocol.WIREGUARD -> extractWireGuardEndpoint(content)
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

    private data class MutableImportState(
        val members: MutableList<PoolMember> = mutableListOf(),
        val skipped: MutableList<SkippedFile> = mutableListOf()
    )

    private fun mutableImportState() = MutableImportState()

    companion object {
        private const val TAG = "PoolImporter"
        private const val DNS_CONCURRENCY = 20
        private const val MAX_CONFIG_BYTES = 1L * 1024 * 1024  // 1 MB per file
    }
}
