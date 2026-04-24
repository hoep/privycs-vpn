package com.privycs.vpn.ui.screens

import android.app.Activity
import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
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
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Cloud
import androidx.compose.material.icons.filled.CloudDownload
import androidx.compose.material.icons.filled.FileOpen
import androidx.compose.material.icons.filled.Link
import androidx.compose.material.icons.filled.QrCodeScanner
import androidx.compose.material.icons.filled.UploadFile
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
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
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.api.GatewayApiClient
import com.privycs.vpn.config.ConfigParser
import com.privycs.vpn.data.models.RemoteConfigEntry
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.util.QrCodePayload
import com.privycs.vpn.util.QrCodeScanner
import com.privycs.vpn.util.parseQrPayload
import kotlinx.coroutines.launch

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AddConnectionScreen(
    connectionId: String? = null,
    onConnectionAdded: () -> Unit,
    onBack: () -> Unit
) {
    val context = LocalContext.current
    val connectionRepo = remember { PrivycsApp.instance.connectionRepository }
    val settingsRepo = remember { PrivycsApp.instance.settingsRepository }
    val settings by settingsRepo.settingsFlow.collectAsState(initial = settingsRepo.defaultSettings())
    val scope = rememberCoroutineScope()

    // "Add protocol to X" mode is active when a connectionId is provided via
    // navigation. Name input is hidden in that mode (we keep the existing
    // connection name) and the gateway panel filters to missing protocols.
    val targetConnection = connectionId?.let { connectionRepo.getById(it) }

    var selectedUri by remember { mutableStateOf<Uri?>(null) }
    var selectedFilename by remember { mutableStateOf("") }
    var fileContent by remember { mutableStateOf("") }
    var detectedProtocol by remember { mutableStateOf<VpnProtocol?>(null) }
    var connectionName by remember { mutableStateOf(targetConnection?.name ?: "") }
    var importError by remember { mutableStateOf<String?>(null) }
    var importSuccess by remember { mutableStateOf(false) }

    // Gateway panel state (in-screen, unlike the desktop where it's in a
    // drawer). Fetched lazily on first open and filtered by missing
    // protocols when the screen targets an existing connection.
    var showGateway by remember { mutableStateOf(false) }
    var gatewayConfigs by remember { mutableStateOf<List<RemoteConfigEntry>>(emptyList()) }
    var gatewayLoading by remember { mutableStateOf(false) }
    var gatewayError by remember { mutableStateOf<String?>(null) }
    var downloadingKey by remember { mutableStateOf<String?>(null) }

    val filePicker = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.OpenDocument()
    ) { uri ->
        if (uri == null) return@rememberLauncherForActivityResult

        selectedUri = uri
        importError = null
        importSuccess = false

        try {
            // Get filename from URI
            val cursor = context.contentResolver.query(uri, null, null, null, null)
            val nameIndex = cursor?.getColumnIndex(android.provider.OpenableColumns.DISPLAY_NAME)
            cursor?.moveToFirst()
            selectedFilename = if (nameIndex != null && nameIndex >= 0) {
                // nameIndex != null implies cursor != null (nameIndex
                // is derived from cursor?.getColumnIndex), so the
                // Kotlin compiler smart-casts cursor here - safe call
                // is redundant.
                cursor.getString(nameIndex) ?: "unknown.conf"
            } else {
                uri.lastPathSegment ?: "unknown.conf"
            }
            cursor?.close()

            // Read content
            val inputStream = context.contentResolver.openInputStream(uri)
            fileContent = inputStream?.bufferedReader()?.readText() ?: ""
            inputStream?.close()

            if (fileContent.isBlank()) {
                importError = "File is empty"
                return@rememberLauncherForActivityResult
            }

            // Auto-detect protocol
            detectedProtocol = ConfigParser.detectProtocol(fileContent, selectedFilename)
            if (detectedProtocol == null) {
                importError = "Unable to detect VPN protocol. Supported: .conf (WireGuard), .ovpn (OpenVPN), .sswan/.mobileconfig (IPSec)"
                return@rememberLauncherForActivityResult
            }

            // Suggest connection name
            if (connectionName.isBlank()) {
                connectionName = ConfigParser.deriveConnectionName(selectedFilename)
            }
        } catch (e: Exception) {
            importError = "Failed to read file: ${e.message}"
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        text = if (targetConnection != null)
                            "Add Protocol to ${targetConnection.name}"
                        else "Add Connection",
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.SemiBold
                    )
                },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                actions = {
                    // QR code scan — camera-based import. Works for raw
                    // WireGuard configs (wg-quick standard QR) out of
                    // the box; for OpenVPN/IPSec it accepts a
                    // privycs://enroll URL that points back at the
                    // gateway the QR was generated from. GMS
                    // CodeScanner handles the camera permission in its
                    // own process, so we don't need CAMERA in the
                    // manifest.
                    IconButton(onClick = {
                        val act = context as? Activity ?: return@IconButton
                        scope.launch {
                            importError = null
                            try {
                                val raw = QrCodeScanner.scan(act) ?: return@launch
                                when (val payload = parseQrPayload(raw)) {
                                    is QrCodePayload.WireGuardConfig -> {
                                        fileContent = payload.content
                                        selectedFilename = "scanned.conf"
                                        detectedProtocol = ConfigParser.detectProtocol(
                                            payload.content, selectedFilename
                                        )
                                        if (detectedProtocol == null) {
                                            importError = "Scanned config did not validate as WireGuard"
                                        } else if (connectionName.isBlank()) {
                                            connectionName = ConfigParser.deriveConnectionName(selectedFilename)
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
                                        showGateway = true
                                        if (gatewayConfigs.isEmpty() && !gatewayLoading) {
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
                                    }
                                    is QrCodePayload.Unknown -> {
                                        importError = "QR code content not recognised as a VPN config or Privycs enrollment URL"
                                    }
                                }
                            } catch (e: Exception) {
                                importError = "QR scan failed: ${e.message}"
                            }
                        }
                    }) {
                        Icon(
                            Icons.Filled.QrCodeScanner,
                            contentDescription = "Scan QR code",
                            tint = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }

                    // Gateway toggle — only usable with API key configured.
                    if (settings.gatewayUrl.isNotBlank() && settings.apiKey.isNotBlank()) {
                        IconButton(onClick = {
                            showGateway = !showGateway
                            if (showGateway && gatewayConfigs.isEmpty() && !gatewayLoading) {
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
        }
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .padding(16.dp),
            horizontalAlignment = Alignment.CenterHorizontally
        ) {
            // Target indicator when adding a protocol to an existing connection.
            if (targetConnection != null) {
                Card(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(bottom = 12.dp),
                    colors = CardDefaults.cardColors(
                        containerColor = MaterialTheme.colorScheme.primary.copy(alpha = 0.08f)
                    ),
                    shape = RoundedCornerShape(12.dp)
                ) {
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(12.dp),
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Icon(
                            Icons.Filled.Link,
                            contentDescription = null,
                            modifier = Modifier.size(20.dp),
                            tint = MaterialTheme.colorScheme.primary
                        )
                        Spacer(modifier = Modifier.size(8.dp))
                        Column(modifier = Modifier.weight(1f)) {
                            Text(
                                text = "Adding to existing connection",
                                style = MaterialTheme.typography.labelMedium,
                                color = MaterialTheme.colorScheme.primary,
                                fontWeight = FontWeight.SemiBold
                            )
                            Text(
                                text = "${targetConnection.name} — currently has " +
                                    targetConnection.availableProtocols().joinToString(", ") { it.shortLabel },
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurface
                            )
                        }
                    }
                }
            }

            // Gateway panel (expandable). Filters to protocols missing from
            // the target connection when in "add protocol" mode.
            if (showGateway) {
                val visibleGatewayConfigs = remember(gatewayConfigs, targetConnection?.id) {
                    if (targetConnection == null) {
                        gatewayConfigs
                    } else {
                        val have = targetConnection.availableProtocols().toSet()
                        gatewayConfigs.filter { entry ->
                            val p = VpnProtocol.fromString(entry.protocol)
                            p != null && p !in have
                        }
                    }
                }
                GatewayPanel(
                    configs = visibleGatewayConfigs,
                    isLoading = gatewayLoading,
                    error = gatewayError,
                    downloadingKey = downloadingKey,
                    emptyMessage = if (targetConnection != null)
                        "All protocols of this user already imported for \"${targetConnection.name}\""
                    else "No configs available",
                    onDownload = { entry ->
                        val key = "${entry.protocol}-${entry.id}"
                        scope.launch {
                            downloadingKey = key
                            gatewayError = null
                            try {
                                val client = GatewayApiClient(settings.gatewayUrl, settings.apiKey)
                                val configContent = client.fetchConfig(entry.protocol, entry.id)
                                client.close()

                                val filename = when (entry.protocol) {
                                    "wireguard" -> "${entry.peerName}.conf"
                                    "openvpn" -> "${entry.peerName}.ovpn"
                                    "ipsec" -> "${entry.peerName}.sswan"
                                    else -> "${entry.peerName}.conf"
                                }

                                val protocolConfig = ConfigParser.buildProtocolConfig(configContent, filename)
                                if (protocolConfig != null) {
                                    val targetName = targetConnection?.name ?: entry.peerName
                                    connectionRepo.addOrUpdate(targetConnection?.id, targetName, protocolConfig)
                                    importSuccess = true
                                    onConnectionAdded()
                                } else {
                                    gatewayError = "Failed to parse config from gateway"
                                }
                            } catch (e: Exception) {
                                gatewayError = "Download failed: ${e.message}"
                            } finally {
                                downloadingKey = null
                            }
                        }
                    }
                )
                Spacer(modifier = Modifier.height(16.dp))
            }

            // File picker area
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .height(160.dp)
                    .clip(RoundedCornerShape(16.dp))
                    .border(
                        width = 2.dp,
                        color = if (detectedProtocol != null)
                            MaterialTheme.colorScheme.primary.copy(alpha = 0.5f)
                        else MaterialTheme.colorScheme.outline.copy(alpha = 0.5f),
                        shape = RoundedCornerShape(16.dp)
                    )
                    .background(MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.3f))
                    .clickable {
                        filePicker.launch(arrayOf("*/*"))
                    },
                contentAlignment = Alignment.Center
            ) {
                Column(horizontalAlignment = Alignment.CenterHorizontally) {
                    if (detectedProtocol != null) {
                        Icon(
                            Icons.Filled.CheckCircle,
                            contentDescription = null,
                            modifier = Modifier.size(40.dp),
                            tint = MaterialTheme.colorScheme.primary
                        )
                        Spacer(modifier = Modifier.height(8.dp))
                        Text(
                            text = selectedFilename,
                            style = MaterialTheme.typography.bodyMedium,
                            fontWeight = FontWeight.Medium,
                            color = MaterialTheme.colorScheme.onSurface
                        )
                        Text(
                            text = "Detected: ${detectedProtocol!!.label}",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.primary
                        )
                    } else {
                        Icon(
                            Icons.Filled.UploadFile,
                            contentDescription = null,
                            modifier = Modifier.size(40.dp),
                            tint = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                        Spacer(modifier = Modifier.height(8.dp))
                        Text(
                            text = "Tap to select config file",
                            style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                        Text(
                            text = "or drop file here",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.7f)
                        )
                    }
                }
            }

            Spacer(modifier = Modifier.height(20.dp))

            // Connection name input — hidden when adding a protocol to an
            // existing connection (we keep the existing name).
            if (targetConnection == null) {
                OutlinedTextField(
                    value = connectionName,
                    onValueChange = { connectionName = it },
                    label = { Text("Connection Name") },
                    placeholder = { Text("e.g. Office VPN") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                    enabled = detectedProtocol != null
                )

                Spacer(modifier = Modifier.height(16.dp))
            }

            // Import button
            Button(
                onClick = {
                    if (detectedProtocol != null && fileContent.isNotBlank()) {
                        val protocolConfig = ConfigParser.buildProtocolConfig(fileContent, selectedFilename)
                        if (protocolConfig != null) {
                            val name = if (targetConnection != null) {
                                targetConnection.name
                            } else {
                                connectionName.ifBlank {
                                    ConfigParser.deriveConnectionName(selectedFilename)
                                }
                            }
                            connectionRepo.addOrUpdate(targetConnection?.id, name, protocolConfig)
                            importSuccess = true
                            importError = null
                            onConnectionAdded()
                        } else {
                            importError = "Failed to parse config file"
                        }
                    }
                },
                enabled = detectedProtocol != null && fileContent.isNotBlank() && !importSuccess,
                modifier = Modifier.fillMaxWidth(),
                colors = ButtonDefaults.buttonColors(
                    containerColor = MaterialTheme.colorScheme.primary
                )
            ) {
                Icon(
                    Icons.Filled.FileOpen,
                    contentDescription = null,
                    modifier = Modifier.size(18.dp)
                )
                Spacer(modifier = Modifier.size(8.dp))
                Text(
                    text = when {
                        importSuccess -> "Imported"
                        targetConnection != null -> "Add Protocol Config"
                        else -> "Import Config"
                    }
                )
            }

            // Error message
            if (importError != null) {
                Spacer(modifier = Modifier.height(12.dp))
                Text(
                    text = importError!!,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.error,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.fillMaxWidth()
                )
            }

            Spacer(modifier = Modifier.height(24.dp))

            // Supported formats info
            Card(
                modifier = Modifier.fillMaxWidth(),
                colors = CardDefaults.cardColors(
                    containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f)
                ),
                shape = RoundedCornerShape(12.dp)
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text(
                        text = "Supported Formats",
                        style = MaterialTheme.typography.labelMedium,
                        fontWeight = FontWeight.SemiBold,
                        color = MaterialTheme.colorScheme.onSurface
                    )
                    Spacer(modifier = Modifier.height(8.dp))
                    FormatInfo("WireGuard", ".conf", "WireGuard configuration files")
                    Spacer(modifier = Modifier.height(4.dp))
                    FormatInfo("OpenVPN", ".ovpn", "OpenVPN configuration files")
                    Spacer(modifier = Modifier.height(4.dp))
                    FormatInfo("IPSec", ".sswan / .mobileconfig", "strongSwan or Apple profile")
                }
            }
        }
    }
}

/**
 * In-screen gateway panel used from AddConnectionScreen. Lists the
 * authenticated user's remote configs and lets the user import any of them
 * directly — optionally filtered to the protocols missing from a target
 * connection (add-protocol-to-existing mode).
 *
 * The parent owns `downloadingKey`, a "<protocol>-<id>" string, so rows can
 * disable themselves while a download is in flight without the panel needing
 * its own coroutine scope.
 */
@Composable
private fun GatewayPanel(
    configs: List<RemoteConfigEntry>,
    isLoading: Boolean,
    error: String?,
    downloadingKey: String?,
    emptyMessage: String,
    onDownload: (RemoteConfigEntry) -> Unit
) {
    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f)
        ),
        shape = RoundedCornerShape(12.dp)
    ) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(
                    Icons.Filled.Cloud,
                    contentDescription = null,
                    modifier = Modifier.size(16.dp),
                    tint = MaterialTheme.colorScheme.primary
                )
                Spacer(modifier = Modifier.size(6.dp))
                Text(
                    text = "From Gateway",
                    style = MaterialTheme.typography.labelMedium,
                    fontWeight = FontWeight.SemiBold,
                    color = MaterialTheme.colorScheme.onSurface
                )
            }

            Spacer(modifier = Modifier.height(6.dp))

            when {
                isLoading -> {
                    Box(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(16.dp),
                        contentAlignment = Alignment.Center
                    ) {
                        CircularProgressIndicator(
                            modifier = Modifier.size(22.dp),
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
                        text = emptyMessage,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }

                else -> {
                    configs.forEach { entry ->
                        val key = "${entry.protocol}-${entry.id}"
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .padding(vertical = 4.dp),
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            val protocol = VpnProtocol.fromString(entry.protocol)
                            val iconRes = when (protocol) {
                                VpnProtocol.WIREGUARD -> com.privycs.vpn.R.drawable.ic_protocol_wireguard
                                VpnProtocol.OPENVPN   -> com.privycs.vpn.R.drawable.ic_protocol_openvpn
                                VpnProtocol.IPSEC     -> com.privycs.vpn.R.drawable.ic_protocol_strongswan
                                null                  -> null
                            }
                            val iconTint = when (protocol) {
                                VpnProtocol.WIREGUARD -> com.privycs.vpn.ui.theme.WireGuardRed
                                VpnProtocol.OPENVPN   -> com.privycs.vpn.ui.theme.OpenVpnOrange
                                VpnProtocol.IPSEC     -> com.privycs.vpn.ui.theme.IpSecBlue
                                null                  -> MaterialTheme.colorScheme.primary
                            }
                            if (iconRes != null) {
                                androidx.compose.material3.Icon(
                                    painter = androidx.compose.ui.res.painterResource(id = iconRes),
                                    contentDescription = entry.protocol,
                                    tint = iconTint,
                                    modifier = Modifier.size(26.dp).padding(end = 8.dp)
                                )
                            } else {
                                Text(
                                    text = entry.protocol.uppercase(),
                                    style = MaterialTheme.typography.labelSmall,
                                    fontWeight = FontWeight.SemiBold,
                                    color = MaterialTheme.colorScheme.primary,
                                    modifier = Modifier.padding(end = 8.dp)
                                )
                            }
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
                            if (downloadingKey == key) {
                                CircularProgressIndicator(
                                    modifier = Modifier.size(18.dp),
                                    strokeWidth = 2.dp
                                )
                            } else {
                                TextButton(
                                    onClick = { onDownload(entry) },
                                    enabled = downloadingKey == null
                                ) {
                                    Icon(
                                        Icons.Filled.CloudDownload,
                                        contentDescription = null,
                                        modifier = Modifier.size(16.dp)
                                    )
                                    Spacer(modifier = Modifier.size(4.dp))
                                    Text("Import", style = MaterialTheme.typography.labelSmall)
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun FormatInfo(name: String, extension: String, description: String) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        verticalAlignment = Alignment.CenterVertically
    ) {
        Text(
            text = name,
            style = MaterialTheme.typography.bodySmall,
            fontWeight = FontWeight.Medium,
            color = MaterialTheme.colorScheme.onSurface,
            modifier = Modifier.weight(0.3f)
        )
        Text(
            text = extension,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.primary,
            modifier = Modifier.weight(0.3f)
        )
        Text(
            text = description,
            style = MaterialTheme.typography.labelSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.weight(0.4f)
        )
    }
}
