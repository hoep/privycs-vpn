package com.privycs.vpn.ui.tv

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.tv.material3.Button
import androidx.tv.material3.Card
import androidx.tv.material3.ExperimentalTvMaterial3Api
import androidx.tv.material3.MaterialTheme
import androidx.tv.material3.OutlinedButton
import androidx.tv.material3.Text
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
import com.privycs.vpn.api.GatewayApiClient
import com.privycs.vpn.config.ConfigParser
import com.privycs.vpn.data.models.RemoteConfigEntry
import com.privycs.vpn.service.VpnServiceManager
import kotlinx.coroutines.launch

/**
 * TV connect surface.
 *
 * Left = a focus-navigable list of selectable targets (saved connections
 * first, then any gateway configs not yet pulled). Right = a big connect /
 * disconnect control + live status driven by [VpnServiceManager.status].
 *
 * Reuses the engine exactly like the phone ConnectScreen: selecting a
 * saved connection calls [VpnServiceManager.switchActiveConnection]; the
 * big button calls [onRequestConnect] (which runs VpnService.prepare()
 * consent then VpnServiceManager.connect) or [VpnServiceManager.disconnect].
 * Gateway configs are pulled with [GatewayApiClient] (the same call the
 * phone AddConnectionScreen uses) and saved via ConnectionRepository.
 *
 * Deliberately omitted on TV (TV_PORT_PLAN.md §5): QR import, per-app VPN,
 * network-rules engine, IPSec setup. IPSec entries returned by the gateway
 * are filtered out of the pull list for v1.
 */
@OptIn(ExperimentalTvMaterial3Api::class)
@Composable
fun TvConnectScreen(
    vpnManager: VpnServiceManager,
    onRequestConnect: () -> Unit,
    onRelink: () -> Unit,
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val app = PrivycsApp.instance
    val connectionRepo = app.connectionRepository

    val status by vpnManager.status.collectAsState()
    val isConnecting by vpnManager.isConnecting.collectAsState()
    val registry by connectionRepo.registry.collectAsState()
    val settings by app.settingsRepository.settingsFlow
        .collectAsState(initial = app.settingsRepository.defaultSettings())

    val isConnected = status.connected

    // Gateway pull state.
    var gatewayConfigs by remember { mutableStateOf<List<RemoteConfigEntry>>(emptyList()) }
    var gatewayError by remember { mutableStateOf<String?>(null) }
    var downloadingKey by remember { mutableStateOf<String?>(null) }
    val hasGateway = settings.gatewayUrl.isNotBlank() && settings.apiKey.isNotBlank()

    LaunchedEffect(Unit) {
        vpnManager.refreshStatus()
    }

    // Pull the gateway config list once on entry (if enrolled). IPSec is
    // deferred on TV, so those entries are filtered out.
    LaunchedEffect(hasGateway) {
        if (!hasGateway) return@LaunchedEffect
        gatewayError = null
        try {
            val client = GatewayApiClient(settings.gatewayUrl, settings.apiKey)
            gatewayConfigs = client.fetchProfile().configs
                .filter { it.protocol.lowercase() != "ipsec" }
            client.close()
        } catch (e: Exception) {
            gatewayError = e.message
        }
    }

    Row(modifier = Modifier.fillMaxSize().padding(40.dp)) {
        // ---- Left column: selectable targets ----------------------------
        Column(
            modifier = Modifier
                .weight(1f)
                .fillMaxHeight(),
        ) {
            Text(
                text = stringResource(R.string.tv_connect_servers_title),
                style = MaterialTheme.typography.titleLarge,
                fontWeight = FontWeight.Bold,
                color = MaterialTheme.colorScheme.onSurface,
            )
            Spacer(Modifier.height(16.dp))

            // Regular Compose-foundation LazyColumn: foundation 1.7 (pinned via
            // the Compose BOM) handles D-pad focus + scrolling natively, so we
            // avoid androidx.tv:tv-foundation (alpha12) whose LazyLayout ABI is
            // incompatible with foundation 1.7 and would NoSuchMethodError at runtime.
            LazyColumn(
                verticalArrangement = Arrangement.spacedBy(12.dp),
                modifier = Modifier.weight(1f),
            ) {
                // Saved connections — active first.
                items(registry.connections, key = { it.id }) { conn ->
                    TvTargetCard(
                        title = conn.name,
                        subtitle = conn.activeProtocol.label,
                        selected = conn.id == registry.activeId,
                        onClick = { vpnManager.switchActiveConnection(conn.id) },
                    )
                }

                // Gateway configs not yet saved locally — tap to download + save.
                val unsaved = gatewayConfigs.filter { entry ->
                    registry.connections.none { it.name == entry.peerName }
                }
                if (unsaved.isNotEmpty()) {
                    items(unsaved, key = { "gw-${it.protocol}-${it.id}" }) { entry ->
                        val key = "${entry.protocol}-${entry.id}"
                        TvTargetCard(
                            title = entry.peerName,
                            subtitle = if (downloadingKey == key)
                                stringResource(R.string.tv_connect_downloading)
                            else stringResource(R.string.tv_connect_tap_to_add),
                            selected = false,
                            onClick = {
                                downloadingKey = key
                                gatewayError = null
                                scope.launch {
                                    try {
                                        val client = GatewayApiClient(
                                            settings.gatewayUrl, settings.apiKey
                                        )
                                        val content = client.fetchConfig(entry.protocol, entry.id)
                                        client.close()
                                        val stableId = "gw-${entry.protocol}-${entry.id}"
                                        val filename = when (entry.protocol) {
                                            "wireguard", "amneziawg" -> "$stableId.conf"
                                            "openvpn" -> "$stableId.ovpn"
                                            else -> "$stableId.conf"
                                        }
                                        val parsed = ConfigParser.buildProtocolConfig(content, filename)
                                            ?.copy(id = stableId)
                                        if (parsed != null) {
                                            connectionRepo.addOrUpdate(null, entry.peerName, parsed)
                                        } else {
                                            gatewayError = context.getString(
                                                R.string.tv_connect_add_failed
                                            )
                                        }
                                    } catch (e: Exception) {
                                        gatewayError = e.message
                                    } finally {
                                        downloadingKey = null
                                    }
                                }
                            },
                        )
                    }
                }
            }

            gatewayError?.let { err ->
                Text(
                    text = err,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.error,
                    modifier = Modifier.padding(top = 8.dp),
                )
            }

            Spacer(Modifier.height(12.dp))
            OutlinedButton(onClick = onRelink) {
                Text(stringResource(R.string.tv_connect_relink))
            }
        }

        Spacer(Modifier.width(40.dp))

        // ---- Right column: status + connect control ---------------------
        Column(
            modifier = Modifier
                .weight(1f)
                .fillMaxHeight(),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
        ) {
            val activeName = status.connectionName.ifBlank {
                connectionRepo.getActive()?.name
                    ?: stringResource(R.string.tv_connect_no_selection)
            }
            Text(
                text = activeName,
                style = MaterialTheme.typography.headlineSmall,
                fontWeight = FontWeight.Bold,
                color = MaterialTheme.colorScheme.onSurface,
            )
            Spacer(Modifier.height(8.dp))

            val statusText = when {
                isConnecting -> stringResource(R.string.tv_connect_status_connecting)
                isConnected -> stringResource(R.string.tv_connect_status_connected)
                else -> stringResource(R.string.tv_connect_status_disconnected)
            }
            Text(
                text = statusText,
                style = MaterialTheme.typography.titleMedium,
                color = if (isConnected) MaterialTheme.colorScheme.primary
                else MaterialTheme.colorScheme.onSurfaceVariant,
            )

            if (isConnected && status.serverEndpoint.isNotBlank()) {
                Spacer(Modifier.height(4.dp))
                Text(
                    text = status.serverEndpoint,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }

            status.error?.takeIf { it.isNotBlank() }?.let { err ->
                Spacer(Modifier.height(8.dp))
                Text(
                    text = err,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.error,
                )
            }

            Spacer(Modifier.height(32.dp))

            val canConnect = registry.connections.isNotEmpty()
            Button(
                onClick = {
                    if (isConnected) {
                        vpnManager.disconnect()
                    } else if (canConnect) {
                        onRequestConnect()
                    }
                },
            ) {
                Text(
                    text = when {
                        isConnected -> stringResource(R.string.tv_connect_button_disconnect)
                        isConnecting -> stringResource(R.string.tv_connect_button_connecting)
                        else -> stringResource(R.string.tv_connect_button_connect)
                    },
                    style = MaterialTheme.typography.titleLarge,
                    modifier = Modifier.padding(horizontal = 24.dp, vertical = 8.dp),
                )
            }

            if (!canConnect) {
                Spacer(Modifier.height(16.dp))
                Text(
                    text = stringResource(R.string.tv_connect_pick_a_server),
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}

/**
 * A single focusable target row. androidx.tv.material3.Card gives the
 * built-in TV focus scaling / border treatment so D-pad navigation is
 * visually obvious without custom focus handling.
 */
@OptIn(ExperimentalTvMaterial3Api::class)
@Composable
private fun TvTargetCard(
    title: String,
    subtitle: String,
    selected: Boolean,
    onClick: () -> Unit,
) {
    Card(
        onClick = onClick,
        modifier = Modifier.fillMaxWidth(),
    ) {
        Box(modifier = Modifier.padding(16.dp)) {
            Column {
                Text(
                    text = title,
                    style = MaterialTheme.typography.titleMedium,
                    fontWeight = if (selected) FontWeight.Bold else FontWeight.Normal,
                    color = if (selected) MaterialTheme.colorScheme.primary
                    else MaterialTheme.colorScheme.onSurface,
                )
                Text(
                    text = subtitle,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}
