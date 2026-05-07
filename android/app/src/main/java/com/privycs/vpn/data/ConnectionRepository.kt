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

        // Last-line defense against names that leaked through the upstream
        // path (file-picker DISPLAY_NAME set to the raw JSON/YAML content,
        // stale user-input, backup-restore of a previously-broken entry,
        // gateway returning an empty peer_name). ConfigParser
        // .deriveConnectionName already guards the filename-derived branch
        // but callers can pass the raw user-typed string here too. Anything
        // that looks like config content or a single non-alphanumeric
        // glyph gets replaced with a safe default so the connection list
        // never renders "{" as a connection name again.
        val cleanName = sanitizeConnectionName(name)

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
                activeProtocol = protocolConfig.protocol,
                createdAt = Instant.now().toString()
            )
            registry.connections.add(conn)
        }

        // Replace existing protocol config or add new one
        val existingIndex = conn.protocols.indexOfFirst { it.protocol == protocolConfig.protocol }
        if (existingIndex >= 0) {
            conn.protocols[existingIndex] = protocolConfig
        } else {
            conn.protocols.add(protocolConfig)
        }

        // Set the connection name only when the connection has none
        // yet. Subsequent protocol-additions to the same connection
        // PRESERVE the user's chosen name — including any rename the
        // user did. Without this guard, dropping a newly-pulled .ovpn
        // / .sswan / .conf into an existing connection silently
        // re-baselined the name to the new file's name, so every time
        // the user pulled an updated IPSec profile from the gateway,
        // their custom connection label got overwritten.
        if (conn.name.isBlank() && cleanName.isNotBlank()) {
            conn.name = cleanName
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

    fun setActiveProtocol(connectionId: String, protocol: VpnProtocol): Boolean {
        val conn = getById(connectionId) ?: return false
        if (!conn.hasProtocol(protocol)) return false
        conn.activeProtocol = protocol
        save()
        return true
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
        conn.protocols.removeAll { it.protocol == protocol }

        if (conn.activeProtocol == protocol && conn.protocols.isNotEmpty()) {
            conn.activeProtocol = conn.protocols.first().protocol
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
        val idx = conn.protocols.indexOfFirst { it.protocol == protocol }
        if (idx < 0) return
        if (conn.protocols[idx].localAddress == addr) return
        conn.protocols[idx].localAddress = addr
        save()
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
        return try {
            if (file.exists()) {
                json.decodeFromString<ConnectionRegistry>(file.readText())
            } else {
                ConnectionRegistry()
            }
        } catch (e: Exception) {
            ConnectionRegistry()
        }
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
