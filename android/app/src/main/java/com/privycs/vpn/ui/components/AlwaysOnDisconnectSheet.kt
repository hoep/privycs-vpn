package com.privycs.vpn.ui.components

import android.content.Intent
import android.provider.Settings
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
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.outlined.OpenInNew
import androidx.compose.material.icons.outlined.Pause
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp

/**
 * Bottom sheet shown when the user taps Disconnect while Android's
 * system-level Always-On VPN is active.
 *
 * Rationale: with Always-On on, a raw stopSelf() is immediately undone
 * by the OS's START_STICKY auto-respawn - the tunnel is back up within
 * ~1 s and the disconnect button feels broken. This sheet exposes the
 * only two actions that actually work:
 *
 *  - Pause for N minutes: sets AlwaysOnDetector.pauseFor(N) which the
 *    service's handleAlwaysOnReconnect() honors by skipping the
 *    reconnect call until the timestamp expires.
 *  - Open system Always-On settings: deep-links to the VPN settings
 *    page so the user can flip off the global toggle directly.
 *
 * Same pattern used by Mullvad, IVPN and the WireGuard-Android
 * reference client.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AlwaysOnDisconnectSheet(
    onDismiss: () -> Unit,
    onPauseSelected: (minutes: Int) -> Unit,
) {
    val context = LocalContext.current
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
            // Explain WHY the usual disconnect does not work - users
            // who don't know about Always-On will otherwise think the
            // app is broken. One concise paragraph, not a treatise.
            Text(
                text = "Android's system setting 'Always-on VPN' is enabled for Privycs. "
                    + "Android will automatically reconnect the tunnel every time it's stopped. "
                    + "Choose how you'd like to disconnect:",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(modifier = Modifier.height(16.dp))

            // Pause options. Three tiers let the user pick granularity
            // without a slider (sliders in bottom sheets feel fiddly
            // on small phones). 5 min = quick task on untrusted net,
            // 15 min = captive portal / video call, 60 min = focused
            // local-network work where VPN is in the way.
            SheetAction(
                icon = Icons.Outlined.Pause,
                title = "Pause for 5 minutes",
                subtitle = "Tunnel stays down until the timer expires",
                onClick = {
                    onPauseSelected(5)
                    onDismiss()
                },
            )
            Spacer(modifier = Modifier.height(8.dp))
            SheetAction(
                icon = Icons.Outlined.Pause,
                title = "Pause for 15 minutes",
                subtitle = null,
                onClick = {
                    onPauseSelected(15)
                    onDismiss()
                },
            )
            Spacer(modifier = Modifier.height(8.dp))
            SheetAction(
                icon = Icons.Outlined.Pause,
                title = "Pause for 60 minutes",
                subtitle = null,
                onClick = {
                    onPauseSelected(60)
                    onDismiss()
                },
            )
            Spacer(modifier = Modifier.height(16.dp))

            // Divider. Raw Box + background so we don't pull in a
            // whole Divider import for one hairline.
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .height(1.dp)
                    .background(MaterialTheme.colorScheme.outlineVariant),
            )
            Spacer(modifier = Modifier.height(16.dp))

            SheetAction(
                icon = Icons.AutoMirrored.Outlined.OpenInNew,
                title = "Open Always-On settings",
                subtitle = "Disable Always-On VPN in Android system settings",
                onClick = {
                    val intent = Intent(Settings.ACTION_VPN_SETTINGS).apply {
                        flags = Intent.FLAG_ACTIVITY_NEW_TASK
                    }
                    try {
                        context.startActivity(intent)
                    } catch (_: Exception) {
                        // Falls Settings.ACTION_VPN_SETTINGS not available
                        // (extremely rare custom ROMs), fall back to the
                        // top-level Settings panel.
                        context.startActivity(
                            Intent(Settings.ACTION_SETTINGS)
                                .apply { flags = Intent.FLAG_ACTIVITY_NEW_TASK }
                        )
                    }
                    onDismiss()
                },
            )
            Spacer(modifier = Modifier.height(8.dp))
        }
    }
}

@Composable
private fun SheetAction(
    icon: androidx.compose.ui.graphics.vector.ImageVector,
    title: String,
    subtitle: String?,
    onClick: () -> Unit,
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(12.dp))
            .clickable(onClick = onClick)
            .padding(vertical = 10.dp, horizontal = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.Start,
    ) {
        Icon(
            imageVector = icon,
            contentDescription = null,
            modifier = Modifier.size(22.dp),
            tint = MaterialTheme.colorScheme.primary,
        )
        Spacer(modifier = Modifier.width(14.dp))
        Column {
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
