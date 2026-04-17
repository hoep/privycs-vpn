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

        if (conn == null) {
            conn = VpnConnection(
                id = UUID.randomUUID().toString(),
                name = name,
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

        if (name.isNotBlank()) {
            conn.name = name
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
     * Rename an existing connection. Returns true if the connection was found
     * and the new name is non-blank, false otherwise. Empty or whitespace-only
     * names are rejected to avoid unreadable rows in the connections list.
     */
    fun rename(connectionId: String, newName: String): Boolean {
        val trimmed = newName.trim()
        if (trimmed.isEmpty()) return false
        val conn = getById(connectionId) ?: return false
        conn.name = trimmed
        save()
        return true
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
