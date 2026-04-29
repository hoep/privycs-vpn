package com.privycs.vpn.ui.screens

import android.content.Intent
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
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.Pool
import com.privycs.vpn.data.models.PoolPolicy
import com.privycs.vpn.data.models.RegionCoverage
import com.privycs.vpn.service.PrivycsVpnService
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
            kotlinx.coroutines.runBlocking {
                // Activating a pool clears the active single-connection
                // selection so the Connect screen does not show stale
                // "Currently:" text from a previously-selected single
                // while the pool boots. Mirrors desktop's
                // app_pool.go:407 `a.connections.SetActive("")`.
                app.connectionRepository.setActive("")
                app.poolRepository.setActiveId(poolId)
            }
            // Warm the SelfIp cache in the background so the next
            // pickAndConnect (about to fire via the service intent
            // below) gets the user's country from cache instead of
            // probing synchronously in the connect critical path.
            // Mirrors desktop's app_pool.go:434 `go autoRestrictRoundRobinToHomeRegion(p)`
            // which is what populates the cache for the connect-time
            // pick.
            //
            // 3-second budget here; if probing takes longer the picker
            // still completes (degraded to RestrictRegions-only) and a
            // later background call will repopulate the cache.
            kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.Dispatchers.IO).launch {
                try {
                    app.selfIpDetector.countryFor(timeoutMs = 3_000L)
                } catch (e: Exception) {
                    // Probe failed (no internet, captive portal).
                    // Picker tier-1+2 short-circuit; tier-3 random pick
                    // (within RestrictRegions) still works.
                }
            }
            val intent = Intent(ctx, PrivycsVpnService::class.java).apply {
                action = PrivycsVpnService.ACTION_POOL_CONNECT
                putExtra(PrivycsVpnService.EXTRA_POOL_ID, poolId)
            }
            if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.O) {
                ctx.startForegroundService(intent)
            } else {
                ctx.startService(intent)
            }
            onActivated()
        },
        onDelete = {
            kotlinx.coroutines.runBlocking { app.poolRepository.delete(poolId) }
            onDeleted()
        }
    )

    if (showEditSheet) {
        EditPoolSheet(
            pool = pool,
            onDismiss = { showEditSheet = false },
            onSave = { updated ->
                kotlinx.coroutines.runBlocking { app.poolRepository.update(updated) }
                showEditSheet = false
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
            Text("Edit pool", style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(16.dp))

            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text("Pool name") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth()
            )
            Spacer(Modifier.height(16.dp))

            Text("Policy", style = MaterialTheme.typography.labelMedium)
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
                    label = { Text("Rotation interval (minutes)") },
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth()
                )
            }

            // Split-tunnel section. CIDRs entered here are excluded
            // from the tunnel (traffic to those ranges goes via the
            // local default gateway). WireGuard + OpenVPN supported;
            // IPSec pool members ignore this and log a warning.
            Spacer(Modifier.height(20.dp))
            androidx.compose.material3.HorizontalDivider()
            Spacer(Modifier.height(12.dp))
            Text("Split tunnel (bypass)",
                style = MaterialTheme.typography.titleSmall)
            Spacer(Modifier.height(4.dp))
            Text(
                "Traffic to these IP ranges goes around the VPN. " +
                        "WireGuard + OpenVPN; IPSec members are skipped " +
                        "(server-side traffic-selector negotiation).",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant
            )
            Spacer(Modifier.height(8.dp))
            Row(
                modifier = Modifier.fillMaxWidth(),
                verticalAlignment = androidx.compose.ui.Alignment.CenterVertically
            ) {
                androidx.compose.material3.Checkbox(
                    checked = excludePrivate,
                    onCheckedChange = { excludePrivate = it }
                )
                Column(modifier = Modifier.weight(1f)) {
                    Text("Exclude private networks",
                        style = MaterialTheme.typography.bodyMedium)
                    Text(
                        "RFC1918 (10/8, 172.16/12, 192.168/16) + " +
                                "IPv6 ULA fc00::/7 + link-local",
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }
            }
            Spacer(Modifier.height(12.dp))
            OutlinedTextField(
                value = bypassCidrsText,
                onValueChange = { bypassCidrsText = it },
                label = { Text("Custom bypass CIDRs (one per line)") },
                placeholder = { Text("203.0.113.0/24\n2001:db8::/32\n198.51.100.42") },
                modifier = Modifier
                    .fillMaxWidth()
                    .height(120.dp),
                supportingText = {
                    val (total, invalidCount, invalidLines) = bypassValidation
                    when {
                        total == 0 -> Text(
                            "Empty = no custom CIDRs (private-networks toggle still applies)"
                        )
                        invalidCount == 0 -> Text(
                            "$total CIDR${if (total == 1) "" else "s"} valid",
                            color = MaterialTheme.colorScheme.primary
                        )
                        else -> Text(
                            "$invalidCount of $total invalid: " +
                                    invalidLines.take(3).joinToString(", "),
                            color = MaterialTheme.colorScheme.error
                        )
                    }
                }
            )

            Spacer(Modifier.height(20.dp))
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.End
            ) {
                TextButton(onClick = onDismiss) { Text("Cancel") }
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
                            )
                        )
                        scope.launch { onSave(updated) }
                    }
                ) { Text("Save") }
            }
            Spacer(Modifier.height(8.dp))
        }
    }
}
