package com.privycs.vpn.ui.components

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import com.privycs.vpn.R
import com.privycs.vpn.util.DnsValidator

/**
 * Reusable preset picker for DNS Override fields. Renders an
 * OutlinedButton that opens a DropdownMenu listing every entry in
 * DnsValidator.providers. Selecting a provider invokes onPick with
 * the comma-separated dual-stack server list, ready to paste into
 * the matching OutlinedTextField that owns the override string.
 *
 * Used in three places (v0.9.14.4):
 *  - SettingsScreen (global DNS override)
 *  - ConnectionsScreen rename dialog (per-connection)
 *  - PoolDetailHost edit form (per-pool)
 *
 * Keeping the picker in one place ensures the option set, ordering,
 * and dual-text-with-note styling stay identical across all three
 * surfaces. The Provider list itself comes from DnsValidator —
 * canonical with desktop's GetDnsProviders.
 */
@Composable
fun DnsPresetPicker(
    onPick: (servers: String) -> Unit,
    modifier: Modifier = Modifier,
    label: String = stringResource(R.string.dnspreset_pick_preset_label),
) {
    var open by remember { mutableStateOf(false) }
    Box(modifier = modifier.fillMaxWidth()) {
        OutlinedButton(
            onClick = { open = true },
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text(label)
        }
        DropdownMenu(
            expanded = open,
            onDismissRequest = { open = false },
        ) {
            DnsValidator.providers.forEach { p ->
                DropdownMenuItem(
                    text = {
                        // Brand-colored badge + two-line label.
                        // v0.9.14.14 — instant brand recognition via
                        // colored badge mirrors desktop DnsProviderBadge.
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(10.dp),
                        ) {
                            DnsProviderBadge(id = p.id)
                            Column {
                                Text(p.label, style = MaterialTheme.typography.bodyMedium)
                                Text(
                                    p.note,
                                    style = MaterialTheme.typography.labelSmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                )
                            }
                        }
                    },
                    onClick = {
                        open = false
                        onPick(p.servers.joinToString(", "))
                    },
                )
            }
        }
    }
}
