package com.privycs.vpn.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowBack
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import com.privycs.vpn.data.models.Pool
import com.privycs.vpn.data.models.PoolMember
import com.privycs.vpn.data.models.RegionCoverage
import kotlinx.coroutines.launch

/**
 * Pool detail — shows pool name, policy, member coverage breakdown,
 * the active member, and a list of all members with unreachable
 * badges. Settings cog opens an edit sheet (not implemented in this
 * file — separate component).
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PoolDetailScreen(
    pool: Pool,
    coverage: List<RegionCoverage>,
    activeMemberId: String,
    unreachableIds: Set<String>,
    onBack: () -> Unit,
    onEdit: () -> Unit,
    onResetUnreachable: suspend () -> Int,
    onActivate: () -> Unit,
    onDelete: () -> Unit
) {
    val scope = rememberCoroutineScope()
    var resetting by remember { mutableStateOf(false) }
    var memberFilter by remember { mutableStateOf("") }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(pool.name) },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                actions = {
                    IconButton(onClick = onEdit) {
                        Icon(Icons.Filled.Settings, contentDescription = "Edit pool")
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.background
                )
            )
        },
        containerColor = MaterialTheme.colorScheme.background
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
        ) {
            // Header summary
            Card(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp, vertical = 8.dp),
                colors = CardDefaults.cardColors(
                    containerColor = MaterialTheme.colorScheme.surface
                )
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text("${pool.policy.displayName} · ${pool.members.size} servers",
                        style = MaterialTheme.typography.bodyMedium)
                    Spacer(Modifier.height(4.dp))
                    if (pool.restrictRegions.isNotEmpty()) {
                        Text("Restricted to: ${pool.restrictRegions.joinToString(", ")}",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
            }

            // Coverage breakdown
            if (coverage.isNotEmpty()) {
                Card(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 16.dp, vertical = 4.dp),
                    colors = CardDefaults.cardColors(
                        containerColor = MaterialTheme.colorScheme.surface
                    )
                ) {
                    Column(modifier = Modifier.padding(16.dp)) {
                        Text("Coverage", style = MaterialTheme.typography.titleSmall)
                        Spacer(Modifier.height(8.dp))
                        for (c in coverage) {
                            Row(modifier = Modifier
                                .fillMaxWidth()
                                .padding(vertical = 2.dp)) {
                                Text(c.region, modifier = Modifier.weight(1f))
                                Text("${c.servers} servers · ${c.countries} countries",
                                    style = MaterialTheme.typography.bodySmall)
                            }
                        }
                    }
                }
            }

            // Action row
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp, vertical = 8.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                TextButton(onClick = onActivate, modifier = Modifier.weight(1f)) {
                    Text("Use this pool")
                }
                if (unreachableIds.isNotEmpty()) {
                    TextButton(
                        onClick = {
                            scope.launch {
                                resetting = true
                                onResetUnreachable()
                                resetting = false
                            }
                        },
                        enabled = !resetting,
                        modifier = Modifier.weight(1f)
                    ) {
                        Icon(Icons.Filled.Refresh, contentDescription = null,
                            modifier = Modifier.padding(end = 4.dp))
                        Text(if (resetting) "Resetting..." else "Reset (${unreachableIds.size})")
                    }
                }
            }

            // Members list
            val visibleMembers = remember(memberFilter, pool.members) {
                if (memberFilter.isEmpty()) pool.members
                else pool.members.filter {
                    it.name.contains(memberFilter, ignoreCase = true) ||
                            it.country.contains(memberFilter, ignoreCase = true) ||
                            it.region.contains(memberFilter, ignoreCase = true)
                }
            }

            // Members list. weight(1f) so the LazyColumn shares
            // remaining vertical space with the Delete button below
            // it instead of consuming all of it (which previously
            // pushed the Delete button off the screen on tall pools).
            LazyColumn(
                modifier = Modifier
                    .fillMaxWidth()
                    .weight(1f)
                    .padding(horizontal = 16.dp)
            ) {
                items(visibleMembers, key = { it.id }) { m ->
                    MemberRow(
                        member = m,
                        isActive = m.id == activeMemberId,
                        isUnreachable = m.id in unreachableIds
                    )
                }
            }

            // Delete (bottom) - always visible, dialog-confirmed.
            var showConfirmDelete by remember { mutableStateOf(false) }
            TextButton(
                onClick = { showConfirmDelete = true },
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(16.dp)
            ) {
                Text("Delete pool", color = MaterialTheme.colorScheme.error)
            }
            if (showConfirmDelete) {
                androidx.compose.material3.AlertDialog(
                    onDismissRequest = { showConfirmDelete = false },
                    title = { Text("Delete pool?") },
                    text = { Text("This removes the pool and all its imported configs. " +
                            "Cannot be undone.") },
                    confirmButton = {
                        TextButton(onClick = {
                            showConfirmDelete = false
                            onDelete()
                        }) {
                            Text("Delete", color = MaterialTheme.colorScheme.error)
                        }
                    },
                    dismissButton = {
                        TextButton(onClick = { showConfirmDelete = false }) {
                            Text("Cancel")
                        }
                    }
                )
            }
        }
    }
}

@Composable
private fun MemberRow(member: PoolMember, isActive: Boolean, isUnreachable: Boolean) {
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 2.dp),
        colors = CardDefaults.cardColors(
            containerColor = when {
                isUnreachable -> MaterialTheme.colorScheme.errorContainer.copy(alpha = 0.3f)
                isActive -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.3f)
                else -> MaterialTheme.colorScheme.surface
            }
        )
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 12.dp, vertical = 8.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    if (isUnreachable) {
                        Icon(
                            Icons.Filled.Warning,
                            contentDescription = "Unreachable",
                            tint = MaterialTheme.colorScheme.error,
                            modifier = Modifier
                                .padding(end = 6.dp)
                                .height(16.dp).width(16.dp)
                        )
                    }
                    Text(member.name,
                        style = MaterialTheme.typography.bodyMedium,
                        fontFamily = FontFamily.Monospace)
                }
                Text(
                    "${member.country.ifEmpty { "—" }} · ${member.region.ifEmpty { "Other" }}",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
            }
            if (isActive) {
                // Pill-style active badge — visually prominent so
                // user sees at a glance which member is connected.
                androidx.compose.material3.Badge(
                    containerColor = MaterialTheme.colorScheme.primary,
                    contentColor = MaterialTheme.colorScheme.onPrimary,
                    modifier = Modifier.padding(start = 8.dp)
                ) {
                    Text("ACTIVE",
                        style = MaterialTheme.typography.labelSmall)
                }
            }
        }
    }
}
