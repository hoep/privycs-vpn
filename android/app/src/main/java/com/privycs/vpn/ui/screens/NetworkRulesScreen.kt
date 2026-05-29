package com.privycs.vpn.ui.screens

import android.content.Context
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Edit
import androidx.compose.foundation.background
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.draw.clip
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
import com.privycs.vpn.data.models.NetworkRule
import com.privycs.vpn.data.models.RuleAction
import com.privycs.vpn.data.models.RuleMatchType
import com.privycs.vpn.util.proGateAllowed
import kotlinx.coroutines.launch
import java.util.UUID

/**
 * On-Demand & Network Rules screen — the single source of truth for
 * auto-tunnel behaviour. The rule list is checked top to bottom on
 * every network change; the first matching rule wins. When no rule
 * matches, the engine takes no action (see the pinned fallback card).
 * The legacy "simple Connect-on-Demand" was converted into rules by a
 * one-time migration (NetworkRulesRepository.migrateLegacyCod).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun NetworkRulesScreen(onBack: () -> Unit) {
    val app = PrivycsApp.instance
    val repo = remember { app.networkRulesRepository }
    val settingsRepo = remember { app.settingsRepository }
    val rules by repo.rules.collectAsState()
    // v1.0.5 — master on/off for the rules engine; the prominent toggle
    // card at the top of this screen edits this flag. When false, the
    // NetworkMonitor short-circuits its evaluation and no rule can fire.
    val settings by settingsRepo.settingsFlow.collectAsState(initial = settingsRepo.defaultSettings())
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var editing by remember { mutableStateOf<NetworkRule?>(null) }
    var showCreate by remember { mutableStateOf(false) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        stringResource(R.string.netrules_screen_title),
                        fontWeight = FontWeight.SemiBold,
                    )
                },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(
                            Icons.AutoMirrored.Filled.ArrowBack,
                            contentDescription = stringResource(R.string.netrules_back),
                        )
                    }
                },
            )
        },
        floatingActionButton = {
            // Pro gate 3 — adding a network rule (Connect-on-Demand).
            FloatingActionButton(onClick = { if (proGateAllowed(context)) showCreate = true }) {
                Icon(
                    Icons.Filled.Add,
                    contentDescription = stringResource(R.string.netrules_add_rule),
                )
            }
        },
    ) { padding ->
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .padding(horizontal = 16.dp),
        ) {
            item {
                Spacer(Modifier.height(12.dp))
                // v1.0.5: master toggle — top of screen, primary-colored
                // card so it's the first thing the user sees. When OFF,
                // NetworkMonitor.runEvaluation() returns immediately so
                // no rule fires; when ON, the rule list below is
                // evaluated top-to-bottom on every network change.
                MasterToggleCard(
                    enabled = settings.networkRulesEnabled,
                    onToggle = { newValue ->
                        scope.launch {
                            settingsRepo.updateSettings(
                                settings.copy(networkRulesEnabled = newValue),
                            )
                        }
                    },
                )
                Spacer(Modifier.height(16.dp))
            }

            if (rules.isEmpty()) {
                item { EmptyRulesCard() }
            } else {
                item {
                    Text(
                        stringResource(R.string.netrules_rules_header),
                        style = MaterialTheme.typography.labelSmall,
                        fontWeight = FontWeight.SemiBold,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    Spacer(Modifier.height(8.dp))
                }
                items(rules, key = { it.id }) { rule ->
                    RuleRow(
                        rule = rule,
                        onEdit = { editing = rule },
                        onDelete = { scope.launch { repo.delete(rule.id) } },
                        onMoveUp = {
                            val idx = rules.indexOf(rule)
                            if (idx > 0) {
                                val ids = rules.map { it.id }.toMutableList()
                                ids[idx] = ids[idx - 1].also { ids[idx - 1] = ids[idx] }
                                scope.launch { repo.reorder(ids) }
                            }
                        },
                        onMoveDown = {
                            val idx = rules.indexOf(rule)
                            if (idx < rules.size - 1) {
                                val ids = rules.map { it.id }.toMutableList()
                                ids[idx] = ids[idx + 1].also { ids[idx + 1] = ids[idx] }
                                scope.launch { repo.reorder(ids) }
                            }
                        },
                        onToggle = {
                            scope.launch {
                                repo.update(rule.copy(enabled = !rule.enabled))
                            }
                        },
                    )
                    Spacer(Modifier.height(8.dp))
                }
            }

            item {
                Spacer(Modifier.height(8.dp))
                LiveEvalCard()
                Spacer(Modifier.height(24.dp))
            }
        }

        if (showCreate) {
            RuleEditDialog(
                initial = null,
                onDismiss = { showCreate = false },
                onSave = { newRule ->
                    scope.launch {
                        repo.add(newRule.copy(id = UUID.randomUUID().toString()))
                        showCreate = false
                    }
                },
            )
        }
        editing?.let { current ->
            RuleEditDialog(
                initial = current,
                onDismiss = { editing = null },
                onSave = { updated ->
                    scope.launch {
                        repo.update(updated)
                        editing = null
                    }
                },
            )
        }
    }
}

/**
 * Master on/off toggle for the NetworkRules engine — pinned at the top
 * of the screen, primary-colored for visual prominence. When this is
 * off, no rule can fire regardless of the rule list below; the user
 * can still manually Connect/Disconnect from the Connect screen.
 * Mirrors Desktop's master toggle in NetworkRulesView.vue.
 */
@Composable
private fun MasterToggleCard(enabled: Boolean, onToggle: (Boolean) -> Unit) {
    Card(
        colors = CardDefaults.cardColors(
            containerColor = if (enabled)
                MaterialTheme.colorScheme.primaryContainer
            else
                MaterialTheme.colorScheme.surfaceVariant,
        ),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Row(
            modifier = Modifier.padding(16.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    stringResource(R.string.netrules_master_title),
                    style = MaterialTheme.typography.titleSmall,
                    fontWeight = FontWeight.SemiBold,
                    color = if (enabled)
                        MaterialTheme.colorScheme.onPrimaryContainer
                    else
                        MaterialTheme.colorScheme.onSurface,
                )
                Spacer(Modifier.height(2.dp))
                Text(
                    stringResource(
                        if (enabled) R.string.netrules_master_subtitle_on
                        else R.string.netrules_master_subtitle_off,
                    ),
                    style = MaterialTheme.typography.bodySmall,
                    // v1.0.5.1: pair subtitle colour to the card's
                    // background. Pre-fix used onSurfaceVariant which
                    // is light-gray-on-green when the card sits on
                    // primaryContainer → unreadable. Material3
                    // guideline: secondary text on a colored container
                    // = the "on<container>" colour with ~75 % alpha.
                    color = if (enabled)
                        MaterialTheme.colorScheme.onPrimaryContainer.copy(alpha = 0.75f)
                    else
                        MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Spacer(Modifier.width(12.dp))
            Switch(
                checked = enabled,
                onCheckedChange = onToggle,
            )
        }
    }
}

/**
 * Precedence explainer pinned at the top — the visible-precedence
 * half of Option A1. Before unification, neither the Settings COD
 * section nor this screen told the user that rules win over the
 * simple trigger.
 */
@Composable
private fun PrecedenceHeaderCard() {
    Card(
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.4f),
        ),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            Text(
                stringResource(R.string.netrules_precedence_title),
                style = MaterialTheme.typography.titleSmall,
                color = MaterialTheme.colorScheme.primary,
            )
            Spacer(Modifier.height(6.dp))
            Text(
                stringResource(R.string.netrules_precedence_body),
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

@Composable
private fun EmptyRulesCard() {
    Card(
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surfaceVariant,
        ),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            Text(
                stringResource(R.string.netrules_empty_title),
                style = MaterialTheme.typography.titleSmall,
            )
            Spacer(Modifier.height(4.dp))
            Text(
                stringResource(R.string.netrules_empty_body),
                style = MaterialTheme.typography.bodySmall,
            )
        }
    }
}

/**
 * v1.0.5.23: live evaluation result pinned at the bottom of the
 * NetworkRules screen — replaces the previous static
 * "DefaultBehaviourCard". Reactively reflects the engine's current
 * decision against the current network, in the user's own terms:
 *
 *   "Connected to WiFi 'Hoep@Home' (Auto-tunnel = Off) → Manual control"
 *   "Connected to Mobile (Auto-tunnel = On, no matching rule) → No action"
 *   "Connected to WiFi 'Home' (Auto-tunnel = On, except-rule) → VPN Off"
 *
 * Subscribes to NetworkMonitor.networkState (network type + SSID +
 * rule resolution) and to settings.networkRulesEnabled (master
 * toggle). Composes the display string from the two on every flow
 * emit. No engine changes — the eval pipeline already publishes
 * NetworkState on every run; we just render it.
 */
@Composable
private fun LiveEvalCard() {
    val app = PrivycsApp.instance
    val nm = remember { com.privycs.vpn.service.NetworkMonitor.getInstance(app) }
    val state by nm.networkState.collectAsState()
    val settings by app.settingsRepository.settingsFlow.collectAsState(
        initial = app.settingsRepository.defaultSettings()
    )

    val networkText = when {
        state.networkType == "wifi" && state.ssid.isNotEmpty() ->
            stringResource(R.string.netrules_eval_network_wifi_named, state.ssid)
        state.networkType == "wifi" ->
            stringResource(R.string.netrules_eval_network_wifi_unnamed)
        state.networkType == "mobile" ->
            stringResource(R.string.netrules_eval_network_mobile)
        state.networkType == "ethernet" ->
            stringResource(R.string.netrules_eval_network_ethernet)
        else ->
            stringResource(R.string.netrules_eval_network_none)
    }

    val masterText = if (settings.networkRulesEnabled) {
        stringResource(R.string.netrules_eval_master_on)
    } else {
        stringResource(R.string.netrules_eval_master_off)
    }

    val decisionText = when {
        !settings.networkRulesEnabled ->
            stringResource(R.string.netrules_eval_decision_manual)
        state.ruleMatch.isEmpty() ->
            stringResource(R.string.netrules_eval_decision_idle)
        else -> state.ruleMatch
    }

    Text(
        stringResource(R.string.netrules_eval_header),
        style = MaterialTheme.typography.labelSmall,
        fontWeight = FontWeight.SemiBold,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
    )
    Spacer(Modifier.height(8.dp))
    Card(
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surface,
        ),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            Text(
                networkText,
                style = MaterialTheme.typography.titleSmall,
            )
            Spacer(Modifier.height(2.dp))
            Text(
                masterText,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(8.dp))
            Text(
                stringResource(R.string.netrules_eval_arrow_prefix) + " " + decisionText,
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.primary,
                fontWeight = FontWeight.SemiBold,
            )
        }
    }
}

@Composable
private fun RuleRow(
    rule: NetworkRule,
    onEdit: () -> Unit,
    onDelete: () -> Unit,
    onMoveUp: () -> Unit,
    onMoveDown: () -> Unit,
    onToggle: () -> Unit,
) {
    Card(
        colors = CardDefaults.cardColors(
            containerColor = if (rule.enabled) MaterialTheme.colorScheme.surface
            else MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f),
        ),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    text = ruleSummary(rule),
                    style = MaterialTheme.typography.bodyMedium,
                    fontWeight = FontWeight.Medium,
                    modifier = Modifier.weight(1f),
                )
                Switch(checked = rule.enabled, onCheckedChange = { onToggle() })
            }
            if (rule.name.isNotBlank()) {
                Text(
                    rule.name,
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                IconButton(onClick = onMoveUp) {
                    Icon(
                        Icons.Filled.ArrowUpward,
                        stringResource(R.string.netrules_move_up),
                    )
                }
                IconButton(onClick = onMoveDown) {
                    Icon(
                        Icons.Filled.ArrowDownward,
                        stringResource(R.string.netrules_move_down),
                    )
                }
                Spacer(Modifier.weight(1f))
                IconButton(onClick = onEdit) {
                    Icon(
                        Icons.Filled.Edit,
                        stringResource(R.string.netrules_edit),
                    )
                }
                IconButton(onClick = onDelete) {
                    Icon(
                        Icons.Filled.Delete,
                        stringResource(R.string.netrules_delete),
                        tint = MaterialTheme.colorScheme.error,
                    )
                }
            }
        }
    }
}

@Composable
private fun ruleSummary(rule: NetworkRule): String {
    val match = when (rule.matchType) {
        RuleMatchType.SSID_EXACT ->
            stringResource(R.string.netrules_summary_ssid_exact, rule.matchValue)
        RuleMatchType.SSID_PATTERN ->
            stringResource(R.string.netrules_summary_ssid_pattern, rule.matchValue)
        RuleMatchType.NETWORK_TYPE ->
            stringResource(R.string.netrules_summary_network_type, rule.matchValue)
        RuleMatchType.BSSID ->
            stringResource(R.string.netrules_summary_bssid, rule.matchValue)
        RuleMatchType.ANY ->
            stringResource(R.string.netrules_summary_any)
    }
    val app = PrivycsApp.instance
    // collectAsState on the pool registry so renames / additions
    // recompose this row. .value access inside a composable is a
    // lint-flagged StateFlowValueCalledInComposition bug because
    // it would not subscribe to updates.
    val poolRegistry by app.poolRepository.registry.collectAsState()
    val connectionRegistry by app.connectionRepository.registry.collectAsState()
    val missing = stringResource(R.string.netrules_summary_missing_target)
    val target = when (rule.action) {
        RuleAction.NO_VPN -> stringResource(R.string.netrules_summary_target_no_vpn)
        RuleAction.CONNECT_ACTIVE ->
            stringResource(R.string.netrules_summary_target_connect_active)
        RuleAction.POOL -> {
            val pool = poolRegistry.pools.firstOrNull { it.id == rule.targetId }
            stringResource(R.string.netrules_summary_target_pool, pool?.name ?: missing)
        }
        RuleAction.CONNECTION -> {
            val conn = connectionRegistry.connections
                .firstOrNull { it.id == rule.targetId }
            stringResource(R.string.netrules_summary_target_connection, conn?.name ?: missing)
        }
    }
    return stringResource(R.string.netrules_summary_combined, match, target)
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun RuleEditDialog(
    initial: NetworkRule?,
    onDismiss: () -> Unit,
    onSave: (NetworkRule) -> Unit,
) {
    val app = PrivycsApp.instance
    val context = LocalContext.current
    // collectAsState so the dropdown picker stays fresh if a pool /
    // connection is added or renamed while the dialog is open.
    // .value direct-access here triggers the
    // StateFlowValueCalledInComposition lint rule.
    val poolRegistry by app.poolRepository.registry.collectAsState()
    val connectionRegistry by app.connectionRepository.registry.collectAsState()
    val pools = poolRegistry.pools
    val connections = connectionRegistry.connections

    var matchType by remember {
        mutableStateOf(initial?.matchType ?: RuleMatchType.SSID_EXACT)
    }
    var matchValue by remember { mutableStateOf(initial?.matchValue ?: "") }
    var action by remember { mutableStateOf(initial?.action ?: RuleAction.NO_VPN) }
    var targetId by remember { mutableStateOf(initial?.targetId ?: "") }
    var name by remember { mutableStateOf(initial?.name ?: "") }
    var enabled by remember { mutableStateOf(initial?.enabled ?: true) }

    var matchMenuOpen by remember { mutableStateOf(false) }
    var actionMenuOpen by remember { mutableStateOf(false) }
    var targetMenuOpen by remember { mutableStateOf(false) }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = {
            Text(
                if (initial == null) stringResource(R.string.netrules_dialog_add_title)
                else stringResource(R.string.netrules_dialog_edit_title),
            )
        },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    label = { Text(stringResource(R.string.netrules_field_name)) },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Box {
                    OutlinedButton(
                        onClick = { matchMenuOpen = true },
                        modifier = Modifier.fillMaxWidth(),
                    ) {
                        Text(
                            stringResource(
                                R.string.netrules_match_button,
                                matchType.label(context),
                            ),
                        )
                    }
                    DropdownMenu(
                        expanded = matchMenuOpen,
                        onDismissRequest = { matchMenuOpen = false },
                    ) {
                        RuleMatchType.values().forEach { t ->
                            DropdownMenuItem(
                                text = { Text(t.label(context)) },
                                onClick = { matchType = t; matchMenuOpen = false },
                            )
                        }
                    }
                }
                if (matchType != RuleMatchType.ANY) {
                    OutlinedTextField(
                        value = matchValue,
                        onValueChange = { matchValue = it },
                        label = { Text(matchType.fieldLabel(context)) },
                        placeholder = { Text(matchType.fieldHint(context)) },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
                Box {
                    OutlinedButton(
                        onClick = { actionMenuOpen = true },
                        modifier = Modifier.fillMaxWidth(),
                    ) {
                        Text(
                            stringResource(
                                R.string.netrules_action_button,
                                action.label(context),
                            ),
                        )
                    }
                    DropdownMenu(
                        expanded = actionMenuOpen,
                        onDismissRequest = { actionMenuOpen = false },
                    ) {
                        RuleAction.values().forEach { a ->
                            DropdownMenuItem(
                                text = { Text(a.label(context)) },
                                onClick = { action = a; targetId = ""; actionMenuOpen = false },
                            )
                        }
                    }
                }
                if (action == RuleAction.POOL || action == RuleAction.CONNECTION) {
                    Box {
                        OutlinedButton(
                            onClick = { targetMenuOpen = true },
                            modifier = Modifier.fillMaxWidth(),
                        ) {
                            val label = when (action) {
                                RuleAction.POOL -> pools.firstOrNull { it.id == targetId }?.name
                                    ?: stringResource(R.string.netrules_pick_pool)
                                RuleAction.CONNECTION -> connections.firstOrNull { it.id == targetId }?.name
                                    ?: stringResource(R.string.netrules_pick_connection)
                                else -> ""
                            }
                            Text(label)
                        }
                        DropdownMenu(
                            expanded = targetMenuOpen,
                            onDismissRequest = { targetMenuOpen = false },
                        ) {
                            when (action) {
                                RuleAction.POOL -> pools.forEach { p ->
                                    DropdownMenuItem(
                                        text = { Text(p.name) },
                                        onClick = { targetId = p.id; targetMenuOpen = false },
                                    )
                                }
                                RuleAction.CONNECTION -> connections.forEach { c ->
                                    DropdownMenuItem(
                                        text = { Text(c.name) },
                                        onClick = { targetId = c.id; targetMenuOpen = false },
                                    )
                                }
                                else -> Unit
                            }
                        }
                    }
                }
            }
        },
        confirmButton = {
            TextButton(
                enabled = (matchType == RuleMatchType.ANY || matchValue.isNotBlank()) &&
                    (action == RuleAction.NO_VPN || action == RuleAction.CONNECT_ACTIVE ||
                        targetId.isNotBlank()),
                onClick = {
                    onSave(
                        NetworkRule(
                            id = initial?.id ?: UUID.randomUUID().toString(),
                            priority = initial?.priority ?: 0,
                            matchType = matchType,
                            matchValue = matchValue.trim(),
                            action = action,
                            targetId = targetId,
                            enabled = enabled,
                            name = name.trim(),
                        ),
                    )
                },
            ) { Text(stringResource(R.string.netrules_save)) }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text(stringResource(R.string.netrules_cancel))
            }
        },
    )
}

private fun RuleMatchType.label(context: Context): String = when (this) {
    RuleMatchType.SSID_EXACT -> context.getString(R.string.netrules_match_ssid_exact)
    RuleMatchType.SSID_PATTERN -> context.getString(R.string.netrules_match_ssid_pattern)
    RuleMatchType.NETWORK_TYPE -> context.getString(R.string.netrules_match_network_type)
    RuleMatchType.BSSID -> context.getString(R.string.netrules_match_bssid)
    RuleMatchType.ANY -> context.getString(R.string.netrules_match_any)
}

private fun RuleMatchType.fieldLabel(context: Context): String = when (this) {
    RuleMatchType.SSID_EXACT -> context.getString(R.string.netrules_field_label_ssid_exact)
    RuleMatchType.SSID_PATTERN -> context.getString(R.string.netrules_field_label_ssid_pattern)
    RuleMatchType.NETWORK_TYPE -> context.getString(R.string.netrules_field_label_network_type)
    RuleMatchType.BSSID -> context.getString(R.string.netrules_field_label_bssid)
    RuleMatchType.ANY -> ""
}

private fun RuleMatchType.fieldHint(context: Context): String = when (this) {
    RuleMatchType.SSID_EXACT -> context.getString(R.string.netrules_field_hint_ssid_exact)
    RuleMatchType.SSID_PATTERN -> context.getString(R.string.netrules_field_hint_ssid_pattern)
    RuleMatchType.NETWORK_TYPE -> context.getString(R.string.netrules_field_hint_network_type)
    RuleMatchType.BSSID -> context.getString(R.string.netrules_field_hint_bssid)
    RuleMatchType.ANY -> ""
}

private fun RuleAction.label(context: Context): String = when (this) {
    RuleAction.NO_VPN -> context.getString(R.string.netrules_action_no_vpn)
    RuleAction.CONNECT_ACTIVE -> context.getString(R.string.netrules_action_connect_active)
    RuleAction.POOL -> context.getString(R.string.netrules_action_pool)
    RuleAction.CONNECTION -> context.getString(R.string.netrules_action_connection)
}
