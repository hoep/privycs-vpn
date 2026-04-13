package com.privycs.vpn.ui.screens

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Cloud
import androidx.compose.material.icons.filled.CloudDownload
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SwipeToDismissBox
import androidx.compose.material3.SwipeToDismissBoxValue
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.rememberSwipeToDismissBoxState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.api.GatewayApiClient
import com.privycs.vpn.config.ConfigParser
import com.privycs.vpn.data.models.ProtocolConfig
import com.privycs.vpn.data.models.RemoteConfigEntry
import com.privycs.vpn.data.models.VpnConnection
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.ui.theme.IpSecBlue
import com.privycs.vpn.ui.theme.OpenVpnOrange
import com.privycs.vpn.ui.theme.StatusConnected
import com.privycs.vpn.ui.theme.WireGuardRed
import kotlinx.coroutines.launch
import java.time.Instant

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ConnectionsScreen(
    onNavigateToAdd: () -> Unit,
    onNavigateToConnect: () -> Unit
) {
    val context = LocalContext.current
    val connectionRepo = remember { PrivycsApp.instance.connectionRepository }
    val settingsRepo = remember { PrivycsApp.instance.settingsRepository }
    val registry by connectionRepo.registry.collectAsState()
    val settings by settingsRepo.settingsFlow.collectAsState(initial = settingsRepo.defaultSettings())
    val connections = registry.connections
    val scope = rememberCoroutineScope()

    var deleteTarget by remember { mutableStateOf<VpnConnection?>(null) }
    var showGateway by remember { mutableStateOf(false) }
    var gatewayConfigs by remember { mutableStateOf<List<RemoteConfigEntry>>(emptyList()) }
    var gatewayLoading by remember { mutableStateOf(false) }
    var gatewayError by remember { mutableStateOf<String?>(null) }
    var downloadingId by remember { mutableStateOf<Int?>(null) }

    // Delete confirmation dialog
    if (deleteTarget != null) {
        AlertDialog(
            onDismissRequest = { deleteTarget = null },
            title = { Text("Delete Connection") },
            text = { Text("Delete \"${deleteTarget!!.name}\" and all its configs?") },
            confirmButton = {
                TextButton(onClick = {
                    connectionRepo.delete(deleteTarget!!.id)
                    deleteTarget = null
                }) {
                    Text("Delete", color = MaterialTheme.colorScheme.error)
                }
            },
            dismissButton = {
                TextButton(onClick = { deleteTarget = null }) {
                    Text("Cancel")
                }
            }
        )
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        "Connections",
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.SemiBold
                    )
                },
                actions = {
                    if (settings.gatewayUrl.isNotBlank() && settings.apiKey.isNotBlank()) {
                        IconButton(onClick = {
                            showGateway = !showGateway
                            if (showGateway && gatewayConfigs.isEmpty()) {
                                scope.launch {
                                    gatewayLoading = true
                                    gatewayError = null
                                    try {
                                        val client = GatewayApiClient(settings.gatewayUrl, settings.apiKey)
                                        val profile = client.fetchProfile()
                                        gatewayConfigs = profile.configs
                                        client.close()
                                    } catch (e: Exception) {
                                        gatewayError = e.message
                                    } finally {
                                        gatewayLoading = false
                                    }
                                }
                            }
                        }) {
                            Icon(
                                Icons.Filled.Cloud,
                                contentDescription = "Gateway",
                                tint = if (showGateway) MaterialTheme.colorScheme.primary
                                else MaterialTheme.colorScheme.onSurfaceVariant
                            )
                        }
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.background
                )
            )
        },
        floatingActionButton = {
            FloatingActionButton(
                onClick = onNavigateToAdd,
                containerColor = MaterialTheme.colorScheme.primary
            ) {
                Icon(Icons.Filled.Add, contentDescription = "Add connection")
            }
        }
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
        ) {
            // Gateway remote configs panel
            if (showGateway) {
                GatewayPanel(
                    configs = gatewayConfigs,
                    isLoading = gatewayLoading,
                    error = gatewayError,
                    downloadingId = downloadingId,
                    onDownload = { entry ->
                        scope.launch {
                            downloadingId = entry.id
                            try {
                                val client = GatewayApiClient(settings.gatewayUrl, settings.apiKey)
                                val configContent = client.fetchConfig(entry.protocol, entry.id)
                                client.close()

                                val filename = when (entry.protocol) {
                                    "wireguard" -> "${entry.peerName}.conf"
                                    "openvpn" -> "${entry.peerName}.ovpn"
                                    else -> "${entry.peerName}.conf"
                                }

                                val protocolConfig = ConfigParser.buildProtocolConfig(configContent, filename)
                                if (protocolConfig != null) {
                                    connectionRepo.addOrUpdate(null, entry.peerName, protocolConfig)
                                }
                            } catch (e: Exception) {
                                gatewayError = "Download failed: ${e.message}"
                            } finally {
                                downloadingId = null
                            }
                        }
                    }
                )
            }

            if (connections.isEmpty()) {
                Box(
                    modifier = Modifier
                        .fillMaxSize()
                        .padding(32.dp),
                    contentAlignment = Alignment.Center
                ) {
                    Text(
                        text = "No connections yet.\nTap + to add one.",
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        textAlign = androidx.compose.ui.text.style.TextAlign.Center
                    )
                }
            } else {
                LazyColumn(
                    modifier = Modifier.padding(horizontal = 16.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    items(connections, key = { it.id }) { connection ->
                        ConnectionCard(
                            connection = connection,
                            isActive = connection.id == registry.activeId,
                            onClick = {
                                connectionRepo.setActive(connection.id)
                                onNavigateToConnect()
                            },
                            onDelete = { deleteTarget = connection }
                        )
                    }

                    item { Spacer(modifier = Modifier.height(80.dp)) }
                }
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun ConnectionCard(
    connection: VpnConnection,
    isActive: Boolean,
    onClick: () -> Unit,
    onDelete: () -> Unit
) {
    val dismissState = rememberSwipeToDismissBoxState(
        confirmValueChange = { value ->
            if (value == SwipeToDismissBoxValue.EndToStart) {
                onDelete()
                false // Do not dismiss yet, let dialog confirm
            } else {
                false
            }
        }
    )

    SwipeToDismissBox(
        state = dismissState,
        backgroundContent = {
            Box(
                modifier = Modifier
                    .fillMaxSize()
                    .clip(RoundedCornerShape(12.dp))
                    .background(MaterialTheme.colorScheme.error)
                    .padding(end = 20.dp),
                contentAlignment = Alignment.CenterEnd
            ) {
                Icon(
                    Icons.Filled.Delete,
                    contentDescription = "Delete",
                    tint = Color.White
                )
            }
        },
        enableDismissFromStartToEnd = false,
        enableDismissFromEndToStart = true
    ) {
        Card(
            modifier = Modifier
                .fillMaxWidth()
                .clickable(onClick = onClick),
            colors = CardDefaults.cardColors(
                containerColor = MaterialTheme.colorScheme.surface
            ),
            shape = RoundedCornerShape(12.dp)
        ) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(16.dp),
                verticalAlignment = Alignment.CenterVertically
            ) {
                // Status dot
                Box(
                    modifier = Modifier
                        .size(8.dp)
                        .clip(CircleShape)
                        .background(
                            if (isActive) StatusConnected
                            else MaterialTheme.colorScheme.outline
                        )
                )

                Spacer(modifier = Modifier.width(12.dp))

                // Connection info
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        text = connection.name,
                        style = MaterialTheme.typography.bodyMedium,
                        fontWeight = FontWeight.Medium,
                        color = MaterialTheme.colorScheme.onSurface
                    )
                    Spacer(modifier = Modifier.height(4.dp))
                    Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                        connection.availableProtocols().forEach { protocol ->
                            ProtocolBadge(protocol)
                        }
                    }
                }

                // Delete button
                IconButton(
                    onClick = onDelete,
                    modifier = Modifier.size(32.dp)
                ) {
                    Icon(
                        Icons.Filled.Delete,
                        contentDescription = "Delete",
                        modifier = Modifier.size(18.dp),
                        tint = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }
            }
        }
    }
}

@Composable
private fun ProtocolBadge(protocol: VpnProtocol) {
    val color = when (protocol) {
        VpnProtocol.WIREGUARD -> WireGuardRed
        VpnProtocol.OPENVPN -> OpenVpnOrange
        VpnProtocol.IPSEC -> IpSecBlue
    }

    Box(
        modifier = Modifier
            .clip(RoundedCornerShape(4.dp))
            .background(color.copy(alpha = 0.15f))
            .padding(horizontal = 6.dp, vertical = 2.dp)
    ) {
        Text(
            text = protocol.shortLabel,
            style = MaterialTheme.typography.labelSmall,
            fontWeight = FontWeight.Medium,
            color = color
        )
    }
}

@Composable
private fun GatewayPanel(
    configs: List<RemoteConfigEntry>,
    isLoading: Boolean,
    error: String?,
    downloadingId: Int?,
    onDownload: (RemoteConfigEntry) -> Unit
) {
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 16.dp, vertical = 8.dp)
            .animateContentSize(),
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surfaceVariant
        ),
        shape = RoundedCornerShape(12.dp)
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            Text(
                text = "Gateway Configs",
                style = MaterialTheme.typography.labelMedium,
                fontWeight = FontWeight.SemiBold,
                color = MaterialTheme.colorScheme.onSurface
            )

            Spacer(modifier = Modifier.height(8.dp))

            when {
                isLoading -> {
                    Box(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(16.dp),
                        contentAlignment = Alignment.Center
                    ) {
                        CircularProgressIndicator(
                            modifier = Modifier.size(24.dp),
                            strokeWidth = 2.dp
                        )
                    }
                }

                error != null -> {
                    Text(
                        text = error,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.error
                    )
                }

                configs.isEmpty() -> {
                    Text(
                        text = "No configs available",
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }

                else -> {
                    configs.forEach { entry ->
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .padding(vertical = 4.dp),
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            val protocol = VpnProtocol.fromString(entry.protocol)
                            if (protocol != null) {
                                ProtocolBadge(protocol)
                                Spacer(modifier = Modifier.width(8.dp))
                            }
                            Text(
                                text = entry.peerName,
                                style = MaterialTheme.typography.bodySmall,
                                modifier = Modifier.weight(1f),
                                color = MaterialTheme.colorScheme.onSurface
                            )
                            if (downloadingId == entry.id) {
                                CircularProgressIndicator(
                                    modifier = Modifier.size(18.dp),
                                    strokeWidth = 2.dp
                                )
                            } else {
                                IconButton(
                                    onClick = { onDownload(entry) },
                                    modifier = Modifier.size(32.dp)
                                ) {
                                    Icon(
                                        Icons.Filled.CloudDownload,
                                        contentDescription = "Download",
                                        modifier = Modifier.size(18.dp),
                                        tint = MaterialTheme.colorScheme.primary
                                    )
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}
