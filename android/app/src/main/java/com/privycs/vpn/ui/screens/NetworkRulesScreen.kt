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
import com.privycs.vpn.data.models.NetworkRule
import com.privycs.vpn.data.models.RuleAction
import com.privycs.vpn.data.models.RuleMatchType
import kotlinx.coroutines.launch
import java.util.UUID

/**
 * Per-network auto-tunnel rules editor. Phase 2 of the wgtunnel-
 * inspired roadmap. List of rules in priority order; first
 * matching rule on every network change determines the active
 * VPN target (or "no VPN" for trusted networks).
 *
 * Rules engine is authoritative once the user has at least one
 * rule. With zero rules, the legacy COD trigger / SSID-mode
 * logic runs as before - existing users see no change until
 * they add a rule. Communicated in the empty-state banner.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun NetworkRulesScreen(onBack: () -> Unit) {
    val app = PrivycsApp.instance
    val repo = remember { app.networkRulesRepository }
    val rules by repo.rules.collectAsState()
    val scope = rememberCoroutineScope()
    var editing by remember { mutableStateOf<NetworkRule?>(null) }
    var showCreate by remember { mutableStateOf(false) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Network Rules", fontWeight = FontWeight.SemiBold) },
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
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .padding(horizontal = 16.dp),
        ) {
            if (rules.isEmpty()) {
                Spacer(Modifier.height(12.dp))
                Card(
                    colors = CardDefaults.cardColors(
                        containerColor = MaterialTheme.colorScheme.surfaceVariant,
                    ),
                ) {
                    Column(modifier = Modifier.padding(16.dp)) {
                        Text(
                            "No rules defined",
                            style = MaterialTheme.typography.titleSmall,
                        )
                        Spacer(Modifier.height(4.dp))
                        Text(
                            "When empty, the legacy Connect-on-Demand " +
                                "trigger + SSID list (Settings → Connect on Demand) " +
                                "drives the lifecycle. Add a rule below to take " +
                                "fine-grained control: per-SSID / per-BSSID / per-" +
                                "transport routing to a specific Pool, Connection, " +
                                "or No VPN.",
                            style = MaterialTheme.typography.bodySmall,
                        )
                    }
                }
            } else {
                LazyColumn(modifier = Modifier.fillMaxSize()) {
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
    val target = when (rule.action) {
        RuleAction.NO_VPN -> "→ No VPN"
        RuleAction.POOL -> {
            val pool = app.poolRepository.registry.value.pools
                .firstOrNull { it.id == rule.targetId }
            "→ Pool: ${pool?.name ?: "(missing)"}"
        }
        RuleAction.CONNECTION -> {
            val conn = app.connectionRepository.getById(rule.targetId)
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
    val pools = app.poolRepository.registry.value.pools
    val connections = app.connectionRepository.connections

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
