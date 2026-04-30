package com.privycs.vpn.data

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import com.privycs.vpn.data.models.NetworkRule
import com.privycs.vpn.data.models.RuleResolution
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json

// Separate DataStore for network rules so they don't share the
// settings keyspace. Single-name preferencesDataStore creates the
// file lazily on first access.
private val Context.networkRulesStore: DataStore<Preferences> by preferencesDataStore(
    name = "network_rules"
)

/**
 * Persists the user's network rule list. Backed by the same
 * DataStore Preferences instance as SettingsRepository (no
 * separate file for one rarely-mutated list). JSON-serialised
 * because rules are nested objects with enums.
 *
 * Rule semantics + matching are defined on NetworkRule.matches.
 * This repo just owns CRUD and ordering; the rules engine in
 * NetworkMonitor consumes the list via `resolve()`.
 *
 * Rule ordering: stored as a list, position is priority.
 * resolve() walks first-to-last; first matching rule wins.
 * Move-up/move-down operations are O(n) list mutations.
 */
class NetworkRulesRepository(private val context: Context) {

    private object Keys {
        val RULES_JSON = stringPreferencesKey("network_rules_json")
    }

    private val json = Json { ignoreUnknownKeys = true; encodeDefaults = true }
    private val serializer = ListSerializer(NetworkRule.serializer())

    private val _rules = MutableStateFlow<List<NetworkRule>>(loadBlocking())
    val rules = _rules.asStateFlow()

    val rulesFlow: Flow<List<NetworkRule>> = _rules

    private fun loadBlocking(): List<NetworkRule> = runBlocking {
        try {
            val raw = context.networkRulesStore.data.first()[Keys.RULES_JSON].orEmpty()
            if (raw.isBlank()) emptyList() else json.decodeFromString(serializer, raw)
        } catch (e: Exception) {
            android.util.Log.w("NetworkRulesRepository", "load failed: ${e.message}")
            emptyList()
        }
    }

    suspend fun save(list: List<NetworkRule>) {
        val ordered = list.mapIndexed { i, r -> r.copy(priority = i) }
        _rules.value = ordered
        val raw = json.encodeToString(serializer, ordered)
        context.networkRulesStore.edit { prefs -> prefs[Keys.RULES_JSON] = raw }
    }

    suspend fun add(rule: NetworkRule) {
        save(_rules.value + rule.copy(priority = _rules.value.size))
    }

    suspend fun update(rule: NetworkRule) {
        save(_rules.value.map { if (it.id == rule.id) rule else it })
    }

    suspend fun delete(ruleId: String) {
        save(_rules.value.filterNot { it.id == ruleId })
    }

    suspend fun reorder(orderedIds: List<String>) {
        val byId = _rules.value.associateBy { it.id }
        val ordered = orderedIds.mapNotNull { byId[it] }
        save(ordered)
    }

    /**
     * Walk the rule list in priority order and return the first
     * matching rule's resolution. Empty list or no match = fall
     * through to legacy COD logic via RuleResolution.NoMatch.
     *
     * Pure logic - no side effects, no I/O. Safe to call from
     * NetworkMonitor's evaluateCurrentNetwork on every tick.
     */
    fun resolve(networkType: String, ssid: String, bssid: String): RuleResolution {
        val list = _rules.value
        if (list.isEmpty()) return RuleResolution.NoMatch
        val match = list.firstOrNull { it.matches(networkType, ssid, bssid) }
            ?: return RuleResolution.NoMatch
        return when (match.action) {
            com.privycs.vpn.data.models.RuleAction.NO_VPN -> RuleResolution.NoVpn
            com.privycs.vpn.data.models.RuleAction.POOL -> RuleResolution.Pool(match.targetId)
            com.privycs.vpn.data.models.RuleAction.CONNECTION ->
                RuleResolution.Connection(match.targetId)
        }
    }
}
