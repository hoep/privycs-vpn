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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import com.privycs.vpn.R
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
                        Icon(
                            Icons.Filled.ArrowBack,
                            contentDescription = stringResource(R.string.pooldetail_back)
                        )
                    }
                },
                actions = {
                    IconButton(onClick = onEdit) {
                        Icon(
                            Icons.Filled.Settings,
                            contentDescription = stringResource(R.string.pooldetail_edit_pool)
                        )
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.background
                )
            )
        },
        containerColor = MaterialTheme.colorScheme.background
    ) { padding ->
        // Single-LazyColumn layout: header card, coverage card,
        // action row, member rows, and delete button are all
        // items in the SAME LazyColumn. This makes the entire page
        // scrollable as a unit (user complaint v0.9.11.50: member
        // list was tiny and only its own scroll viewport scrolled).
        // Previously the outer Column had fixed-size cards on top
        // and a weight(1f) LazyColumn for members, so the member
        // viewport was whatever vertical space was left over after
        // the cards - on small phones with a long restrict-regions
        // line that was almost nothing.
        var showConfirmDelete by remember { mutableStateOf(false) }
        val visibleMembers = remember(memberFilter, pool.members) {
            if (memberFilter.isEmpty()) pool.members
            else pool.members.filter {
                it.name.contains(memberFilter, ignoreCase = true) ||
                        it.country.contains(memberFilter, ignoreCase = true) ||
                        it.region.contains(memberFilter, ignoreCase = true)
            }
        }
        LazyColumn(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding),
            contentPadding = androidx.compose.foundation.layout.PaddingValues(
                horizontal = 16.dp,
                vertical = 8.dp
            ),
            verticalArrangement = Arrangement.spacedBy(8.dp)
        ) {
            // Header summary card
            item {
                Card(
                    modifier = Modifier.fillMaxWidth(),
                    colors = CardDefaults.cardColors(
                        containerColor = MaterialTheme.colorScheme.surface
                    )
                ) {
                    Column(modifier = Modifier.padding(16.dp)) {
                        Text(
                            stringResource(
                                R.string.pooldetail_header_summary,
                                pool.policy.displayName,
                                pool.members.size
                            ),
                            style = MaterialTheme.typography.titleMedium
                        )
                        if (pool.restrictRegions.isNotEmpty()) {
                            Spacer(Modifier.height(4.dp))
                            Text(
                                stringResource(
                                    R.string.pooldetail_restricted_to,
                                    pool.restrictRegions.joinToString(", ")
                                ),
                                style = MaterialTheme.typography.bodyMedium,
                                color = MaterialTheme.colorScheme.onSurfaceVariant
                            )
                        }
                    }
                }
            }

            // Coverage breakdown card
            if (coverage.isNotEmpty()) {
                item {
                    Card(
                        modifier = Modifier.fillMaxWidth(),
                        colors = CardDefaults.cardColors(
                            containerColor = MaterialTheme.colorScheme.surface
                        )
                    ) {
                        Column(modifier = Modifier.padding(16.dp)) {
                            Text(
                                stringResource(R.string.pooldetail_coverage),
                                style = MaterialTheme.typography.titleSmall
                            )
                            Spacer(Modifier.height(8.dp))
                            for (c in coverage) {
                                Row(
                                    modifier = Modifier
                                        .fillMaxWidth()
                                        .padding(vertical = 4.dp)
                                ) {
                                    Text(
                                        c.region,
                                        style = MaterialTheme.typography.bodyMedium,
                                        modifier = Modifier.weight(1f)
                                    )
                                    Text(
                                        stringResource(
                                            R.string.pooldetail_coverage_servers_countries,
                                            c.servers,
                                            c.countries
                                        ),
                                        style = MaterialTheme.typography.bodyMedium,
                                        color = MaterialTheme.colorScheme.onSurfaceVariant
                                    )
                                }
                            }
                        }
                    }
                }
            }

            // Action row
            item {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    TextButton(onClick = onActivate, modifier = Modifier.weight(1f)) {
                        Text(
                            stringResource(R.string.pooldetail_use_this_pool),
                            style = MaterialTheme.typography.bodyMedium
                        )
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
                            Icon(
                                Icons.Filled.Refresh, contentDescription = null,
                                modifier = Modifier.padding(end = 4.dp)
                            )
                            Text(
                                if (resetting) {
                                    stringResource(R.string.pooldetail_resetting)
                                } else {
                                    stringResource(
                                        R.string.pooldetail_reset_count,
                                        unreachableIds.size
                                    )
                                }
                            )
                        }
                    }
                }
            }

            // Members header row
            item {
                Text(
                    stringResource(
                        R.string.pooldetail_servers_count,
                        visibleMembers.size
                    ),
                    style = MaterialTheme.typography.titleSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(top = 8.dp, bottom = 4.dp)
                )
            }

            items(visibleMembers, key = { it.id }) { m ->
                MemberRow(
                    member = m,
                    isActive = m.id == activeMemberId,
                    isUnreachable = m.id in unreachableIds
                )
            }

            // Delete (bottom) - always visible, dialog-confirmed.
            item {
                Spacer(Modifier.height(8.dp))
                TextButton(
                    onClick = { showConfirmDelete = true },
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(8.dp)
                ) {
                    Text(
                        stringResource(R.string.pooldetail_delete_pool),
                        color = MaterialTheme.colorScheme.error,
                        style = MaterialTheme.typography.bodyMedium
                    )
                }
            }
        }
        if (showConfirmDelete) {
            androidx.compose.material3.AlertDialog(
                onDismissRequest = { showConfirmDelete = false },
                title = { Text(stringResource(R.string.pooldetail_delete_dialog_title)) },
                text = {
                    Text(stringResource(R.string.pooldetail_delete_dialog_text))
                },
                confirmButton = {
                    TextButton(onClick = {
                        showConfirmDelete = false
                        onDelete()
                    }) {
                        Text(
                            stringResource(R.string.pooldetail_delete),
                            color = MaterialTheme.colorScheme.error
                        )
                    }
                },
                dismissButton = {
                    TextButton(onClick = { showConfirmDelete = false }) {
                        Text(stringResource(R.string.pooldetail_cancel))
                    }
                }
            )
        }
    }
}

@Composable
private fun MemberRow(member: PoolMember, isActive: Boolean, isUnreachable: Boolean) {
    // Bigger row: titleSmall name (was bodyMedium), bodyMedium for
    // the location subtitle (was bodySmall), and 14.dp vertical
    // padding (was 8.dp). The combined effect is roughly 1.4x the
    // visual height and ~30% larger text — addresses the user
    // complaint that pool member rows were "viel zu klein" on
    // v0.9.11.50. Also enriches the subtitle from cryptic "AT ·
    // Europe" to readable "🇦🇹 Vienna, Austria · Europe" using the
    // shared PoolHostnameLabels helper.
    val flag = com.privycs.vpn.data.PoolHostnameLabels.flagEmojiFromCode(member.country)
    val city = com.privycs.vpn.data.PoolHostnameLabels.cityFromHostname(member.name)
    val countryName = com.privycs.vpn.data.PoolHostnameLabels.countryNameFromCode(member.country)
    val regionLabel = member.region.ifEmpty { stringResource(R.string.pooldetail_region_other) }
    val cityCountry = stringResource(R.string.pooldetail_city_country, city, countryName)
    val unknownLocation = stringResource(R.string.pooldetail_location_unknown)
    val locationLine = buildString {
        if (flag.isNotEmpty()) append(flag).append("  ")
        when {
            city.isNotEmpty() && countryName.isNotEmpty() ->
                append(cityCountry)
            city.isNotEmpty() -> append(city)
            countryName.isNotEmpty() -> append(countryName)
            member.country.isNotEmpty() -> append(member.country)
            else -> append(unknownLocation)
        }
        append(" · ").append(regionLabel)
    }

    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(
            containerColor = when {
                isUnreachable -> MaterialTheme.colorScheme.errorContainer.copy(alpha = 0.3f)
                isActive -> MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.3f)
                else -> MaterialTheme.colorScheme.surface
            }
        )
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 14.dp, vertical = 14.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    if (isUnreachable) {
                        Icon(
                            Icons.Filled.Warning,
                            contentDescription = stringResource(R.string.pooldetail_unreachable),
                            tint = MaterialTheme.colorScheme.error,
                            modifier = Modifier
                                .padding(end = 8.dp)
                                .height(20.dp).width(20.dp)
                        )
                    }
                    Text(
                        member.name,
                        style = MaterialTheme.typography.titleSmall,
                        fontFamily = FontFamily.Monospace
                    )
                }
                Spacer(Modifier.height(4.dp))
                Text(
                    locationLine,
                    style = MaterialTheme.typography.bodyMedium,
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
                    Text(stringResource(R.string.pooldetail_active),
                        style = MaterialTheme.typography.labelSmall)
                }
            }
        }
    }
}
