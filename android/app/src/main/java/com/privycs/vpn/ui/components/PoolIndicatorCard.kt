package com.privycs.vpn.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
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
import androidx.compose.ui.unit.dp
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
    nextRotationInMs: Long?,           // null if not round-robin or no next-rotation set
    pendingMemberName: String?,
    pendingMemberCountry: String?,
    onClick: () -> Unit
) {
    var localCountdownMs by remember(nextRotationInMs) {
        mutableStateOf(nextRotationInMs ?: 0L)
    }
    LaunchedEffect(nextRotationInMs) {
        if (nextRotationInMs == null) return@LaunchedEffect
        while (localCountdownMs > 0) {
            delay(1000L)
            localCountdownMs = max(0L, localCountdownMs - 1000L)
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
                Text(
                    "Currently: ${pool.activeMemberName}" +
                            (if (pool.activeMemberCountry.isNotEmpty()) " (${pool.activeMemberCountry})" else ""),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurface
                )
            }

            // Next member (pre-warm)
            if (!pendingMemberName.isNullOrEmpty()) {
                Text(
                    "Next: $pendingMemberName" +
                            (if (!pendingMemberCountry.isNullOrEmpty()) " ($pendingMemberCountry)" else ""),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.tertiary
                )
            }

            // Countdown (round-robin only)
            if (nextRotationInMs != null && nextRotationInMs > 0) {
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
                        Text("Next rotation",
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
