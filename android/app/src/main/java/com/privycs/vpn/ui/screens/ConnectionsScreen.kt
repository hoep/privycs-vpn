package com.privycs.vpn.ui.screens

import android.app.Activity
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
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Cloud
import androidx.compose.material.icons.filled.CloudDownload
import androidx.compose.material.icons.filled.Hub
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.QrCodeScanner
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
import androidx.compose.material3.OutlinedTextField
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
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
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
import com.privycs.vpn.util.QrCodePayload
import com.privycs.vpn.util.QrCodeScanner
import com.privycs.vpn.util.parseQrPayload
import kotlinx.coroutines.launch
import java.time.Instant

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ConnectionsScreen(
    onNavigateToAdd: (connectionId: String?) -> Unit,
    onNavigateToConnect: () -> Unit,
    onNavigateToPoolAdd: () -> Unit = {},
    onNavigateToPoolDetail: (poolId: String) -> Unit = {}
) {
    val context = LocalContext.current
    val connectionRepo = remember { PrivycsApp.instance.connectionRepository }
    val settingsRepo = remember { PrivycsApp.instance.settingsRepository }
    val poolRepo = remember { PrivycsApp.instance.poolRepository }
    val registry by connectionRepo.registry.collectAsState()
    val poolRegistry by poolRepo.registry.collectAsState()
    val settings by settingsRepo.settingsFlow.collectAsState(initial = settingsRepo.defaultSettings())
    val connections = registry.connections
    val pools = poolRegistry.pools
    val scope = rememberCoroutineScope()

    var deleteTarget by remember { mutableStateOf<VpnConnection?>(null) }
    var poolDeleteTarget by remember { mutableStateOf<com.privycs.vpn.data.models.Pool?>(null) }
    var renameTarget by remember { mutableStateOf<VpnConnection?>(null) }
    var renameDraft by remember { mutableStateOf("") }
    // Per-connection DNS override draft. Bundled with the rename
    // dialog (now "Edit Connection") so users have one place to
    // tweak both name and DNS without a separate menu entry. Empty
    // = inherit Settings global. Mirrors the per-pool DNS field on
    // PoolDetailHost.
    var renameDnsDraft by remember { mutableStateOf("") }
    // Per-protocol raw-config edit dialog. Desktop client has the same
    // "edit current config" affordance; on Android we open a full-height
    // text editor seeded with the existing configContent and re-parse on
    // save so all derived fields (serverAddress, filename) stay consistent.
    var editProtocolTarget by remember {
        mutableStateOf<Pair<VpnConnection, ProtocolConfig>?>(null)
    }
    var editProtocolDraft by remember { mutableStateOf("") }
    var editProtocolError by remember { mutableStateOf<String?>(null) }
    var showGateway by remember { mutableStateOf(false) }
    var gatewayConfigs by remember { mutableStateOf<List<RemoteConfigEntry>>(emptyList()) }
    var gatewayLoading by remember { mutableStateOf(false) }
    var gatewayError by remember { mutableStateOf<String?>(null) }
    var downloadingId by remember { mutableStateOf<Int?>(null) }

    // Edit-Connection dialog. Combines rename + per-connection DNS
    // override. The DNS field is the single-connection equivalent
    // of PoolDetailHost's per-pool DNS field; resolution priority
    // is connection > pool > global.
    if (renameTarget != null) {
        val dnsInvalid = remember(renameDnsDraft) {
            com.privycs.vpn.util.DnsValidator.invalidEntries(renameDnsDraft)
        }
        AlertDialog(
            onDismissRequest = { renameTarget = null },
            title = { Text(stringResource(R.string.connections_edit_dialog_title)) },
            text = {
                Column {
                    OutlinedTextField(
                        value = renameDraft,
                        onValueChange = { renameDraft = it },
                        label = { Text(stringResource(R.string.connections_name_label)) },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth()
                    )
                    Spacer(modifier = Modifier.height(12.dp))
                    OutlinedTextField(
                        value = renameDnsDraft,
                        onValueChange = { renameDnsDraft = it },
                        label = { Text(stringResource(R.string.connections_dns_override_label)) },
                        placeholder = { Text(stringResource(R.string.connections_dns_override_placeholder)) },
                        singleLine = true,
                        isError = dnsInvalid.isNotEmpty(),
                        modifier = Modifier.fillMaxWidth(),
                        supportingText = {
                            when {
                                dnsInvalid.isNotEmpty() -> Text(
                                    stringResource(R.string.connections_dns_invalid, dnsInvalid.joinToString(", ")),
                                    color = MaterialTheme.colorScheme.error
                                )
                                renameDnsDraft.isBlank() -> Text(
                                    stringResource(R.string.connections_dns_empty_hint)
                                )
                                else -> Text(
                                    stringResource(R.string.connections_dns_override_hint)
                                )
                            }
                        }
                    )
                    // Per-connection preset dropdown (v0.9.14.4) —
                    // mirror of the global Settings picker so users
                    // can fill in Cloudflare / Quad9 / etc. with one
                    // tap instead of typing the IPs manually.
                    Spacer(modifier = Modifier.height(8.dp))
                    com.privycs.vpn.ui.components.DnsPresetPicker(
                        onPick = { renameDnsDraft = it },
                    )
                }
            },
            confirmButton = {
                TextButton(
                    enabled = renameDraft.trim().isNotEmpty() && dnsInvalid.isEmpty(),
                    onClick = {
                        val target = renameTarget!!
                        connectionRepo.rename(target.id, renameDraft.trim())
                        connectionRepo.updateDnsOverride(target.id, renameDnsDraft.trim())
                        renameTarget = null
                    }
                ) {
                    Text(stringResource(R.string.connections_save))
                }
            },
            dismissButton = {
                TextButton(onClick = { renameTarget = null }) {
                    Text(stringResource(R.string.connections_cancel))
                }
            }
        )
    }

    // Edit-protocol config dialog (raw text editor). Reuses ConfigParser so
    // saving a tweaked Wireguard/OpenVPN config re-derives serverAddress etc.
    // IPSec is skipped because the configContent is a binary-encoded bundle
    // (mobileconfig XML + base64 PKCS#12), not human-editable plain text.
    if (editProtocolTarget != null) {
        val (editConn, editPc) = editProtocolTarget!!
        AlertDialog(
            onDismissRequest = { editProtocolTarget = null },
            title = { Text(stringResource(R.string.connections_edit_protocol_title, editPc.protocol.label)) },
            text = {
                Column(modifier = Modifier.fillMaxWidth()) {
                    OutlinedTextField(
                        value = editProtocolDraft,
                        onValueChange = {
                            editProtocolDraft = it
                            editProtocolError = null
                        },
                        modifier = Modifier
                            .fillMaxWidth()
                            .heightIn(min = 200.dp, max = 400.dp),
                        textStyle = MaterialTheme.typography.bodySmall.copy(
                            fontFamily = com.privycs.vpn.ui.theme.FiraCodeFamily
                        ),
                        label = { Text(editPc.filename) },
                        singleLine = false
                    )
                    if (editProtocolError != null) {
                        Spacer(modifier = Modifier.height(8.dp))
                        Text(
                            text = editProtocolError!!,
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.error
                        )
                    }
                }
            },
            confirmButton = {
                TextButton(
                    enabled = editProtocolDraft.isNotBlank(),
                    onClick = {
                        val rebuilt = ConfigParser.buildProtocolConfig(
                            editProtocolDraft,
                            editPc.filename
                        )
                        if (rebuilt == null || rebuilt.protocol != editPc.protocol) {
                            editProtocolError = context.getString(
                                R.string.connections_edit_protocol_parse_error,
                                editPc.protocol.label
                            )
                            return@TextButton
                        }
                        connectionRepo.addOrUpdate(editConn.id, editConn.name, rebuilt)
                        editProtocolTarget = null
                        editProtocolDraft = ""
                        editProtocolError = null
                    }
                ) {
                    Text(stringResource(R.string.connections_save))
                }
            },
            dismissButton = {
                TextButton(onClick = {
                    editProtocolTarget = null
                    editProtocolDraft = ""
                    editProtocolError = null
                }) {
                    Text(stringResource(R.string.connections_cancel))
                }
            }
        )
    }

    // Delete confirmation dialog
    if (deleteTarget != null) {
        AlertDialog(
            onDismissRequest = { deleteTarget = null },
            title = { Text(stringResource(R.string.connections_delete_dialog_title)) },
            text = { Text(stringResource(R.string.connections_delete_dialog_message, deleteTarget!!.name)) },
            confirmButton = {
                TextButton(onClick = {
                    connectionRepo.delete(deleteTarget!!.id)
                    deleteTarget = null
                }) {
                    Text(stringResource(R.string.connections_delete), color = MaterialTheme.colorScheme.error)
                }
            },
            dismissButton = {
                TextButton(onClick = { deleteTarget = null }) {
                    Text(stringResource(R.string.connections_cancel))
                }
            }
        )
    }

    // Pool delete confirmation (list-level; mirrors the connection dialog above).
    if (poolDeleteTarget != null) {
        AlertDialog(
            onDismissRequest = { poolDeleteTarget = null },
            title = { Text(stringResource(R.string.pooldetail_delete_dialog_title)) },
            text = { Text(stringResource(R.string.pooldetail_delete_dialog_text)) },
            confirmButton = {
                TextButton(onClick = {
                    val id = poolDeleteTarget!!.id
                    poolDeleteTarget = null
                    scope.launch { poolRepo.delete(id) }
                }) {
                    Text(stringResource(R.string.pooldetail_delete), color = MaterialTheme.colorScheme.error)
                }
            },
            dismissButton = {
                TextButton(onClick = { poolDeleteTarget = null }) {
                    Text(stringResource(R.string.connections_cancel))
                }
            }
        )
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        stringResource(R.string.connections_title),
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.SemiBold
                    )
                },
                actions = {
                    // QR code scan. Sits next to the gateway cloud
                    // icon so the two import-from-outside actions are
                    // grouped visually. On success we route WireGuard
                    // configs straight into the registry (no extra
                    // screen to confirm name — we derive one); Privycs
                    // enrollment URLs store the gateway credentials
                    // and open the gateway panel so the user can pick
                    // which protocol to import.
                    IconButton(onClick = {
                        val act = context as? Activity ?: return@IconButton
                        scope.launch {
                            try {
                                val raw = QrCodeScanner.scan(act) ?: return@launch
                                when (val payload = parseQrPayload(raw)) {
                                    is QrCodePayload.WireGuardConfig -> {
                                        // Derive a per-config filename
                                        // from the scanned endpoint so
                                        // two different QR codes don't
                                        // both land as "scanned.conf"
                                        // (which used to make the 2nd
                                        // scan overwrite the 1st). Same
                                        // QR re-scanned → same filename
                                        // + same content → addOrUpdate
                                        // updates in place (idempotent).
                                        val probe = ConfigParser.buildProtocolConfig(
                                            payload.content, "scanned.conf"
                                        )
                                        if (probe != null) {
                                            val host = probe.serverAddress
                                                .substringBefore(':').trim()
                                            val filename = if (host.isNotBlank())
                                                "wg-${host.replace(Regex("[^A-Za-z0-9_.-]"), "_")}.conf"
                                            else "scanned.conf"
                                            val pc = probe.copy(filename = filename)
                                            val name = ConfigParser.deriveConnectionName(filename)
                                            // Each scan = its own config
                                            // in the active (chosen)
                                            // connection when there is
                                            // one; otherwise a fresh
                                            // connection.
                                            val targetId = connectionRepo.getActive()?.id
                                            connectionRepo.addOrUpdate(targetId, name, pc)
                                        } else {
                                            gatewayError = context.getString(R.string.connections_qr_wireguard_invalid)
                                        }
                                    }
                                    is QrCodePayload.PrivycsEnrollment -> {
                                        if (!payload.gatewayUrl.isNullOrBlank() &&
                                            !payload.apiKey.isNullOrBlank()) {
                                            settingsRepo.updateGatewayConfig(
                                                payload.gatewayUrl,
                                                payload.apiKey
                                            )
                                        }
                                        // Force-open the gateway panel
                                        // and trigger a config fetch so
                                        // the user sees what's available.
                                        showGateway = true
                                        gatewayConfigs = emptyList()
                                        gatewayLoading = true
                                        gatewayError = null
                                        try {
                                            val url = payload.gatewayUrl ?: settings.gatewayUrl
                                            val key = payload.apiKey ?: settings.apiKey
                                            val client = GatewayApiClient(url, key)
                                            val profile = client.fetchProfile()
                                            gatewayConfigs = profile.configs
                                            client.close()
                                        } catch (e: Exception) {
                                            gatewayError = e.message
                                        } finally {
                                            gatewayLoading = false
                                        }
                                    }
                                    is QrCodePayload.Unknown -> {
                                        gatewayError = context.getString(R.string.connections_qr_unrecognised)
                                    }
                                }
                            } catch (e: Exception) {
                                gatewayError = context.getString(R.string.connections_qr_scan_failed, e.message ?: "")
                            }
                        }
                    }) {
                        Icon(
                            Icons.Filled.QrCodeScanner,
                            contentDescription = stringResource(R.string.connections_scan_qr_cd),
                            tint = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }

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
                                contentDescription = stringResource(R.string.connections_gateway_cd),
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
                onClick = { onNavigateToAdd(null) },
                containerColor = MaterialTheme.colorScheme.primary
            ) {
                Icon(Icons.Filled.Add, contentDescription = stringResource(R.string.connections_add_connection_cd))
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

                                // v0.9.15.30: stableId-based filename + pc.id.
                                // See AddConnectionScreen for the full rationale.
                                val stableId = "gw-${entry.protocol}-${entry.id}"
                                val filename = when (entry.protocol) {
                                    "wireguard", "amneziawg" -> "$stableId.conf"
                                    "openvpn" -> "$stableId.ovpn"
                                    else -> "$stableId.conf"
                                }

                                val parsed = ConfigParser.buildProtocolConfig(configContent, filename)
                                val protocolConfig = parsed?.copy(id = stableId)
                                if (protocolConfig != null) {
                                    connectionRepo.addOrUpdate(null, entry.peerName, protocolConfig)
                                }
                            } catch (e: Exception) {
                                gatewayError = context.getString(R.string.connections_download_failed, e.message ?: "")
                            } finally {
                                downloadingId = null
                            }
                        }
                    }
                )
            }

            if (connections.isEmpty() && pools.isEmpty()) {
                Box(
                    modifier = Modifier
                        .fillMaxSize()
                        .padding(32.dp),
                    contentAlignment = Alignment.Center
                ) {
                    Column(horizontalAlignment = Alignment.CenterHorizontally) {
                        Text(
                            text = stringResource(R.string.connections_empty),
                            style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            textAlign = androidx.compose.ui.text.style.TextAlign.Center
                        )
                        Spacer(modifier = Modifier.height(12.dp))
                        TextButton(onClick = onNavigateToPoolAdd) {
                            Icon(Icons.Filled.Add, contentDescription = null)
                            Spacer(modifier = Modifier.width(4.dp))
                            Text(stringResource(R.string.connections_add_pool))
                        }
                        Text(
                            stringResource(R.string.connections_empty_add_single_hint),
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                }
            } else {
                LazyColumn(
                    modifier = Modifier.padding(horizontal = 16.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    // Pools section - shown above the singles list.
                    // Tappable cards open the pool detail. The
                    // header carries an "Add Pool" affordance.
                    item {
                        PoolsSectionHeader(
                            poolCount = pools.size,
                            onAddPool = onNavigateToPoolAdd
                        )
                    }
                    items(pools, key = { "pool:${it.id}" }) { p ->
                        PoolListCard(
                            pool = p,
                            isActive = p.id == poolRegistry.activeId,
                            // Card body tap = activate + connect.
                            // Mirrors single-connection card UX where
                            // tapping the row makes that connection
                            // the active target. Edit / detail view
                            // is reachable via the small chevron
                            // icon on the right side of the card.
                            onTap = {
                                // Funnel through VpnServiceManager
                                // .switchActivePool - it handles the
                                // single-clear, pool-set, tentative
                                // status update, and disconnect-if-
                                // connected. Pool tap NEVER auto-
                                // connects: connect is owned by COD
                                // (when on) or the explicit Connect
                                // button (when off).
                                com.privycs.vpn.service.VpnServiceManager
                                    .getInstance(context)
                                    .switchActivePool(p.id)
                                onNavigateToConnect()
                            },
                            onEdit = { onNavigateToPoolDetail(p.id) },
                            onDelete = { poolDeleteTarget = p }
                        )
                    }
                    if (pools.isNotEmpty()) {
                        item {
                            // Visual separator between pools and singles.
                            Spacer(modifier = Modifier.height(8.dp))
                            Text(
                                stringResource(R.string.connections_section_header),
                                style = MaterialTheme.typography.titleSmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                modifier = Modifier.padding(top = 8.dp, bottom = 4.dp)
                            )
                        }
                    }

                    items(connections, key = { it.id }) { connection ->
                        ConnectionCard(
                            connection = connection,
                            isActive = connection.id == registry.activeId,
                            onClick = {
                                if (connection.id != registry.activeId) {
                                    val vpnManager = com.privycs.vpn.service.VpnServiceManager
                                        .getInstance(context)
                                    // Only warn about KS blocking when a
                                    // reconnect will actually be attempted
                                    // (tunnel was up, or COD wants one up).
                                    val willReconnect = vpnManager.switchActiveConnection(connection.id)
                                    if (willReconnect &&
                                        com.privycs.vpn.util.KillSwitchManager.isArmed()
                                    ) {
                                        android.widget.Toast.makeText(
                                            context,
                                            context.getString(R.string.connections_kill_switch_warning),
                                            android.widget.Toast.LENGTH_LONG,
                                        ).show()
                                    }
                                }
                                onNavigateToConnect()
                            },
                            onDelete = { deleteTarget = connection },
                            onRename = {
                                renameTarget = connection
                                renameDraft = connection.name
                                renameDnsDraft = connection.dnsOverride
                            },
                            onAddProtocol = { onNavigateToAdd(connection.id) },
                            onRemoveConfig = { configId ->
                                connectionRepo.removeConfig(connection.id, configId)
                            },
                            onEditConfig = { configId ->
                                connection.getConfigById(configId)?.let { pc ->
                                    editProtocolTarget = connection to pc
                                    editProtocolDraft = pc.configContent
                                    editProtocolError = null
                                }
                            }
                        )
                    }

                    item { Spacer(modifier = Modifier.height(80.dp)) }
                }
            }
        }
    }
}

@Composable
private fun PoolsSectionHeader(
    poolCount: Int,
    onAddPool: () -> Unit
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(top = 8.dp, bottom = 4.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Text(
            if (poolCount == 0) stringResource(R.string.connections_pools_header)
            else stringResource(R.string.connections_pools_header_count, poolCount),
            style = MaterialTheme.typography.titleSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.weight(1f)
        )
        TextButton(onClick = onAddPool) {
            Icon(Icons.Filled.Add, contentDescription = null,
                modifier = Modifier.size(16.dp))
            Spacer(modifier = Modifier.width(4.dp))
            Text(stringResource(R.string.connections_add_pool))
        }
    }
}

@Composable
private fun PoolListCard(
    pool: com.privycs.vpn.data.models.Pool,
    isActive: Boolean,
    onTap: () -> Unit,
    onEdit: () -> Unit,
    onDelete: () -> Unit
) {
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(onClick = onTap),
        colors = CardDefaults.cardColors(
            containerColor = if (isActive)
                MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.3f)
            else MaterialTheme.colorScheme.surface
        )
    ) {
        Row(
            modifier = Modifier.padding(12.dp),
            verticalAlignment = Alignment.CenterVertically
        ) {
            Icon(
                Icons.Filled.Hub,
                contentDescription = null,
                tint = MaterialTheme.colorScheme.primary,
                modifier = Modifier.size(28.dp)
            )
            Spacer(modifier = Modifier.width(12.dp))
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    pool.name,
                    style = MaterialTheme.typography.titleSmall,
                    fontWeight = FontWeight.SemiBold
                )
                Text(
                    stringResource(
                        R.string.connections_pool_summary,
                        pool.policy.displayName,
                        pluralStringResource(
                            R.plurals.connections_pool_server_count,
                            pool.members.size,
                            pool.members.size
                        )
                    ),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
            }
            if (isActive) {
                androidx.compose.material3.Badge(
                    containerColor = MaterialTheme.colorScheme.primary,
                    contentColor = MaterialTheme.colorScheme.onPrimary
                ) {
                    Text(stringResource(R.string.connections_active_badge),
                        style = MaterialTheme.typography.labelSmall)
                }
                Spacer(modifier = Modifier.width(6.dp))
            }
            // Edit / detail icon - small, right side. Card-body tap
            // activates the pool; this icon opens the pool detail
            // for editing without firing connect.
            IconButton(
                onClick = onEdit,
                modifier = Modifier.size(36.dp)
            ) {
                Icon(
                    Icons.Filled.Edit,
                    contentDescription = stringResource(R.string.connections_edit_pool_cd),
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.size(18.dp)
                )
            }
            // Delete from the list (parity with connection cards + desktop) so a
            // pool is removable without opening its detail screen.
            IconButton(
                onClick = onDelete,
                modifier = Modifier.size(36.dp)
            ) {
                Icon(
                    Icons.Filled.Delete,
                    contentDescription = stringResource(R.string.pooldetail_delete),
                    tint = MaterialTheme.colorScheme.error,
                    modifier = Modifier.size(18.dp)
                )
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class, androidx.compose.foundation.layout.ExperimentalLayoutApi::class)
@Composable
private fun ConnectionCard(
    connection: VpnConnection,
    isActive: Boolean,
    onClick: () -> Unit,
    onDelete: () -> Unit,
    onRename: () -> Unit,
    onAddProtocol: () -> Unit,
    onRemoveConfig: (configId: String) -> Unit,
    onEditConfig: (configId: String) -> Unit
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
                    contentDescription = stringResource(R.string.connections_delete_cd),
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
                    // One badge per ProtocolConfig — multi-config-per-
                    // protocol means a connection may hold e.g. two WG
                    // entries (UDP+TCP) and the user needs to see+manage
                    // each independently. Wraps to multiple lines when
                    // the list gets long.
                    androidx.compose.foundation.layout.FlowRow(
                        horizontalArrangement = Arrangement.spacedBy(4.dp),
                        verticalArrangement = Arrangement.spacedBy(4.dp)
                    ) {
                        connection.orderedConfigs().forEach { cfg ->
                            val canRemove = connection.protocols.size > 1
                            ProtocolBadge(
                                protocol = cfg.protocol,
                                onRemove = if (canRemove) {
                                    { onRemoveConfig(cfg.id) }
                                } else null,
                                onEdit = { onEditConfig(cfg.id) },
                                // v1.0.5.17: badge shows endpoint host
                                // (port stripped) instead of being a
                                // logo-only chip. Connect-screen badges
                                // stay logo-only (no endpoint passed
                                // there). The gateway-browser badge
                                // further below also stays logo-only.
                                endpoint = cfg.serverAddress,
                            )
                        }
                        // Add-config button — always visible (no
                        // upper bound on configs-per-connection now
                        // that multi-of-same-protocol is allowed).
                        IconButton(
                            onClick = onAddProtocol,
                            modifier = Modifier.size(24.dp)
                        ) {
                            Icon(
                                Icons.Filled.Add,
                                contentDescription = stringResource(R.string.connections_add_config_cd),
                                modifier = Modifier.size(16.dp),
                                tint = MaterialTheme.colorScheme.primary
                            )
                        }
                    }

                    // VPN-IP per protocol-config — last-known inner
                    // address. WireGuard's address comes from the
                    // .conf at import (always current). OpenVPN +
                    // IPSec are populated post-connect once the
                    // server pushes one and PrivycsVpnService's
                    // poll-loop persists it via
                    // ConnectionRepository.updateLocalAddress. Hidden
                    // when none of the protocols carry an address yet
                    // (fresh import that never connected).
                    val vpnIps = connection.protocols.mapNotNull { pc ->
                        if (pc.localAddress.isBlank()) null
                        else "${pc.protocol.shortLabel}: ${pc.localAddress}"
                    }
                    if (vpnIps.isNotEmpty()) {
                        Spacer(modifier = Modifier.height(4.dp))
                        Text(
                            text = stringResource(
                                R.string.connections_vpn_ip,
                                vpnIps.joinToString(" · ")
                            ),
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                }

                // Rename button
                IconButton(
                    onClick = onRename,
                    modifier = Modifier.size(32.dp)
                ) {
                    Icon(
                        Icons.Filled.Edit,
                        contentDescription = stringResource(R.string.connections_rename_cd),
                        modifier = Modifier.size(18.dp),
                        tint = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }

                // Delete button
                IconButton(
                    onClick = onDelete,
                    modifier = Modifier.size(32.dp)
                ) {
                    Icon(
                        Icons.Filled.Delete,
                        contentDescription = stringResource(R.string.connections_delete_cd),
                        modifier = Modifier.size(18.dp),
                        tint = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }
            }
        }
    }
}

// v1.0.5.17: ProtocolBadge now optionally accepts an endpoint string
// that renders next to the icon. Used by the connections-list view to
// show server-host (port stripped) instead of being a logo-only chip.
// The Connect-screen ProtocolBadges deliberately keep the logo-only
// look — per user direction this change is connections-list-only.
//
// endpointShort strips the port for compact display:
//   "host"          → "host"
//   "host:port"     → "host"
//   "[ipv6]:port"   → "[ipv6]"
//   ""              → "" (badge stays logo-only)
private fun endpointShort(serverAddress: String?): String {
    val s = (serverAddress ?: "").trim()
    if (s.isEmpty()) return ""
    if (s.startsWith("[")) {
        val close = s.indexOf(']')
        return if (close > 0) s.substring(0, close + 1) else s
    }
    val lastColon = s.lastIndexOf(':')
    return if (lastColon == -1) s else s.substring(0, lastColon)
}

@Composable
private fun ProtocolBadge(
    protocol: VpnProtocol,
    onRemove: (() -> Unit)? = null,
    onEdit: (() -> Unit)? = null,
    endpoint: String? = null
) {
    val color = when (protocol) {
        VpnProtocol.AMNEZIAWG -> com.privycs.vpn.ui.theme.AmneziaWgIndigo
        VpnProtocol.WIREGUARD -> WireGuardRed
        VpnProtocol.OPENVPN -> OpenVpnOrange
        VpnProtocol.IPSEC -> IpSecBlue
    }
    val iconRes = when (protocol) {
        // AWG → mono variant so the Icon tint cascade applies
        // (matches WG/OVPN/IPSec). The multi-colour PNG would
        // ignore tint and clash with the tinted siblings.
        VpnProtocol.AMNEZIAWG -> com.privycs.vpn.R.drawable.ic_protocol_amneziawg
        VpnProtocol.WIREGUARD -> com.privycs.vpn.R.drawable.ic_protocol_wireguard
        VpnProtocol.OPENVPN   -> com.privycs.vpn.R.drawable.ic_protocol_openvpn
        VpnProtocol.IPSEC     -> com.privycs.vpn.R.drawable.ic_protocol_strongswan
    }

    // v1.0.5.17: brand icon + optional endpoint host text. When the
    // caller passes no endpoint (default), the badge stays the
    // previous logo-only chip (Connect-screen + gateway-browser
    // call sites). Connections-list call sites pass the server
    // address so the badge shows logo + host (port stripped, see
    // endpointShort helper above).
    val endpointDisplay = endpointShort(endpoint)
    Row(
        modifier = Modifier
            .clip(RoundedCornerShape(4.dp))
            .background(color.copy(alpha = 0.15f))
            .padding(horizontal = 6.dp, vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Icon(
            painter = androidx.compose.ui.res.painterResource(id = iconRes),
            contentDescription = protocol.label,
            tint = color,
            modifier = Modifier.size(20.dp)
        )
        if (endpointDisplay.isNotEmpty()) {
            Spacer(modifier = Modifier.width(4.dp))
            Text(
                text = endpointDisplay,
                style = MaterialTheme.typography.labelSmall,
                color = color,
                maxLines = 1,
                overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis,
                modifier = Modifier.widthIn(max = 160.dp)
            )
        }
        if (onEdit != null) {
            Spacer(modifier = Modifier.width(2.dp))
            IconButton(
                onClick = onEdit,
                modifier = Modifier.size(18.dp)
            ) {
                Icon(
                    Icons.Filled.Edit,
                    contentDescription = stringResource(R.string.connections_edit_config_cd, protocol.shortLabel),
                    modifier = Modifier.size(12.dp),
                    tint = color
                )
            }
        }
        if (onRemove != null) {
            Spacer(modifier = Modifier.width(2.dp))
            IconButton(
                onClick = onRemove,
                modifier = Modifier.size(14.dp)
            ) {
                Icon(
                    Icons.Filled.Close,
                    contentDescription = stringResource(R.string.connections_remove_config_cd, protocol.shortLabel),
                    modifier = Modifier.size(10.dp),
                    tint = color
                )
            }
        }
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
                text = stringResource(R.string.connections_gateway_configs_header),
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
                        text = stringResource(R.string.connections_gateway_no_configs),
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }

                else -> {
                    // Gateway users often have 20+ configs. The parent Column
                    // is not scrollable, so a plain forEach here overflows the
                    // screen on Android and the user cannot reach the rows at
                    // the bottom. Wrap in heightIn+verticalScroll so the panel
                    // scrolls internally with a sensible max height.
                    Column(
                        modifier = Modifier
                            .heightIn(max = 360.dp)
                            .verticalScroll(rememberScrollState())
                    ) {
                        configs.forEach { entry ->
                            Row(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .padding(vertical = 4.dp),
                                verticalAlignment = Alignment.CenterVertically
                            ) {
                                // Map server-side AWG enrollments
                                // (protocol="wireguard" +
                                // obfuscation_enabled=true) to the
                                // AMNEZIAWG badge so the user sees
                                // what they will actually get. Matches
                                // the equivalent logic in
                                // AddConnectionScreen.kt's gateway
                                // listing — Bug 3 fix.
                                val protocol = if ((entry.protocol == "wireguard" && entry.obfuscationEnabled) ||
                                    entry.protocol == "amneziawg"
                                ) VpnProtocol.AMNEZIAWG
                                else VpnProtocol.fromString(entry.protocol)
                                if (protocol != null) {
                                    ProtocolBadge(protocol)
                                    Spacer(modifier = Modifier.width(8.dp))
                                }
                                // Two-line peer description matching
                                // AddConnectionScreen — Bug 4 fix.
                                // Pre-v0.9.15.10 we showed only the
                                // peer name in the top-level gateway
                                // icon path; the user couldn't tell
                                // which .conf/.ovpn/.sswan was about
                                // to be downloaded.
                                Column(modifier = Modifier.weight(1f)) {
                                    Text(
                                        text = entry.peerName,
                                        style = MaterialTheme.typography.bodySmall,
                                        color = MaterialTheme.colorScheme.onSurface
                                    )
                                    if (entry.interfaceName.isNotBlank() || entry.vpnIp.isNotBlank()) {
                                        Text(
                                            text = listOf(entry.interfaceName, entry.vpnIp)
                                                .filter { it.isNotBlank() }
                                                .joinToString(" / "),
                                            style = MaterialTheme.typography.labelSmall,
                                            color = MaterialTheme.colorScheme.onSurfaceVariant
                                        )
                                    }
                                }
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
                                            contentDescription = stringResource(R.string.connections_download_cd),
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
}
