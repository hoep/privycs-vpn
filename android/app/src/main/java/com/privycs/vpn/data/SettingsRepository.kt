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
            apiKey = prefs[Keys.API_KEY] ?: ""
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
