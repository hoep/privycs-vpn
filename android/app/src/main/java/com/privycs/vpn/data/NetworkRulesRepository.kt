package com.privycs.vpn.data

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.core.handlers.ReplaceFileCorruptionHandler
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.emptyPreferences
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import com.privycs.vpn.data.models.NetworkRule
import com.privycs.vpn.data.models.RuleResolution
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json

// Separate DataStore for network rules so they don't share the
// settings keyspace. Single-name preferencesDataStore creates the
// file lazily on first access.
private val Context.networkRulesStore: DataStore<Preferences> by preferencesDataStore(
    name = "network_rules",
    // v0.9.15.75: replace a corrupt rules file rather than throw
    // CorruptionException (loadRules() also catches — belt + braces).
    corruptionHandler = ReplaceFileCorruptionHandler { emptyPreferences() },
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

    // v0.9.15.74 (B-5): start empty and load asynchronously. The
    // constructor runs on the main thread (PrivycsApp.onCreate); a
    // runBlocking DataStore read at field-init stalled cold start.
    // Consumers observe `rules` / `rulesFlow`, so they pick up the
    // persisted list as soon as the async load completes.
    private val _rules = MutableStateFlow<List<NetworkRule>>(emptyList())
    val rules = _rules.asStateFlow()

    val rulesFlow: Flow<List<NetworkRule>> = _rules

    private val loadScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    init {
        loadScope.launch { _rules.value = loadRules() }
    }

    private suspend fun loadRules(): List<NetworkRule> {
        return try {
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
