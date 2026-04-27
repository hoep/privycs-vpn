package com.privycs.vpn.ui.screens

import android.content.Intent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
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
            kotlinx.coroutines.runBlocking { app.poolRepository.setActiveId(poolId) }
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

    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
        containerColor = MaterialTheme.colorScheme.surface
    ) {
        Column(modifier = Modifier.padding(horizontal = 24.dp, vertical = 16.dp)) {
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
                        val updated = pool.copy(
                            name = name.trim(),
                            policy = policy,
                            rotation = pool.rotation.copy(intervalMin = intMin)
                        )
                        scope.launch { onSave(updated) }
                    }
                ) { Text("Save") }
            }
            Spacer(Modifier.height(8.dp))
        }
    }
}
