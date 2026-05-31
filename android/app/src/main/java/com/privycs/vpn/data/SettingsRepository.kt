package com.privycs.vpn.data

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.core.handlers.ReplaceFileCorruptionHandler
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.emptyPreferences
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import com.privycs.vpn.data.models.AppSettings
import com.privycs.vpn.data.models.AppTheme
import com.privycs.vpn.data.models.ConnectOnDemandSettings
import com.privycs.vpn.data.models.VpnProtocol
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.catch
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import java.io.IOException

private val Context.dataStore: DataStore<Preferences> by preferencesDataStore(
    name = "settings",
    // v0.9.15.75: a truncated/corrupt settings file (abrupt power
    // loss or an OEM task-kill mid-edit) would otherwise throw
    // CorruptionException on every read — crashing the app at launch
    // and on every BootReceiver/widget/tile getSettingsBlocking call.
    // Replace it with empty prefs → all defaults via the map below.
    corruptionHandler = ReplaceFileCorruptionHandler { emptyPreferences() },
)

class SettingsRepository(private val context: Context) {

    private object Keys {
        val ACTIVE_PROTOCOL = stringPreferencesKey("active_protocol")
        val KILL_SWITCH = booleanPreferencesKey("kill_switch_enabled")
        val AUTO_CONNECT = booleanPreferencesKey("auto_connect_on_start")
        val THEME = stringPreferencesKey("theme")
        val DNS_OVERRIDE = stringPreferencesKey("dns_override")
        val GATEWAY_URL = stringPreferencesKey("gateway_url")
        val API_KEY = stringPreferencesKey("api_key")
        val COD_ENABLED = booleanPreferencesKey("cod_enabled")
        val COD_TRIGGER = stringPreferencesKey("cod_trigger")
        val COD_SSID_MODE = stringPreferencesKey("cod_ssid_mode")
        val COD_SSID_LIST = stringPreferencesKey("cod_ssid_list")
        val FIRST_LAUNCH_COMPLETED = booleanPreferencesKey("first_launch_completed")
        val TUNNEL_HEALTH_MODE = stringPreferencesKey("tunnel_health_mode")
        val TUNNEL_HEALTH_TARGET = stringPreferencesKey("tunnel_health_target")
        val KEEP_MONITOR_ALIVE = booleanPreferencesKey("keep_monitor_alive")
        // v0.9.15.72 — user-configurable protocol failover order,
        // CSV of lowercase enum names (e.g. "amneziawg,wireguard,
        // openvpn,ipsec"). Empty / missing key falls back to the
        // AppSettings default in the data class.
        val PROTOCOL_FAILOVER_ORDER = stringPreferencesKey("protocol_failover_order")
        // v1.0.5: master on/off for the NetworkRules engine. Matches
        // Desktop's `network_rules_enabled` field. Defaults true.
        val NETWORK_RULES_ENABLED = booleanPreferencesKey("network_rules_enabled")
        // v1.0.7: anonymous crash-report opt-in. Default false.
        // Matches Desktop's `crash_reports_enabled`. Bound to the
        // Settings → "Anonymous diagnostics" toggle. CrashReporter
        // observes settingsFlow + (re)inits on every transition.
        val CRASH_REPORTS_ENABLED = booleanPreferencesKey("crash_reports_enabled")
    }

    val settingsFlow: Flow<AppSettings> = context.dataStore.data
        .catch { e ->
            // corruptionHandler above replaces a corrupt file; this
            // catches other transient read failures (disk I/O) so a
            // settings read never crashes a collector. Empty prefs →
            // all defaults via the map below.
            if (e is IOException) emit(emptyPreferences()) else throw e
        }
        .map { prefs ->
        AppSettings(
            activeProtocol = prefs[Keys.ACTIVE_PROTOCOL]?.let { VpnProtocol.fromString(it) }
                ?: VpnProtocol.WIREGUARD,
            killSwitchEnabled = prefs[Keys.KILL_SWITCH] ?: false,
            autoConnectOnStart = prefs[Keys.AUTO_CONNECT] ?: false,
            theme = prefs[Keys.THEME]?.let { parseTheme(it) } ?: AppTheme.SYSTEM,
            dnsOverride = prefs[Keys.DNS_OVERRIDE] ?: "",
            gatewayUrl = prefs[Keys.GATEWAY_URL] ?: "",
            apiKey = decryptApiKey(prefs[Keys.API_KEY]),
            connectOnDemand = ConnectOnDemandSettings(
                enabled = prefs[Keys.COD_ENABLED] ?: false,
                trigger = prefs[Keys.COD_TRIGGER] ?: "wifi_mobile",
                ssidMode = prefs[Keys.COD_SSID_MODE] ?: "all",
                ssidList = (prefs[Keys.COD_SSID_LIST] ?: "").let { raw ->
                    if (raw.isBlank()) emptyList() else raw.split(",").map { it.trim() }
                }
            ),
            firstLaunchCompleted = prefs[Keys.FIRST_LAUNCH_COMPLETED] ?: false,
            tunnelHealthMode = prefs[Keys.TUNNEL_HEALTH_MODE] ?: "auto",
            tunnelHealthTarget = prefs[Keys.TUNNEL_HEALTH_TARGET] ?: "",
            keepMonitorAlive = prefs[Keys.KEEP_MONITOR_ALIVE] ?: false,
            networkRulesEnabled = prefs[Keys.NETWORK_RULES_ENABLED] ?: true,
            crashReportsEnabled = prefs[Keys.CRASH_REPORTS_ENABLED] ?: false,
            protocolFailoverOrder = (prefs[Keys.PROTOCOL_FAILOVER_ORDER] ?: "")
                .split(",")
                .mapNotNull { VpnProtocol.fromString(it.trim()) }
                .let { parsed ->
                    if (parsed.isEmpty()) {
                        listOf(
                            VpnProtocol.AMNEZIAWG,
                            VpnProtocol.WIREGUARD,
                            VpnProtocol.OPENVPN,
                            VpnProtocol.IPSEC,
                        )
                    } else {
                        // Append any protocol the user hasn't placed
                        // explicitly so a partial list (e.g. after a
                        // future protocol addition) still produces a
                        // total order in enum sequence.
                        val seen = parsed.toSet()
                        parsed + listOf(
                            VpnProtocol.AMNEZIAWG,
                            VpnProtocol.WIREGUARD,
                            VpnProtocol.OPENVPN,
                            VpnProtocol.IPSEC,
                        ).filter { it !in seen }
                    }
                }
        )
    }

    fun defaultSettings(): AppSettings = AppSettings()

    // v0.9.15.74 (B-5): in-memory cache of the latest AppSettings so
    // getSettingsBlocking() is a non-blocking read once the cache has
    // warmed (within milliseconds of construction). Eliminates the
    // runBlocking DataStore read from ~15 call sites — several on the
    // Compose UI thread (connection picker, QS-Tile disconnect) where
    // it was an ANR risk. The collector keeps the cache current on
    // every persisted change, so reads are never stale.
    @Volatile
    private var cachedSettings: AppSettings? = null
    private val cacheScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    init {
        cacheScope.launch {
            settingsFlow.collect { cachedSettings = it }
        }
    }

    /**
     * Synchronous read for boot receiver and service startup.
     * Returns the in-memory cache once warm (the common case — every
     * call after the first few ms of process life); only a call that
     * lands before the cache warms falls back to a one-shot blocking
     * DataStore read. Prefer settingsFlow for UI observation.
     */
    fun getSettingsBlocking(): AppSettings =
        cachedSettings ?: runBlocking { settingsFlow.first() }.also { cachedSettings = it }

    // v0.9.15.74 (audit item B): the gateway API key is encrypted at
    // rest via the Keystore-backed SecretCrypto. A legacy plaintext
    // value decrypt-fails and falls through to the raw string, which
    // is re-stored as ciphertext on the next write (transparent
    // migration). Empty stays empty (no ciphertext for a blank key).
    private fun encryptApiKey(plain: String): String =
        if (plain.isBlank()) "" else com.privycs.vpn.util.SecretCrypto.encrypt(plain)

    private fun decryptApiKey(stored: String?): String {
        if (stored.isNullOrBlank()) return ""
        return try {
            com.privycs.vpn.util.SecretCrypto.decrypt(stored)
        } catch (e: Exception) {
            // Legacy plaintext value, or (rare) Keystore key lost.
            stored
        }
    }

    suspend fun updateSettings(settings: AppSettings) {
        context.dataStore.edit { prefs ->
            prefs[Keys.ACTIVE_PROTOCOL] = settings.activeProtocol.name.lowercase()
            prefs[Keys.KILL_SWITCH] = settings.killSwitchEnabled
            prefs[Keys.AUTO_CONNECT] = settings.autoConnectOnStart
            prefs[Keys.THEME] = settings.theme.name.lowercase()
            prefs[Keys.DNS_OVERRIDE] = settings.dnsOverride
            prefs[Keys.GATEWAY_URL] = settings.gatewayUrl
            prefs[Keys.API_KEY] = encryptApiKey(settings.apiKey)
            prefs[Keys.COD_ENABLED] = settings.connectOnDemand.enabled
            prefs[Keys.COD_TRIGGER] = settings.connectOnDemand.trigger
            prefs[Keys.COD_SSID_MODE] = settings.connectOnDemand.ssidMode
            prefs[Keys.COD_SSID_LIST] = settings.connectOnDemand.ssidList.joinToString(",")
            prefs[Keys.FIRST_LAUNCH_COMPLETED] = settings.firstLaunchCompleted
            prefs[Keys.TUNNEL_HEALTH_MODE] = settings.tunnelHealthMode
            prefs[Keys.TUNNEL_HEALTH_TARGET] = settings.tunnelHealthTarget
            prefs[Keys.KEEP_MONITOR_ALIVE] = settings.keepMonitorAlive
            prefs[Keys.PROTOCOL_FAILOVER_ORDER] =
                settings.protocolFailoverOrder.joinToString(",") { it.name.lowercase() }
            prefs[Keys.NETWORK_RULES_ENABLED] = settings.networkRulesEnabled
            prefs[Keys.CRASH_REPORTS_ENABLED] = settings.crashReportsEnabled
        }
    }

    /**
     * Targeted setter for the foreground-keepalive toggle (v0.9.14.75).
     * Used by the in-notification "Stop monitoring" action and any
     * other code path that wants to flip the bit without rewriting
     * the whole AppSettings document.
     */
    suspend fun setKeepMonitorAlive(enabled: Boolean) {
        context.dataStore.edit { prefs ->
            prefs[Keys.KEEP_MONITOR_ALIVE] = enabled
        }
    }

    suspend fun updateGatewayConfig(url: String, apiKey: String) {
        context.dataStore.edit { prefs ->
            prefs[Keys.GATEWAY_URL] = url
            prefs[Keys.API_KEY] = encryptApiKey(apiKey)
        }
    }

    suspend fun updateTheme(theme: AppTheme) {
        context.dataStore.edit { prefs ->
            prefs[Keys.THEME] = theme.name.lowercase()
        }
    }

    suspend fun updateKillSwitch(enabled: Boolean) {
        context.dataStore.edit { prefs ->
            prefs[Keys.KILL_SWITCH] = enabled
        }
        if (!enabled) {
            // Tear down the sinkhole immediately on disable. Without
            // this the block-all tun fd would persist until the next
            // connect/disconnect cycle.
            com.privycs.vpn.util.KillSwitchManager.disarm()
            return
        }
        // Enabled: decide between arm, force-sinkhole, or ignore
        // based on current tunnel + active-connection state.
        val manager = com.privycs.vpn.service.VpnServiceManager.getInstance(context)
        if (manager.isConnected) {
            // Tunnel up: arm the safety net so an unexpected drop
            // engages the sinkhole.
            if (!com.privycs.vpn.util.KillSwitchManager.isArmed()) {
                com.privycs.vpn.util.KillSwitchManager.arm()
            }
            return
        }
        // Tunnel down while user enables KS. Industry-standard "hardcore"
        // kill switch: if there's a configured connection we should be
        // protecting, block traffic immediately - otherwise the user's
        // intent ("block unprotected traffic NOW") is ignored until they
        // actually connect.
        val hasActiveConnection = com.privycs.vpn.PrivycsApp.instance
            .connectionRepository.getActive() != null
        if (hasActiveConnection) {
            com.privycs.vpn.util.KillSwitchManager.forceSinkhole(
                "KS enabled while disconnected with active connection configured",
            )
            // Setting state=SINKHOLE alone is not enough - we also need
            // a live VpnService instance to actually hold the block-all
            // tun fd. If the service was destroyed by a prior manual
            // disconnect (handleDisconnect calls stopSelf), the
            // KillSwitchManager state-flow observer that establishes the
            // sinkhole is gone too. Fire an explicit start with
            // ACTION_ENGAGE_SINKHOLE so the service comes back up,
            // sees state=SINKHOLE in onCreate, and calls
            // enterSinkholeMode synchronously. Without this, the UI
            // shows "Kill Switch Active" but no traffic is actually
            // blocked - the bug observed when user manually disconnects
            // then enables KS.
            try {
                val intent = android.content.Intent(
                    context,
                    com.privycs.vpn.service.PrivycsVpnService::class.java,
                ).setAction(com.privycs.vpn.service.PrivycsVpnService.ACTION_ENGAGE_SINKHOLE)
                androidx.core.content.ContextCompat.startForegroundService(context, intent)
            } catch (e: Exception) {
                android.util.Log.w(
                    "SettingsRepository",
                    "Failed to start VpnService for sinkhole: ${e.message}",
                )
            }
        } else {
            android.util.Log.d(
                "SettingsRepository",
                "Kill Switch enabled but no active connection: staying IDLE",
            )
        }
    }

    suspend fun updateAutoConnect(enabled: Boolean) {
        context.dataStore.edit { prefs ->
            prefs[Keys.AUTO_CONNECT] = enabled
        }
    }

    suspend fun updateConnectOnDemand(cod: ConnectOnDemandSettings) {
        context.dataStore.edit { prefs ->
            prefs[Keys.COD_ENABLED] = cod.enabled
            prefs[Keys.COD_TRIGGER] = cod.trigger
            prefs[Keys.COD_SSID_MODE] = cod.ssidMode
            prefs[Keys.COD_SSID_LIST] = cod.ssidList.joinToString(",")
        }
    }

    /**
     * Mark the first-launch flow complete. Called by MainActivity
     * after the post-install Location-permission rationale + request
     * has been shown exactly once. Subsequent app starts read the
     * flag and skip the dialog.
     */
    suspend fun markFirstLaunchCompleted() {
        context.dataStore.edit { prefs ->
            prefs[Keys.FIRST_LAUNCH_COMPLETED] = true
        }
    }

    private fun parseTheme(value: String): AppTheme = when (value.lowercase()) {
        "dark" -> AppTheme.DARK
        "light" -> AppTheme.LIGHT
        else -> AppTheme.SYSTEM
    }

}
