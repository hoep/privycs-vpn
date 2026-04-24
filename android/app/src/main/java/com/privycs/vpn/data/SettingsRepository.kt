package com.privycs.vpn.data

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import com.privycs.vpn.data.models.AppSettings
import com.privycs.vpn.data.models.AppTheme
import com.privycs.vpn.data.models.ConnectOnDemandSettings
import com.privycs.vpn.data.models.RoutingMode
import com.privycs.vpn.data.models.VpnProtocol
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.runBlocking

private val Context.dataStore: DataStore<Preferences> by preferencesDataStore(name = "settings")

class SettingsRepository(private val context: Context) {

    private object Keys {
        val ACTIVE_PROTOCOL = stringPreferencesKey("active_protocol")
        val KILL_SWITCH = booleanPreferencesKey("kill_switch_enabled")
        val AUTO_CONNECT = booleanPreferencesKey("auto_connect_on_start")
        val ALWAYS_ON = booleanPreferencesKey("always_on")
        val THEME = stringPreferencesKey("theme")
        val DNS_OVERRIDE = stringPreferencesKey("dns_override")
        val ROUTING_MODE = stringPreferencesKey("routing_mode")
        val GATEWAY_URL = stringPreferencesKey("gateway_url")
        val API_KEY = stringPreferencesKey("api_key")
        val COD_ENABLED = booleanPreferencesKey("cod_enabled")
        val COD_TRIGGER = stringPreferencesKey("cod_trigger")
        val COD_SSID_MODE = stringPreferencesKey("cod_ssid_mode")
        val COD_SSID_LIST = stringPreferencesKey("cod_ssid_list")
    }

    val settingsFlow: Flow<AppSettings> = context.dataStore.data.map { prefs ->
        AppSettings(
            activeProtocol = prefs[Keys.ACTIVE_PROTOCOL]?.let { VpnProtocol.fromString(it) }
                ?: VpnProtocol.WIREGUARD,
            killSwitchEnabled = prefs[Keys.KILL_SWITCH] ?: false,
            autoConnectOnStart = prefs[Keys.AUTO_CONNECT] ?: false,
            alwaysOn = prefs[Keys.ALWAYS_ON] ?: false,
            theme = prefs[Keys.THEME]?.let { parseTheme(it) } ?: AppTheme.SYSTEM,
            dnsOverride = prefs[Keys.DNS_OVERRIDE] ?: "",
            routingMode = prefs[Keys.ROUTING_MODE]?.let { parseRoutingMode(it) } ?: RoutingMode.FULL,
            gatewayUrl = prefs[Keys.GATEWAY_URL] ?: "",
            apiKey = prefs[Keys.API_KEY] ?: "",
            connectOnDemand = ConnectOnDemandSettings(
                enabled = prefs[Keys.COD_ENABLED] ?: false,
                trigger = prefs[Keys.COD_TRIGGER] ?: "wifi_mobile",
                ssidMode = prefs[Keys.COD_SSID_MODE] ?: "all",
                ssidList = (prefs[Keys.COD_SSID_LIST] ?: "").let { raw ->
                    if (raw.isBlank()) emptyList() else raw.split(",").map { it.trim() }
                }
            )
        )
    }

    fun defaultSettings(): AppSettings = AppSettings()

    /**
     * Synchronous read for boot receiver and service startup.
     * Prefer settingsFlow for UI observation.
     */
    fun getSettingsBlocking(): AppSettings = runBlocking {
        settingsFlow.first()
    }

    suspend fun updateSettings(settings: AppSettings) {
        context.dataStore.edit { prefs ->
            prefs[Keys.ACTIVE_PROTOCOL] = settings.activeProtocol.name.lowercase()
            prefs[Keys.KILL_SWITCH] = settings.killSwitchEnabled
            prefs[Keys.AUTO_CONNECT] = settings.autoConnectOnStart
            prefs[Keys.ALWAYS_ON] = settings.alwaysOn
            prefs[Keys.THEME] = settings.theme.name.lowercase()
            prefs[Keys.DNS_OVERRIDE] = settings.dnsOverride
            prefs[Keys.ROUTING_MODE] = settings.routingMode.name.lowercase()
            prefs[Keys.GATEWAY_URL] = settings.gatewayUrl
            prefs[Keys.API_KEY] = settings.apiKey
            prefs[Keys.COD_ENABLED] = settings.connectOnDemand.enabled
            prefs[Keys.COD_TRIGGER] = settings.connectOnDemand.trigger
            prefs[Keys.COD_SSID_MODE] = settings.connectOnDemand.ssidMode
            prefs[Keys.COD_SSID_LIST] = settings.connectOnDemand.ssidList.joinToString(",")
        }
    }

    suspend fun updateGatewayConfig(url: String, apiKey: String) {
        context.dataStore.edit { prefs ->
            prefs[Keys.GATEWAY_URL] = url
            prefs[Keys.API_KEY] = apiKey
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
        // If the user toggles Kill Switch off while the sinkhole is
        // active, tear it down immediately. Without this the block-
        // all tun fd would persist until the next connect/disconnect
        // cycle and the user's "let me have my traffic back" intent
        // would look broken.
        if (!enabled) {
            com.privycs.vpn.util.KillSwitchManager.disarm()
            return
        }
        // Toggled ON while already connected: arm now. The
        // connected-transition path in VpnServiceManager wouldn't
        // fire in this case (status.connected isn't changing), and
        // waiting for the next poll-tick defensive-arm is a
        // 0-2s UX lag the user shouldn't have to see.
        val manager = com.privycs.vpn.service.VpnServiceManager.getInstance(context)
        if (manager.isConnected && !com.privycs.vpn.util.KillSwitchManager.isArmed()) {
            com.privycs.vpn.util.KillSwitchManager.arm()
        }
    }

    suspend fun updateAutoConnect(enabled: Boolean) {
        context.dataStore.edit { prefs ->
            prefs[Keys.AUTO_CONNECT] = enabled
        }
    }

    suspend fun updateAlwaysOn(enabled: Boolean) {
        context.dataStore.edit { prefs ->
            prefs[Keys.ALWAYS_ON] = enabled
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

    private fun parseTheme(value: String): AppTheme = when (value.lowercase()) {
        "dark" -> AppTheme.DARK
        "light" -> AppTheme.LIGHT
        else -> AppTheme.SYSTEM
    }

    private fun parseRoutingMode(value: String): RoutingMode = when (value.lowercase()) {
        "split" -> RoutingMode.SPLIT
        else -> RoutingMode.FULL
    }
}
