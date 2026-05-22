package com.privycs.vpn.ui.screens

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
import com.privycs.vpn.data.models.Pool
import com.privycs.vpn.data.models.PoolPolicy
import com.privycs.vpn.data.models.RegionCoverage
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Wires PoolDetailScreen to the actual repositories. Resolves the
 * active member ID + the unreachable-flag set from the state repo
 * on every render. Pool definition itself is observed via the
 * registry StateFlow so an in-place edit (rename / policy /
 * interval) is reflected immediately without a re-navigate.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PoolDetailHost(
    poolId: String,
    onBack: () -> Unit,
    onActivated: () -> Unit,
    onDeleted: () -> Unit
) {
    val app = PrivycsApp.instance
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val registry by app.poolRepository.registry.collectAsState()
    val pool: Pool? = remember(poolId, registry) {
        registry.pools.firstOrNull { it.id == poolId }
    }

    if (pool == null) {
        LaunchedEffect(Unit) { onBack() }
        return
    }

    val coverage: List<RegionCoverage> = remember(pool) { app.poolRepository.coverage(pool) }

    val activeMemberId by produceState("", poolId) {
        while (true) {
            value = app.poolRepository.activeMemberId(poolId)
            delay(1000)
        }
    }
    val unreachableIds by produceState<Set<String>>(emptySet(), poolId) {
        while (true) {
            val ids = mutableSetOf<String>()
            for (m in pool.members) {
                if (app.poolRepository.isMemberUnreachable(poolId, m.id)) ids.add(m.id)
            }
            value = ids
            delay(2000)
        }
    }

    var showEditSheet by remember { mutableStateOf(false) }

    PoolDetailScreen(
        pool = pool,
        coverage = coverage,
        activeMemberId = activeMemberId,
        unreachableIds = unreachableIds,
        onBack = onBack,
        onEdit = { showEditSheet = true },
        onResetUnreachable = {
            app.poolRepository.clearAllMembersUnreachable(poolId)
        },
        onActivate = {
            // v0.9.15.74 (B-5): whole activation on the Compose scope
            // instead of runBlocking on the UI thread. setActive +
            // setActiveId are suspend calls and run sequentially here,
            // so switchActivePool still observes the cleared/updated
            // selection — same ordering guarantee as the old
            // runBlocking, without blocking the main thread.
            scope.launch {
                // Activating a pool clears the active single-connection
                // selection so the Connect screen does not show stale
                // "Currently:" text from a previously-selected single
                // while the pool boots. Mirrors desktop's
                // app_pool.go:407 `a.connections.SetActive("")`.
                app.connectionRepository.setActive("")
                app.poolRepository.setActiveId(poolId)

                // Warm the SelfIp cache in the background so the next
                // pickAndConnect (about to fire via the service intent
                // below) gets the user's country from cache instead of
                // probing synchronously in the connect critical path.
                // 3-second budget; if probing takes longer the picker
                // still completes (degraded to RestrictRegions-only).
                kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.Dispatchers.IO).launch {
                    try {
                        app.selfIpDetector.countryFor(timeoutMs = 3_000L)
                    } catch (e: Exception) {
                        // Probe failed (no internet, captive portal).
                        // Picker tiers still degrade gracefully.
                    }
                }
                // Funnel pool selection through VpnServiceManager
                // .switchActivePool (clears single-active, sets pool-
                // active, updates status, lets COD decide reconnect).
                // Pool tap NEVER auto-connects directly here — that was
                // the v0.9.11.59 over-connect bug.
                com.privycs.vpn.service.VpnServiceManager
                    .getInstance(ctx)
                    .switchActivePool(poolId)
                onActivated()
            }
        },
        onDelete = {
            scope.launch {
                app.poolRepository.delete(poolId)
                onDeleted()
            }
        }
    )

    if (showEditSheet) {
        EditPoolSheet(
            pool = pool,
            onDismiss = { showEditSheet = false },
            onSave = { updated ->
                scope.launch {
                    app.poolRepository.update(updated)
                    showEditSheet = false
                }
            }
        )
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun EditPoolSheet(
    pool: Pool,
    onDismiss: () -> Unit,
    onSave: (Pool) -> Unit
) {
    val sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)
    val scope = rememberCoroutineScope()

    var name by remember(pool.id) { mutableStateOf(pool.name) }
    var policy by remember(pool.id) { mutableStateOf(pool.policy) }
    var intervalMin by remember(pool.id) {
        mutableStateOf(pool.rotation.intervalMin.toString())
    }
    var excludePrivate by remember(pool.id) {
        mutableStateOf(pool.splitTunnel.excludePrivateNetworks)
    }
    // Bypass CIDRs as a multi-line String for the textfield so the
    // user can edit freely. Persisted-list <-> textfield-string
    // round-trip happens at Save time.
    var bypassCidrsText by remember(pool.id) {
        mutableStateOf(pool.splitTunnel.bypassCidrs.joinToString("\n"))
    }
    // Per-pool DNS override. When non-empty, overrides
    // Settings.dnsOverride for this pool's tunnel only.
    var dnsOverride by remember(pool.id) { mutableStateOf(pool.dnsOverride) }
    // Per-line validation tally so the UI can surface "3 lines, 1
    // invalid". Recomputed on every keystroke (cheap: parse is
    // microseconds + the list is short).
    val bypassValidation = remember(bypassCidrsText) {
        val lines = bypassCidrsText.split("\n").map { it.trim() }.filter { it.isNotEmpty() }
        val invalid = lines.filter { com.privycs.vpn.data.CidrMath.parse(it) == null }
        Triple(lines.size, invalid.size, invalid)
    }

    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
        containerColor = MaterialTheme.colorScheme.surface
    ) {
        // Scrollable column so the Save / Cancel buttons remain
        // reachable even when (a) the soft keyboard is open and
        // (b) the Round-Robin policy expands the interval field.
        // Without this scroll the buttons end up below the visible
        // sheet area on small / split-screen devices and the user
        // cannot save edits. Adding imePadding prevents the keyboard
        // from obscuring the active input field.
        Column(
            modifier = Modifier
                .padding(horizontal = 24.dp, vertical = 16.dp)
                .imePadding()
                .verticalScroll(rememberScrollState())
        ) {
            Text(stringResource(R.string.pooldetailhost_edit_pool_title), style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(16.dp))

            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text(stringResource(R.string.pooldetailhost_pool_name_label)) },
                singleLine = true,
                modifier = Modifier.fillMaxWidth()
            )
            Spacer(Modifier.height(16.dp))

            Text(stringResource(R.string.pooldetailhost_policy_label), style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(4.dp))
            SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                val policies = PoolPolicy.values()
                policies.forEachIndexed { i, p ->
                    SegmentedButton(
                        selected = policy == p,
                        onClick = { policy = p },
                        shape = SegmentedButtonDefaults.itemShape(index = i, count = policies.size)
                    ) { Text(p.displayName) }
                }
            }

            if (policy == PoolPolicy.ROUND_ROBIN) {
                Spacer(Modifier.height(16.dp))
                OutlinedTextField(
                    value = intervalMin,
                    onValueChange = {
                        intervalMin = it.filter { ch -> ch.isDigit() }
                    },
                    label = { Text(stringResource(R.string.pooldetailhost_rotation_interval_label)) },
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth()
                )
            }

            // Split-tunnel section. CIDRs entered here are excluded
            // from the tunnel (traffic to those ranges goes via the
            // local default gateway). WireGuard + OpenVPN supported;
            // IPSec pool members ignore this and log a warning.
            // Pro gate 6 — hidden for Free users. excludePrivate /
            // bypassCidrsText keep their loaded values, so Save
            // re-persists any existing CIDRs untouched.
            if (PrivycsApp.instance.entitlementRepository.isUnlocked()) {
            Spacer(Modifier.height(20.dp))
            androidx.compose.material3.HorizontalDivider()
            Spacer(Modifier.height(12.dp))
            Text(stringResource(R.string.pooldetailhost_split_tunnel_title),
                style = MaterialTheme.typography.titleSmall)
            Spacer(Modifier.height(4.dp))
            Text(
                stringResource(R.string.pooldetailhost_split_tunnel_desc),
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant
            )
            Spacer(Modifier.height(8.dp))
            // Switch + label-column matches the rest of the app's
            // toggle pattern (SettingsScreen, PerAppVpnScreen). Earlier
            // draft used Checkbox which is consistent with no other
            // toggle in the codebase - swapped for visual coherence
            // with the existing UI surface.
            Row(
                modifier = Modifier.fillMaxWidth(),
                verticalAlignment = androidx.compose.ui.Alignment.CenterVertically
            ) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(stringResource(R.string.pooldetailhost_exclude_private_label),
                        style = MaterialTheme.typography.bodyMedium)
                    Text(
                        stringResource(R.string.pooldetailhost_exclude_private_desc),
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }
                androidx.compose.material3.Switch(
                    checked = excludePrivate,
                    onCheckedChange = { excludePrivate = it },
                    colors = androidx.compose.material3.SwitchDefaults.colors(
                        checkedTrackColor = MaterialTheme.colorScheme.primary,
                        checkedThumbColor = MaterialTheme.colorScheme.onPrimary
                    )
                )
            }
            Spacer(Modifier.height(12.dp))
            OutlinedTextField(
                value = bypassCidrsText,
                onValueChange = { bypassCidrsText = it },
                label = { Text(stringResource(R.string.pooldetailhost_bypass_cidrs_label)) },
                placeholder = { Text(stringResource(R.string.pooldetailhost_bypass_cidrs_placeholder)) },
                modifier = Modifier
                    .fillMaxWidth()
                    .height(120.dp),
                supportingText = {
                    val (total, invalidCount, invalidLines) = bypassValidation
                    when {
                        total == 0 -> Text(
                            stringResource(R.string.pooldetailhost_bypass_cidrs_empty)
                        )
                        invalidCount == 0 -> Text(
                            pluralStringResource(
                                R.plurals.pooldetailhost_bypass_cidrs_valid, total, total
                            ),
                            color = MaterialTheme.colorScheme.primary
                        )
                        else -> Text(
                            stringResource(
                                R.string.pooldetailhost_bypass_cidrs_invalid,
                                invalidCount, total,
                                invalidLines.take(3).joinToString(", ")
                            ),
                            color = MaterialTheme.colorScheme.error
                        )
                    }
                }
            )
            }

            Spacer(Modifier.height(12.dp))
            // Per-pool DNS override. Empty = inherit Settings global.
            // Validation feedback via supportingText so the user
            // knows which entries the inject pipeline will reject
            // before they save and connect.
            val dnsInvalid = remember(dnsOverride) {
                com.privycs.vpn.util.DnsValidator.invalidEntries(dnsOverride)
            }
            OutlinedTextField(
                value = dnsOverride,
                onValueChange = { dnsOverride = it },
                label = { Text(stringResource(R.string.pooldetailhost_dns_override_label)) },
                placeholder = { Text(stringResource(R.string.pooldetailhost_dns_override_placeholder)) },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
                supportingText = {
                    when {
                        dnsOverride.isBlank() -> Text(
                            stringResource(R.string.pooldetailhost_dns_override_empty)
                        )
                        dnsInvalid.isEmpty() -> Text(
                            stringResource(R.string.pooldetailhost_dns_override_valid),
                            color = MaterialTheme.colorScheme.primary
                        )
                        else -> Text(
                            stringResource(
                                R.string.pooldetailhost_dns_override_invalid,
                                dnsInvalid.joinToString(", ")
                            ),
                            color = MaterialTheme.colorScheme.error
                        )
                    }
                },
                isError = dnsInvalid.isNotEmpty()
            )

            // Per-pool preset dropdown (v0.9.14.4) — same picker as
            // Settings global and per-connection, lets users fill
            // the override field by tap instead of manual typing.
            Spacer(Modifier.height(8.dp))
            com.privycs.vpn.ui.components.DnsPresetPicker(
                onPick = { dnsOverride = it },
            )

            Spacer(Modifier.height(20.dp))
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.End
            ) {
                TextButton(onClick = onDismiss) { Text(stringResource(R.string.pooldetailhost_cancel)) }
                Spacer(Modifier.width(8.dp))
                Button(
                    enabled = name.isNotBlank(),
                    onClick = {
                        val intMin = intervalMin.toIntOrNull()?.coerceAtLeast(1) ?: 30
                        // Save only valid CIDRs - silently drop
                        // malformed entries. The UI surfaced the
                        // count above so the user already knew
                        // some lines were bad; saving with bad
                        // ones included would just have them
                        // stripped at injection time anyway.
                        val cidrLines = bypassCidrsText.split("\n")
                            .map { it.trim() }
                            .filter {
                                it.isNotEmpty() &&
                                        com.privycs.vpn.data.CidrMath.parse(it) != null
                            }
                        val updated = pool.copy(
                            name = name.trim(),
                            policy = policy,
                            rotation = pool.rotation.copy(intervalMin = intMin),
                            splitTunnel = com.privycs.vpn.data.models.PoolSplitTunnel(
                                bypassCidrs = cidrLines,
                                excludePrivateNetworks = excludePrivate
                            ),
                            dnsOverride = dnsOverride.trim()
                        )
                        scope.launch { onSave(updated) }
                    }
                ) { Text(stringResource(R.string.pooldetailhost_save)) }
            }
            Spacer(Modifier.height(8.dp))
        }
    }
}
