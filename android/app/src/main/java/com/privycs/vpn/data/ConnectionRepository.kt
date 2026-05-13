package com.privycs.vpn.data

import android.content.Context
import com.privycs.vpn.data.models.ConnectionRegistry
import com.privycs.vpn.data.models.ProtocolConfig
import com.privycs.vpn.data.models.VpnConnection
import com.privycs.vpn.data.models.VpnProtocol
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.serialization.json.Json
import java.io.File
import java.time.Instant
import java.util.UUID

class ConnectionRepository(private val context: Context) {

    private val json = Json {
        prettyPrint = true
        ignoreUnknownKeys = true
        encodeDefaults = true
    }

    private val file: File
        get() = File(context.filesDir, "connections.json")

    private val _registry = MutableStateFlow(load())
    val registry: StateFlow<ConnectionRegistry> = _registry.asStateFlow()

    val connections: List<VpnConnection>
        get() = _registry.value.connections

    val activeId: String
        get() = _registry.value.activeId

    fun getById(id: String): VpnConnection? =
        _registry.value.connections.find { it.id == id }

    fun getActive(): VpnConnection? =
        getById(_registry.value.activeId)

    fun addOrUpdate(
        connectionId: String?,
        name: String,
        protocolConfig: ProtocolConfig
    ): VpnConnection {
        val registry = _registry.value
        var conn = connectionId?.let { getById(it) }

        val cleanName = sanitizeConnectionName(name)

        // Every ProtocolConfig persisted by this method gets a
        // non-empty id. Caller's ProtocolConfig may have id=""
        // (typical for fresh ConfigParser.buildProtocolConfig
        // output); we fill one in. If the caller passed an explicit
        // id (e.g. backup restore preserving original) we honour it.
        val withId = if (protocolConfig.id.isEmpty())
            protocolConfig.copy(id = UUID.randomUUID().toString())
        else protocolConfig

        if (conn == null) {
            // When connectionId was supplied (e.g. from a backup import), keep
            // it. Previously we unconditionally generated a fresh UUID, which
            // meant a backup restore that feeds the three protocols one by
            // one would create three separate connections: the first call
            // spawned a new UUID, and the next two could not find it with
            // the original backup ID so they each spawned their own
            // connections. Honouring the supplied ID collapses them back
            // into a single multi-protocol connection.
            conn = VpnConnection(
                id = connectionId?.takeIf { it.isNotBlank() } ?: UUID.randomUUID().toString(),
                name = cleanName,
                activeProtocol = withId.protocol,
                activeConfigId = withId.id,
                createdAt = Instant.now().toString()
            )
            registry.connections.add(conn)
        }

        // Multi-config-per-protocol match strategy:
        //
        //   1. Explicit id match — caller passed a non-empty id that
        //      corresponds to an existing config. Re-import of the
        //      same logical endpoint, update in place.
        //
        //   2. Filename + protocol fallback — fresh import (id="")
        //      whose (protocol, filename) tuple matches an existing
        //      config. Same gateway re-download, same drag-drop of
        //      the same .conf, etc. Update in place; preserve the
        //      existing id so external references stay valid.
        //
        //   3. Otherwise — genuinely new config. Append.
        //
        // The pre-v0.9.15.7 version did (1) only and silently
        // appended on every fresh import — even re-downloading the
        // same gateway peer made a NEW config slot. The connection
        // accumulated duplicates and the protocol-pill row's
        // disambiguation logic (which keys on "same protocol type
        // appears more than once") then showed the per-config
        // filename ("Privycs-foo") instead of the clean protocol
        // label ("WireGuard"). That's the user's "warum stehen die
        // peer-namen jetzt in den protokoll pills?" report.
        // Match strategy — the slot is identified by (protocol,
        // filename). Re-import of the SAME slot replaces in place;
        // any other combination (different protocol type, different
        // filename within the same protocol) appends a new slot.
        //
        // User-stated intent (v0.9.15.10): "ich möchte in eine
        // connection auch 10 versch. gleiche protokoll configs laden
        // können". Multi-config-per-protocol-per-connection is
        // unlimited; the connection acts as a failover bag (like a
        // mini-Pool) of same-protocol endpoints. Cross-protocol
        // entries (WG + AWG of the same peer-name) ALSO coexist —
        // the user wants both for failover preference.
        //
        // Earlier (v0.9.15.9) cross-protocol filename-fallback was
        // wrong: it overwrote the existing WG slot when the user
        // downloaded the AWG variant of the same peer. Reverted.
        var existingIndex = -1
        if (protocolConfig.id.isNotEmpty()) {
            existingIndex = conn.protocols.indexOfFirst { it.id == protocolConfig.id }
        }
        if (existingIndex < 0 && protocolConfig.filename.isNotEmpty()) {
            existingIndex = conn.protocols.indexOfFirst {
                it.protocol == protocolConfig.protocol &&
                    it.filename == protocolConfig.filename
            }
        }
        if (existingIndex >= 0) {
            // Preserve id+nickname so activeConfigId / pool-member
            // refs stay valid across re-imports.
            val keep = conn.protocols[existingIndex]
            conn.protocols[existingIndex] = withId.copy(
                id = keep.id,
                nickname = keep.nickname,
            )
        } else {
            conn.protocols.add(withId)
        }

        if (conn.name.isBlank() && cleanName.isNotBlank()) {
            conn.name = cleanName
        }

        // If the connection had no active config yet, pin it to the
        // one we just added/updated.
        if (conn.activeConfigId.isEmpty()) {
            conn.activeConfigId = withId.id
            conn.activeProtocol = withId.protocol
        }

        // Auto-set as active if it is the first connection
        if (registry.connections.size == 1) {
            registry.activeId = conn.id
        }

        save()
        return conn
    }

    fun setActive(id: String) {
        _registry.value.activeId = id
        save()
    }

    /**
     * Pin a specific ProtocolConfig as the active one on this
     * connection. configId must correspond to an existing config —
     * returns false if it doesn't. Also updates the legacy
     * activeProtocol field for back-compat with code paths that
     * still read it.
     */
    fun setActiveConfig(connectionId: String, configId: String): Boolean {
        val conn = getById(connectionId) ?: return false
        val cfg = conn.getConfigById(configId) ?: return false
        conn.activeConfigId = configId
        conn.activeProtocol = cfg.protocol
        save()
        return true
    }

    /**
     * Back-compat alias: caller wants the first config of a given
     * protocol type to become active. Used by the protocol-switcher
     * pills that still think in protocol terms. With multi-config
     * support, "first config of this protocol" is sort-key-deterministic
     * (orderedConfigs filtered by protocol, take first).
     */
    fun setActiveProtocol(connectionId: String, protocol: VpnProtocol): Boolean {
        val conn = getById(connectionId) ?: return false
        val target = conn.orderedConfigs().firstOrNull { it.protocol == protocol } ?: return false
        return setActiveConfig(connectionId, target.id)
    }

    fun delete(id: String) {
        val registry = _registry.value
        registry.connections.removeAll { it.id == id }
        if (registry.activeId == id) {
            registry.activeId = registry.connections.firstOrNull()?.id ?: ""
        }
        save()
    }

    fun removeProtocol(connectionId: String, protocol: VpnProtocol) {
        val conn = getById(connectionId) ?: return
        val removed = conn.protocols.filter { it.protocol == protocol }
        conn.protocols.removeAll { it.protocol == protocol }

        // If the active config was among the removed ones, repick.
        if (removed.any { it.id == conn.activeConfigId } && conn.protocols.isNotEmpty()) {
            val replacement = conn.orderedConfigs().first()
            conn.activeConfigId = replacement.id
            conn.activeProtocol = replacement.protocol
        }

        if (conn.protocols.isEmpty()) {
            delete(connectionId)
        } else {
            save()
        }
    }

    fun updateLastConnected(connectionId: String) {
        val conn = getById(connectionId) ?: return
        conn.lastConnected = Instant.now().toString()
        save()
    }

    /**
     * Persist the runtime-assigned VPN inner IP to the matching
     * ProtocolConfig entry so the Configs screen can render it after
     * reload, even before the next connect. WireGuard's Address is
     * static (parsed from the .conf at import) and was already
     * persisted; OpenVPN + IPSec only learn their inner IP after
     * IKE_AUTH / TLS, so we update it here whenever PrivycsVpnService
     * observes a non-empty localAddress in VpnStatus.
     *
     * Empty `addr` is treated as a no-op so a transient null status
     * doesn't wipe out the previously-known address. Same protocol-
     * config entry replaced by index to keep the rest of the list
     * stable.
     */
    fun updateLocalAddress(connectionId: String, protocol: VpnProtocol, addr: String) {
        if (addr.isBlank()) return
        val conn = getById(connectionId) ?: return
        // Prefer writing to the currently-active config, falling
        // back to "first config of this protocol type" for older
        // callers that don't know about multi-config. This avoids
        // mis-attributing a connected WG-1's local address to a
        // dormant WG-2 in the same connection.
        val active = conn.getActiveConfig()
        val idx = if (active != null && active.protocol == protocol)
            conn.protocols.indexOfFirst { it.id == active.id }
        else
            conn.protocols.indexOfFirst { it.protocol == protocol }
        if (idx < 0) return
        if (conn.protocols[idx].localAddress == addr) return
        conn.protocols[idx].localAddress = addr
        save()
    }

    /**
     * Remove a specific ProtocolConfig by its id. Used by the
     * Connections screen's "delete this config" affordance when a
     * connection has multiple configs of the same protocol type.
     * If the removed config was the active one and others remain,
     * repick. If no configs remain, deletes the whole connection.
     */
    fun removeConfig(connectionId: String, configId: String) {
        val conn = getById(connectionId) ?: return
        val removed = conn.protocols.removeAll { it.id == configId }
        if (!removed) return
        if (conn.activeConfigId == configId && conn.protocols.isNotEmpty()) {
            val replacement = conn.orderedConfigs().first()
            conn.activeConfigId = replacement.id
            conn.activeProtocol = replacement.protocol
        }
        if (conn.protocols.isEmpty()) {
            delete(connectionId)
        } else {
            save()
        }
    }

    /** Rename a specific ProtocolConfig — sets its user-editable nickname. */
    fun renameConfig(connectionId: String, configId: String, nickname: String): Boolean {
        val conn = getById(connectionId) ?: return false
        val idx = conn.protocols.indexOfFirst { it.id == configId }
        if (idx < 0) return false
        conn.protocols[idx].nickname = nickname.trim()
        save()
        return true
    }

    /**
     * Rename an existing connection. Returns true if the connection was found
     * and the new name is non-blank, false otherwise. Empty or whitespace-only
     * names are rejected to avoid unreadable rows in the connections list.
     */
    fun rename(connectionId: String, newName: String): Boolean {
        val trimmed = newName.trim()
        if (trimmed.isEmpty()) return false
        val clean = sanitizeConnectionName(trimmed)
        val conn = getById(connectionId) ?: return false
        conn.name = clean
        save()
        return true
    }

    /**
     * Set the per-connection DNS override. Empty string clears it
     * (= inherit Settings global). Caller is responsible for IP
     * validation; the inject pipeline silently drops malformed
     * entries via DnsValidator.parseServers anyway.
     */
    fun updateDnsOverride(connectionId: String, override: String): Boolean {
        val conn = getById(connectionId) ?: return false
        conn.dnsOverride = override.trim()
        save()
        return true
    }

    /**
     * Replace obviously-bad names (raw config content that leaked through
     * a ContentProvider DISPLAY_NAME, a single non-alphanumeric glyph,
     * multi-line text) with a safe default. ConfigParser.deriveConnection-
     * Name does the same thing for the filename-derived code path; this
     * helper covers user-typed, gateway-API-derived, and backup-restored
     * names too.
     */
    private fun sanitizeConnectionName(raw: String): String {
        val trimmed = raw.trim()
        if (trimmed.isEmpty()) return ""
        if (trimmed.length > 256 ||
            trimmed.startsWith("{") || trimmed.startsWith("[") ||
            trimmed.startsWith("<") || trimmed.startsWith("-----") ||
            trimmed.contains('\n') || trimmed.contains('\r')
        ) {
            return "VPN Connection"
        }
        if (trimmed.length == 1 && !trimmed[0].isLetterOrDigit()) {
            return "VPN Connection"
        }
        return if (trimmed.length > 64) trimmed.substring(0, 64) else trimmed
    }

    private fun load(): ConnectionRegistry {
        val registry = try {
            if (file.exists()) {
                json.decodeFromString<ConnectionRegistry>(file.readText())
            } else {
                ConnectionRegistry()
            }
        } catch (e: Exception) {
            android.util.Log.w("ConnectionRepository",
                "decode failed (${e.javaClass.simpleName}): ${e.message} — starting empty")
            ConnectionRegistry()
        }
        // Defensive wrapper: any heal-phase failure (mutability of
        // deserialized list, NPE on a field that turned out null
        // via legacy data, etc.) MUST NOT propagate out of load().
        // Bug 1 in v0.9.15.9 was the app crashing on startup after
        // a fresh AWG import — load() blew up before the UI could
        // render, leaving the app un-openable. Catch every Throwable
        // (including OOM-shaped errors that may surface during the
        // heal walk) and continue with whatever registry state we
        // managed to deserialize.
        try {
            healRegistry(registry)
        } catch (t: Throwable) {
            android.util.Log.e("ConnectionRepository",
                "load-heal threw ${t.javaClass.simpleName}: ${t.message} — skipping heal", t)
        }
        return registry
    }

    @Suppress("LongMethod", "ComplexMethod")
    private fun healRegistry(registry: ConnectionRegistry) {
        // Heal IPSec server-address corruption from ConfigParser's
        // pre-fix line-based parser (see parseIpSec comment).
        // Affected entries had server_address = "{" because the
        // parser matched the JSON-object-opening line of the .sswan
        // content. Wipe any obviously-bogus server address so the
        // Connect screen's fallback (status.serverEndpoint) doesn't
        // render the corrupt value.
        //
        // v0.9.14.76: persist the healed state to disk immediately.
        // The previous version (v0.9.14.70) did the heal in-memory
        // only and waited for the next user-driven mutation to
        // trigger save(). That left a window where the on-disk
        // file kept the corrupt value and a process restart re-
        // applied the in-memory heal but disk stayed dirty.
        // User-reported as "the '{' is still there after upgrade".
        // Now: any heal triggers an immediate save() so the disk
        // matches the in-memory state from launch onwards.
        var healed = false
        registry.connections.forEach { conn ->
            // Phase 1: assign a stable UUID to every ProtocolConfig
            // that doesn't have one yet. The id field arrived with
            // the multi-config-per-protocol refactor; pre-existing
            // user data has id = "" because the field didn't exist.
            // Without a non-empty id the activeConfigId resolution
            // below can't find anything.
            for (i in conn.protocols.indices) {
                if (conn.protocols[i].id.isEmpty()) {
                    conn.protocols[i] = conn.protocols[i].copy(id = UUID.randomUUID().toString())
                    healed = true
                }
            }

            // Phase 2: reclassify legacy WIREGUARD entries whose
            // configContent contains AmneziaWG obfuscation keys.
            // AWG became its own protocol slot in v0.9.15.x; older
            // saved data has these as WIREGUARD.
            val swaps = mutableListOf<Pair<Int, com.privycs.vpn.data.models.ProtocolConfig>>()
            conn.protocols.forEachIndexed { idx, pc ->
                if (pc.protocol == com.privycs.vpn.data.models.VpnProtocol.WIREGUARD &&
                    com.privycs.vpn.data.models.TunnelVariant.detect(pc.configContent) ==
                        com.privycs.vpn.data.models.TunnelVariant.AMNEZIAWG
                ) {
                    swaps.add(idx to pc.copy(protocol = com.privycs.vpn.data.models.VpnProtocol.AMNEZIAWG))
                    healed = true
                }
            }
            for ((idx, replacement) in swaps) {
                conn.protocols[idx] = replacement
            }
            if (swaps.isNotEmpty() && conn.activeProtocol == com.privycs.vpn.data.models.VpnProtocol.WIREGUARD &&
                swaps.any { it.second.protocol == com.privycs.vpn.data.models.VpnProtocol.AMNEZIAWG }
            ) {
                if (!conn.protocols.any { it.protocol == com.privycs.vpn.data.models.VpnProtocol.WIREGUARD }) {
                    conn.activeProtocol = com.privycs.vpn.data.models.VpnProtocol.AMNEZIAWG
                }
            }

            // Phase 3: dedupe by (protocol, filename) tuple. Drops
            // accidental duplicates left behind by pre-v0.9.15.7's
            // ID-only addOrUpdate which appended on every import.
            // Cross-protocol same-filename entries (e.g. WG-obelix
            // alongside AWG-obelix) are NOT duplicates — they are
            // legitimately different slots and stay. v0.9.15.9's
            // filename-only dedupe was wrong here: it dropped the
            // user's newly-imported AWG slot when a WG slot with
            // the same filename existed, hiding the AWG icon and
            // breaking the AWG handshake path.
            // Keep the FIRST occurrence (preserves id stability
            // for activeConfigId / pool refs); drop the rest.
            val seen = HashSet<Pair<com.privycs.vpn.data.models.VpnProtocol, String>>()
            val deduped = mutableListOf<com.privycs.vpn.data.models.ProtocolConfig>()
            var droppedAny = false
            for (pc in conn.protocols) {
                val key = pc.protocol to pc.filename
                if (pc.filename.isEmpty() || seen.add(key)) {
                    deduped.add(pc)
                } else {
                    droppedAny = true
                }
            }
            if (droppedAny) {
                conn.protocols.clear()
                conn.protocols.addAll(deduped)
                healed = true
            }

            // Phase 4: reconcile activeConfigId. With multi-config
            // support an "active slot" is identified by config-id,
            // not protocol type. If the field is empty (legacy
            // record), points at a config that no longer exists, or
            // points at a config we just deduped away, repoint it
            // at the first config matching the legacy activeProtocol,
            // falling back to the first config in the connection.
            val activeStillValid = conn.activeConfigId.isNotEmpty() &&
                conn.protocols.any { it.id == conn.activeConfigId }
            if (!activeStillValid && conn.protocols.isNotEmpty()) {
                conn.activeConfigId = (
                    conn.protocols.firstOrNull { it.protocol == conn.activeProtocol }
                        ?: conn.protocols.first()
                ).id
                healed = true
            }

            conn.protocols.forEach { pc ->
                if (isCorruptServerAddress(pc.serverAddress)) {
                    pc.serverAddress = ""
                    healed = true
                }
            }
        }
        if (healed) {
            try {
                file.parentFile?.mkdirs()
                file.writeText(json.encodeToString(ConnectionRegistry.serializer(), registry))
            } catch (e: Exception) {
                // Heal-save best-effort. If it fails the in-memory
                // heal still protects the UI for this session;
                // next launch will re-attempt.
                e.printStackTrace()
            }
        }
    }

    /**
     * True when the stored server address looks like a parse artifact
     * rather than a real hostname / IP. Catches:
     *
     *   - "{" / "[" / "<"  (JSON / XML / OpenVPN-cert opening glyph)
     *   - single non-alphanumeric character
     *   - empty after trim
     *
     * Real hostnames + IPs always start with an alphanumeric character
     * and contain at least one dot or colon, so the bar is high enough
     * that no legitimate value matches.
     */
    private fun isCorruptServerAddress(s: String): Boolean {
        val t = s.trim()
        if (t.isEmpty()) return false  // empty is fine, lets live status fill it
        if (t.length == 1 && !t[0].isLetterOrDigit()) return true
        if (t.startsWith("{") || t.startsWith("[") || t.startsWith("<") ||
            t.startsWith("-----")
        ) return true
        return false
    }

    private fun save() {
        try {
            file.parentFile?.mkdirs()
            file.writeText(json.encodeToString(ConnectionRegistry.serializer(), _registry.value))
            // Emit updated state
            _registry.value = _registry.value.copy()
            // Refresh the home-screen widget too — its protocol-pill
            // visibility reads availableProtocols() of the active
            // connection, so adding/removing a protocol from a
            // connection (or activating a different connection) must
            // trigger a widget redraw. Without this, the widget sticks
            // with the previous protocol-set rendering until the next
            // status update from PrivycsVpnService — which only fires
            // when a VPN session is running. Editing connections while
            // disconnected was the case the user reported as
            // "widget doesn't follow connect screen".
            try {
                val intent = android.content.Intent(context, com.privycs.vpn.widget.VpnWidget::class.java).apply {
                    action = android.appwidget.AppWidgetManager.ACTION_APPWIDGET_UPDATE
                    val ids = android.appwidget.AppWidgetManager.getInstance(context).getAppWidgetIds(
                        android.content.ComponentName(context, com.privycs.vpn.widget.VpnWidget::class.java)
                    )
                    putExtra(android.appwidget.AppWidgetManager.EXTRA_APPWIDGET_IDS, ids)
                }
                context.sendBroadcast(intent)
            } catch (_: Throwable) {
                // Widget refresh is purely cosmetic — never let it
                // break the registry save path.
            }
        } catch (e: Exception) {
            // Log but do not crash
            e.printStackTrace()
        }
    }

    /**
     * Force re-emit current state to observers.
     * Useful after in-place mutations of connection list items.
     */
    fun notifyChanged() {
        _registry.value = _registry.value.copy()
    }
}
