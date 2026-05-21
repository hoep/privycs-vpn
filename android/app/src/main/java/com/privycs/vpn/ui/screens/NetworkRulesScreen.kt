package com.privycs.vpn.ui.screens

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
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.ConnectOnDemandSettings
import com.privycs.vpn.data.models.NetworkRule
import com.privycs.vpn.data.models.RuleAction
import com.privycs.vpn.data.models.RuleMatchType
import kotlinx.coroutines.launch
import java.util.UUID

/**
 * Unified On-Demand & Network Rules screen. Phase 2 of the wgtunnel-
 * inspired roadmap, made the single source of truth for on-demand
 * behaviour in v0.9.15.73 (Option A1 — COD + Network-Rules
 * unification).
 *
 * Evaluation is strict first-match-then-fallback and the engine is
 * unchanged: on every network change the rules are checked top to
 * bottom, the first match wins; if no rule matches, the legacy
 * Connect-on-Demand trigger / SSID-list runs as the "Default
 * behaviour" shown pinned at the bottom. A1 only made that
 * precedence visible — it did not rewrite the engine.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun NetworkRulesScreen(onBack: () -> Unit, onEditDefault: () -> Unit) {
    val app = PrivycsApp.instance
    val repo = remember { app.networkRulesRepository }
    val rules by repo.rules.collectAsState()
    val settingsRepo = remember { app.settingsRepository }
    val settings by settingsRepo.settingsFlow.collectAsState(
        initial = settingsRepo.defaultSettings(),
    )
    val scope = rememberCoroutineScope()
    var editing by remember { mutableStateOf<NetworkRule?>(null) }
    var showCreate by remember { mutableStateOf(false) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text("On-Demand & Network Rules", fontWeight = FontWeight.SemiBold)
                },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
        floatingActionButton = {
            FloatingActionButton(onClick = { showCreate = true }) {
                Icon(Icons.Filled.Add, contentDescription = "Add rule")
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
                PrecedenceHeaderCard()
                Spacer(Modifier.height(16.dp))
            }

            if (rules.isEmpty()) {
                item { EmptyRulesCard() }
            } else {
                item {
                    Text(
                        "RULES — CHECKED TOP TO BOTTOM",
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
                DefaultBehaviourCard(
                    cod = settings.connectOnDemand,
                    onEdit = onEditDefault,
                )
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
                "How this screen decides",
                style = MaterialTheme.typography.titleSmall,
                color = MaterialTheme.colorScheme.primary,
            )
            Spacer(Modifier.height(6.dp))
            Text(
                "On every network change the rules below are checked " +
                    "top to bottom — the first rule that matches the " +
                    "current Wi-Fi or mobile network wins. If no rule " +
                    "matches, the Default behaviour pinned at the bottom " +
                    "applies. Rules are only evaluated while " +
                    "Connect-on-Demand is enabled.",
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
            Text("No rules yet", style = MaterialTheme.typography.titleSmall)
            Spacer(Modifier.height(4.dp))
            Text(
                "Every network falls through to the Default behaviour " +
                    "below. Add a rule with the + button to override it " +
                    "for a specific Wi-Fi SSID, BSSID, or network type — " +
                    "routing that network to a Pool, a Connection, or " +
                    "No VPN.",
                style = MaterialTheme.typography.bodySmall,
            )
        }
    }
}

/**
 * The pinned, non-deletable fallback card. Summarises the legacy
 * Connect-on-Demand trigger / SSID-list and links to the full
 * editor (ConnectOnDemandScreen).
 */
@Composable
private fun DefaultBehaviourCard(cod: ConnectOnDemandSettings, onEdit: () -> Unit) {
    Text(
        "FALLBACK — WHEN NO RULE MATCHES",
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
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        "Default behaviour",
                        style = MaterialTheme.typography.titleSmall,
                    )
                    Text(
                        "Connect on Demand — applies to any network no rule above matched",
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
                TextButton(onClick = onEdit) {
                    Icon(
                        Icons.Filled.Edit,
                        contentDescription = null,
                        modifier = Modifier.size(16.dp),
                    )
                    Spacer(Modifier.width(4.dp))
                    Text("Edit")
                }
            }
            Spacer(Modifier.height(8.dp))
            Text(
                connectOnDemandSummary(cod),
                style = MaterialTheme.typography.bodyMedium,
                fontWeight = FontWeight.Medium,
            )
        }
    }
}

private fun connectOnDemandSummary(cod: ConnectOnDemandSettings): String {
    if (!cod.enabled) {
        return "Connect-on-Demand is off — the VPN stays under manual " +
            "control on networks no rule matched."
    }
    val trigger = when (cod.trigger) {
        "wifi" -> "Connect on Wi-Fi"
        "mobile" -> "Connect on mobile data"
        else -> "Connect on Wi-Fi & mobile data"
    }
    val wifiInTrigger = cod.trigger == "wifi" || cod.trigger == "wifi_mobile"
    if (!wifiInTrigger) return trigger
    val n = cod.ssidList.size
    val nets = "Wi-Fi network" + if (n == 1) "" else "s"
    return when (cod.ssidMode) {
        "only" -> "$trigger — but only on $n saved $nets"
        "except" -> "$trigger — except $n saved $nets"
        else -> "$trigger — on every Wi-Fi network"
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
                IconButton(onClick = onMoveUp) { Icon(Icons.Filled.ArrowUpward, "Up") }
                IconButton(onClick = onMoveDown) { Icon(Icons.Filled.ArrowDownward, "Down") }
                Spacer(Modifier.weight(1f))
                IconButton(onClick = onEdit) { Icon(Icons.Filled.Edit, "Edit") }
                IconButton(onClick = onDelete) {
                    Icon(Icons.Filled.Delete, "Delete", tint = MaterialTheme.colorScheme.error)
                }
            }
        }
    }
}

@Composable
private fun ruleSummary(rule: NetworkRule): String {
    val match = when (rule.matchType) {
        RuleMatchType.SSID_EXACT -> "SSID = \"${rule.matchValue}\""
        RuleMatchType.SSID_PATTERN -> "SSID matches \"${rule.matchValue}\""
        RuleMatchType.NETWORK_TYPE -> "Network = ${rule.matchValue}"
        RuleMatchType.BSSID -> "BSSID = ${rule.matchValue}"
        RuleMatchType.ANY -> "Any network"
    }
    val app = PrivycsApp.instance
    // collectAsState on the pool registry so renames / additions
    // recompose this row. .value access inside a composable is a
    // lint-flagged StateFlowValueCalledInComposition bug because
    // it would not subscribe to updates.
    val poolRegistry by app.poolRepository.registry.collectAsState()
    val connectionRegistry by app.connectionRepository.registry.collectAsState()
    val target = when (rule.action) {
        RuleAction.NO_VPN -> "→ No VPN"
        RuleAction.POOL -> {
            val pool = poolRegistry.pools.firstOrNull { it.id == rule.targetId }
            "→ Pool: ${pool?.name ?: "(missing)"}"
        }
        RuleAction.CONNECTION -> {
            val conn = connectionRegistry.connections
                .firstOrNull { it.id == rule.targetId }
            "→ Connection: ${conn?.name ?: "(missing)"}"
        }
    }
    return "$match  $target"
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun RuleEditDialog(
    initial: NetworkRule?,
    onDismiss: () -> Unit,
    onSave: (NetworkRule) -> Unit,
) {
    val app = PrivycsApp.instance
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
        title = { Text(if (initial == null) "Add Rule" else "Edit Rule") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    label = { Text("Name (optional)") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Box {
                    OutlinedButton(
                        onClick = { matchMenuOpen = true },
                        modifier = Modifier.fillMaxWidth(),
                    ) {
                        Text("Match: ${matchType.label()}")
                    }
                    DropdownMenu(
                        expanded = matchMenuOpen,
                        onDismissRequest = { matchMenuOpen = false },
                    ) {
                        RuleMatchType.values().forEach { t ->
                            DropdownMenuItem(
                                text = { Text(t.label()) },
                                onClick = { matchType = t; matchMenuOpen = false },
                            )
                        }
                    }
                }
                if (matchType != RuleMatchType.ANY) {
                    OutlinedTextField(
                        value = matchValue,
                        onValueChange = { matchValue = it },
                        label = { Text(matchType.fieldLabel()) },
                        placeholder = { Text(matchType.fieldHint()) },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
                Box {
                    OutlinedButton(
                        onClick = { actionMenuOpen = true },
                        modifier = Modifier.fillMaxWidth(),
                    ) {
                        Text("Action: ${action.label()}")
                    }
                    DropdownMenu(
                        expanded = actionMenuOpen,
                        onDismissRequest = { actionMenuOpen = false },
                    ) {
                        RuleAction.values().forEach { a ->
                            DropdownMenuItem(
                                text = { Text(a.label()) },
                                onClick = { action = a; targetId = ""; actionMenuOpen = false },
                            )
                        }
                    }
                }
                if (action != RuleAction.NO_VPN) {
                    Box {
                        OutlinedButton(
                            onClick = { targetMenuOpen = true },
                            modifier = Modifier.fillMaxWidth(),
                        ) {
                            val label = when (action) {
                                RuleAction.POOL -> pools.firstOrNull { it.id == targetId }?.name
                                    ?: "Pick a pool…"
                                RuleAction.CONNECTION -> connections.firstOrNull { it.id == targetId }?.name
                                    ?: "Pick a connection…"
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
                    (action == RuleAction.NO_VPN || targetId.isNotBlank()),
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
            ) { Text("Save") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

private fun RuleMatchType.label(): String = when (this) {
    RuleMatchType.SSID_EXACT -> "Wi-Fi SSID (exact)"
    RuleMatchType.SSID_PATTERN -> "Wi-Fi SSID (pattern)"
    RuleMatchType.NETWORK_TYPE -> "Network type"
    RuleMatchType.BSSID -> "Wi-Fi BSSID (MAC)"
    RuleMatchType.ANY -> "Any network"
}

private fun RuleMatchType.fieldLabel(): String = when (this) {
    RuleMatchType.SSID_EXACT -> "SSID"
    RuleMatchType.SSID_PATTERN -> "SSID pattern (use *, ?)"
    RuleMatchType.NETWORK_TYPE -> "wifi / mobile / ethernet / wifi_mobile / any"
    RuleMatchType.BSSID -> "BSSID (e.g. aa:bb:cc:dd:ee:ff)"
    RuleMatchType.ANY -> ""
}

private fun RuleMatchType.fieldHint(): String = when (this) {
    RuleMatchType.SSID_EXACT -> "HomeWifi"
    RuleMatchType.SSID_PATTERN -> "Cafe-*"
    RuleMatchType.NETWORK_TYPE -> "wifi"
    RuleMatchType.BSSID -> "aa:bb:cc:dd:ee:ff"
    RuleMatchType.ANY -> ""
}

private fun RuleAction.label(): String = when (this) {
    RuleAction.NO_VPN -> "No VPN (trusted)"
    RuleAction.POOL -> "Use Pool"
    RuleAction.CONNECTION -> "Use Connection"
}
