package com.privycs.vpn.ui.screens

import android.content.Intent
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.data.models.RegionCoverage
import com.privycs.vpn.service.PrivycsVpnService
import kotlinx.coroutines.delay

/**
 * Wires PoolDetailScreen to the actual repositories. Resolves
 * the active member ID + the unreachable-flag set from the
 * state repo on every render (debounced via produceState +
 * delay so a rapid mutation series doesn't repaint thrashily).
 */
@Composable
fun PoolDetailHost(
    poolId: String,
    onBack: () -> Unit,
    onActivated: () -> Unit,
    onDeleted: () -> Unit
) {
    val app = PrivycsApp.instance
    val ctx = LocalContext.current
    val pool = remember(poolId) { app.poolRepository.get(poolId) }

    // Bail if the pool was deleted between navigation and render.
    if (pool == null) {
        LaunchedEffect(Unit) { onBack() }
        return
    }

    // Coverage is pure-read on definitions, so we compute it once
    // at composition entry and cache.
    val coverage: List<RegionCoverage> = remember(pool) { app.poolRepository.coverage(pool) }

    // Active member + unreachable set come from the state repo.
    // produceState polls every 1s — the state repo is event-driven
    // but we don't expose a Flow on it, so this is the simplest
    // correct integration. 1s cadence is barely visible to the
    // user and keeps the render path uncoupled from the state-
    // repo's lock contention.
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

    PoolDetailScreen(
        pool = pool,
        coverage = coverage,
        activeMemberId = activeMemberId,
        unreachableIds = unreachableIds,
        onBack = onBack,
        onEdit = {
            // Edit-pool BottomSheet not yet implemented in this
            // round - silent for now. Phase J+ work.
        },
        onResetUnreachable = {
            app.poolRepository.clearAllMembersUnreachable(poolId)
        },
        onActivate = {
            // Persist as the active pool, kick the VpnService
            // with ACTION_POOL_CONNECT. The service does the
            // pick-and-connect via PoolConnector.
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
}
