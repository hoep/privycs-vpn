package com.privycs.vpn.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Hub
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import com.privycs.vpn.R
import com.privycs.vpn.data.models.PoolListItem
import kotlinx.coroutines.delay
import kotlin.math.max

/**
 * Pool indicator on the Connect screen — appears above the connect
 * button when a pool is the active selection. Shows pool name,
 * policy, current member, and (for round-robin) a live countdown
 * to next rotation with optional "Next: <member>" line.
 *
 * Refreshes the countdown locally every 1s so the user sees seconds
 * tick down smoothly between data refreshes from the service.
 */
@Composable
fun PoolIndicatorCard(
    pool: PoolListItem,
    nextRotationAt: Long,              // epoch-ms; 0 = no countdown (non-RR or not connected)
    pendingMemberName: String?,
    pendingMemberCountry: String?,
    onClick: () -> Unit
) {
    // Countdown source-of-truth: an epoch-ms timestamp from the
    // service's authoritative VpnStatus. The service rewrites this
    // on every successful pool connect / rotation (including
    // pre-warm), so a fresh value invalidates the remember() block
    // below and the local countdown restarts cleanly.
    //
    // Earlier draft used `nextRotationInMs: Long?` (a delta). That
    // failed because `intervalMin * 60 * 1000` is the SAME number
    // before and after a rotation - remember() saw equal keys, never
    // invalidated, the countdown ticked once down to zero and stayed
    // stuck (the "00:00 forever" symptom).
    val showCountdown = nextRotationAt > 0L
    var localCountdownMs by remember(nextRotationAt) {
        mutableStateOf(
            if (showCountdown) max(0L, nextRotationAt - System.currentTimeMillis())
            else 0L
        )
    }
    LaunchedEffect(nextRotationAt) {
        if (!showCountdown) return@LaunchedEffect
        while (true) {
            val remaining = nextRotationAt - System.currentTimeMillis()
            localCountdownMs = max(0L, remaining)
            if (remaining <= 0) break
            delay(500L)
        }
    }

    Card(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onClick),
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.15f)
        )
    ) {
        Column(modifier = Modifier.padding(12.dp)) {
            // Top row: icon + name + policy
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(
                    Icons.Filled.Hub,
                    contentDescription = null,
                    tint = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.height(18.dp)
                )
                Spacer(Modifier.padding(horizontal = 4.dp))
                Text(
                    pool.name,
                    style = MaterialTheme.typography.titleSmall,
                    color = MaterialTheme.colorScheme.onSurface
                )
                Spacer(Modifier.padding(horizontal = 4.dp))
                Text(
                    "· ${pool.policy.displayName}",
                    style = MaterialTheme.typography.labelMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
            }

            // Active member
            if (pool.activeMemberName.isNotEmpty()) {
                Spacer(Modifier.height(4.dp))
                val activeMemberDisplay = pool.activeMemberName +
                        (if (pool.activeMemberCountry.isNotEmpty()) " (${pool.activeMemberCountry})" else "")
                Text(
                    stringResource(R.string.poolind_currently, activeMemberDisplay),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurface
                )
            }

            // Next member (pre-warm) — tertiary-colored pill via
            // Surface so the @Composable scope flows correctly and
            // the color/shape APIs work without Modifier.background
            // inference issues.
            if (!pendingMemberName.isNullOrEmpty()) {
                Spacer(Modifier.height(4.dp))
                androidx.compose.material3.Surface(
                    color = MaterialTheme.colorScheme.tertiary.copy(alpha = 0.15f),
                    shape = RoundedCornerShape(4.dp),
                    contentColor = MaterialTheme.colorScheme.tertiary
                ) {
                    val pendingMemberDisplay = pendingMemberName +
                            (if (!pendingMemberCountry.isNullOrEmpty()) " ($pendingMemberCountry)" else "")
                    Text(
                        stringResource(R.string.poolind_next, pendingMemberDisplay),
                        style = MaterialTheme.typography.bodySmall,
                        modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp)
                    )
                }
            }

            // Countdown (round-robin only — non-zero nextRotationAt
            // signals a scheduled rotation)
            if (showCountdown) {
                Spacer(Modifier.height(8.dp))
                Row(verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.SpaceBetween,
                    modifier = Modifier.fillMaxWidth()) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Icon(
                            Icons.Filled.Refresh,
                            contentDescription = null,
                            tint = MaterialTheme.colorScheme.primary,
                            modifier = Modifier.height(14.dp)
                        )
                        Spacer(Modifier.padding(horizontal = 4.dp))
                        Text(stringResource(R.string.poolind_next_rotation),
                            style = MaterialTheme.typography.labelMedium)
                    }
                    Text(
                        formatCountdown(localCountdownMs),
                        style = MaterialTheme.typography.titleMedium,
                        color = MaterialTheme.colorScheme.primary
                    )
                }
            }
        }
    }
}

private fun formatCountdown(ms: Long): String {
    val totalSec = (ms / 1000L).coerceAtLeast(0L)
    val m = totalSec / 60
    val s = totalSec % 60
    return "%02d:%02d".format(m, s)
}
