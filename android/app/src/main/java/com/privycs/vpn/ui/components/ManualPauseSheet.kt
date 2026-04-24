package com.privycs.vpn.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Pause
import androidx.compose.material.icons.outlined.PowerSettingsNew
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp

/**
 * Bottom sheet shown when the user long-presses the Connect screen
 * toggle button to disconnect with more granularity than the
 * default "off = off forever" behaviour.
 *
 * Offers a plain disconnect plus three timed pauses (1/3/5 min)
 * which auto-reconnect if Connect-on-Demand rules still match at
 * expiry. Driven by [com.privycs.vpn.util.VpnPauseTimer].
 *
 * This is separate from AlwaysOnDisconnectSheet: that one handles
 * the Always-On-VPN system setting where plain disconnect is
 * neutralised by OS respawn; this one is for everyday use with
 * Always-On disabled.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ManualPauseSheet(
    onDismiss: () -> Unit,
    onDisconnect: () -> Unit,
    onPauseSelected: (minutes: Int) -> Unit,
) {
    val sheetState = rememberModalBottomSheetState()

    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 20.dp, vertical = 12.dp),
        ) {
            Text(
                text = "Disconnect VPN",
                style = MaterialTheme.typography.titleLarge,
                fontWeight = FontWeight.SemiBold,
                color = MaterialTheme.colorScheme.onSurface,
            )
            Spacer(modifier = Modifier.height(4.dp))
            Text(
                text = "Choose how you'd like to disconnect. Pauses automatically reconnect if your Connect-on-Demand rules still match when the timer expires.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(modifier = Modifier.height(16.dp))

            PauseSheetAction(
                icon = Icons.Outlined.PowerSettingsNew,
                title = "Disconnect now",
                subtitle = "Stays disconnected until you reconnect manually",
                onClick = {
                    onDisconnect()
                    onDismiss()
                },
            )
            Spacer(modifier = Modifier.height(8.dp))
            PauseSheetAction(
                icon = Icons.Outlined.Pause,
                title = "Pause for 1 minute",
                subtitle = null,
                onClick = {
                    onPauseSelected(1)
                    onDismiss()
                },
            )
            Spacer(modifier = Modifier.height(8.dp))
            PauseSheetAction(
                icon = Icons.Outlined.Pause,
                title = "Pause for 3 minutes",
                subtitle = null,
                onClick = {
                    onPauseSelected(3)
                    onDismiss()
                },
            )
            Spacer(modifier = Modifier.height(8.dp))
            PauseSheetAction(
                icon = Icons.Outlined.Pause,
                title = "Pause for 5 minutes",
                subtitle = null,
                onClick = {
                    onPauseSelected(5)
                    onDismiss()
                },
            )
            Spacer(modifier = Modifier.height(16.dp))
        }
    }
}

@Composable
private fun PauseSheetAction(
    icon: ImageVector,
    title: String,
    subtitle: String?,
    onClick: () -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .border(
                width = 1.dp,
                color = MaterialTheme.colorScheme.outlineVariant,
                shape = RoundedCornerShape(12.dp),
            )
            .background(
                color = MaterialTheme.colorScheme.surface,
                shape = RoundedCornerShape(12.dp),
            )
            .clickable { onClick() }
            .padding(horizontal = 16.dp, vertical = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(
            imageVector = icon,
            contentDescription = null,
            tint = MaterialTheme.colorScheme.primary,
            modifier = Modifier.size(24.dp),
        )
        Spacer(modifier = Modifier.width(14.dp))
        Column(verticalArrangement = Arrangement.Center) {
            Text(
                text = title,
                style = MaterialTheme.typography.bodyLarge,
                fontWeight = FontWeight.Medium,
                color = MaterialTheme.colorScheme.onSurface,
            )
            if (subtitle != null) {
                Text(
                    text = subtitle,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}
