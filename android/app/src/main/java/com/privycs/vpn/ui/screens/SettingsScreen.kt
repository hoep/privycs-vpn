package com.privycs.vpn.ui.screens

import android.app.Activity
import android.content.Intent
import android.provider.Settings
import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Error
import androidx.compose.material.icons.automirrored.filled.OpenInNew
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.InputChip
import androidx.compose.material3.InputChipDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.api.GatewayApiClient
import com.privycs.vpn.backup.CloudBackupManager
import com.privycs.vpn.data.models.AppTheme
import com.privycs.vpn.data.models.ConnectOnDemandSettings
import com.privycs.vpn.data.models.RoutingMode
import com.privycs.vpn.service.NetworkMonitor
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
fun SettingsScreen(
    onNavigateToLogs: () -> Unit,
    onNavigateToSplitTunnel: () -> Unit = {}
) {
    val context = LocalContext.current
    val settingsRepo = remember { PrivycsApp.instance.settingsRepository }
    val settings by settingsRepo.settingsFlow.collectAsState(initial = settingsRepo.defaultSettings())
    val scope = rememberCoroutineScope()

    // Gateway verification state
    var verifying by remember { mutableStateOf(false) }
    var verifyResult by remember { mutableStateOf<Pair<Boolean, String>?>(null) }

    // Local mutable copies for text fields
    var gatewayUrl by remember(settings.gatewayUrl) { mutableStateOf(settings.gatewayUrl) }
    var apiKey by remember(settings.apiKey) { mutableStateOf(settings.apiKey) }
    var dnsOverride by remember(settings.dnsOverride) { mutableStateOf(settings.dnsOverride) }

    // Backup state
    val backupManager = remember { CloudBackupManager(context) }
    var showPasswordDialog by remember { mutableStateOf(false) }
    var passwordDialogMode by remember { mutableStateOf("export") } // "export" or "import"
    var backupPassword by remember { mutableStateOf("") }
    var pendingImportUri by remember { mutableStateOf<android.net.Uri?>(null) }

    // SAF launchers for backup
    val exportLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.CreateDocument("application/json")
    ) { uri ->
        if (uri != null) {
            passwordDialogMode = "export"
            showPasswordDialog = true
            pendingImportUri = uri
        }
    }

    val importLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.OpenDocument()
    ) { uri ->
        if (uri != null) {
            passwordDialogMode = "import"
            pendingImportUri = uri
            showPasswordDialog = true
        }
    }

    // Location permission launcher for WiFi SSID detection. On Android 8+,
    // reading the currently-connected SSID via WifiManager.connectionInfo
    // returns "<unknown ssid>" without ACCESS_FINE_LOCATION granted AT
    // RUNTIME (manifest declaration alone is not enough since API 23). When
    // SSID comes back empty, the "only these" rule falls into the can't-match
    // branch (verdict: do not connect) and "except these" falls into the
    // can't-check-exceptions branch (verdict: connect) -- which to the user
    // looks like the modes are swapped. Requesting the permission here fixes
    // the root cause; the binary verdict then reflects the actual SSID.
    val locationPermissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { _ ->
        scope.launch {
            com.privycs.vpn.service.NetworkMonitor.getInstance(context).reevaluate()
        }
    }

    fun ensureLocationPermissionIfNeeded(mode: String) {
        if (mode != "only" && mode != "except") return
        val granted = androidx.core.content.ContextCompat.checkSelfPermission(
            context, android.Manifest.permission.ACCESS_FINE_LOCATION
        ) == android.content.pm.PackageManager.PERMISSION_GRANTED
        if (!granted) {
            locationPermissionLauncher.launch(android.Manifest.permission.ACCESS_FINE_LOCATION)
        }
    }

    // One-shot prompt when the screen opens if the user already has SSID-based
    // connect-on-demand enabled from a prior install that predated this fix.
    androidx.compose.runtime.LaunchedEffect(settings.connectOnDemand.ssidMode) {
        ensureLocationPermissionIfNeeded(settings.connectOnDemand.ssidMode)
    }

    // Password dialog
    if (showPasswordDialog) {
        AlertDialog(
            onDismissRequest = {
                showPasswordDialog = false
                backupPassword = ""
                pendingImportUri = null
            },
            title = {
                Text(
                    text = if (passwordDialogMode == "export") "Export Backup" else "Import Backup",
                    style = MaterialTheme.typography.titleMedium
                )
            },
            text = {
                Column {
                    Text(
                        text = if (passwordDialogMode == "export")
                            "Enter a password to encrypt the backup."
                        else
                            "Enter the password used when this backup was created.",
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                    Spacer(modifier = Modifier.height(12.dp))
                    OutlinedTextField(
                        value = backupPassword,
                        onValueChange = { backupPassword = it },
                        label = { Text("Password") },
                        singleLine = true,
                        visualTransformation = PasswordVisualTransformation(),
                        modifier = Modifier.fillMaxWidth()
                    )
                }
            },
            confirmButton = {
                TextButton(
                    onClick = {
                        val password = backupPassword
                        val uri = pendingImportUri
                        val mode = passwordDialogMode
                        showPasswordDialog = false
                        backupPassword = ""

                        if (password.isNotBlank() && uri != null) {
                            scope.launch {
                                try {
                                    withContext(Dispatchers.IO) {
                                        if (mode == "export") {
                                            backupManager.exportBackup(password, uri)
                                        } else {
                                            backupManager.importAndMerge(password, uri)
                                        }
                                    }
                                    val msg = if (mode == "export") "Backup exported" else "Backup imported"
                                    Toast.makeText(context, msg, Toast.LENGTH_SHORT).show()
                                } catch (e: Exception) {
                                    val errMsg = e.message ?: "Operation failed"
                                    Toast.makeText(context, errMsg, Toast.LENGTH_LONG).show()
                                }
                            }
                        }
                        pendingImportUri = null
                    },
                    enabled = backupPassword.isNotBlank()
                ) {
                    Text(if (passwordDialogMode == "export") "Export" else "Import")
                }
            },
            dismissButton = {
                TextButton(onClick = {
                    showPasswordDialog = false
                    backupPassword = ""
                    pendingImportUri = null
                }) {
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
                        "Settings",
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.SemiBold
                    )
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
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            // -- Privycs Gateway --
            SettingsSection(title = "PRIVYCS GATEWAY") {
                OutlinedTextField(
                    value = gatewayUrl,
                    onValueChange = {
                        gatewayUrl = it
                        scope.launch {
                            settingsRepo.updateGatewayConfig(it, apiKey)
                        }
                    },
                    label = { Text("Gateway URL") },
                    placeholder = { Text("https://app.privycs.com") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth()
                )

                Spacer(modifier = Modifier.height(8.dp))

                OutlinedTextField(
                    value = apiKey,
                    onValueChange = {
                        apiKey = it
                        scope.launch {
                            settingsRepo.updateGatewayConfig(gatewayUrl, it)
                        }
                    },
                    label = { Text("API Key") },
                    placeholder = { Text("pvcs_...") },
                    singleLine = true,
                    visualTransformation = PasswordVisualTransformation(),
                    modifier = Modifier.fillMaxWidth()
                )

                Spacer(modifier = Modifier.height(8.dp))

                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    Button(
                        onClick = {
                            scope.launch {
                                verifying = true
                                verifyResult = null
                                try {
                                    settingsRepo.updateGatewayConfig(gatewayUrl, apiKey)
                                    val client = GatewayApiClient(gatewayUrl, apiKey)
                                    val profile = client.fetchProfile()
                                    client.close()
                                    verifyResult = Pair(true, "${profile.user} (${profile.count} configs)")
                                } catch (e: Exception) {
                                    verifyResult = Pair(false, e.message ?: "Connection failed")
                                } finally {
                                    verifying = false
                                }
                            }
                        },
                        enabled = !verifying && gatewayUrl.isNotBlank() && apiKey.isNotBlank(),
                        colors = ButtonDefaults.buttonColors(
                            containerColor = MaterialTheme.colorScheme.primary
                        )
                    ) {
                        if (verifying) {
                            CircularProgressIndicator(
                                modifier = Modifier.size(16.dp),
                                strokeWidth = 2.dp,
                                color = MaterialTheme.colorScheme.onPrimary
                            )
                        } else {
                            Text("Verify & Sync")
                        }
                    }

                    verifyResult?.let { (ok, msg) ->
                        Icon(
                            imageVector = if (ok) Icons.Filled.CheckCircle else Icons.Filled.Error,
                            contentDescription = null,
                            modifier = Modifier.size(16.dp),
                            tint = if (ok) MaterialTheme.colorScheme.primary
                            else MaterialTheme.colorScheme.error
                        )
                        Text(
                            text = msg,
                            style = MaterialTheme.typography.labelSmall,
                            color = if (ok) MaterialTheme.colorScheme.primary
                            else MaterialTheme.colorScheme.error
                        )
                    }
                }
            }

            // -- Connection --
            SettingsSection(title = "CONNECTION") {
                SettingsToggle(
                    title = "Kill Switch",
                    description = "Block traffic if VPN disconnects",
                    checked = settings.killSwitchEnabled,
                    onCheckedChange = { scope.launch { settingsRepo.updateKillSwitch(it) } }
                )

                SettingsToggle(
                    title = "Always-On VPN",
                    description = "Reconnect automatically after disconnection",
                    checked = settings.alwaysOn,
                    onCheckedChange = { scope.launch { settingsRepo.updateAlwaysOn(it) } }
                )

                // Always-on VPN auto-start after boot is governed by an
                // Android system-level setting, not by this app. Google's
                // security model explicitly prevents any app from enabling
                // "Always-on VPN" in code — the user must flip it in
                // Settings → Network & Internet → VPN → <our app> → gear.
                // We can deep-link them straight to the VPN settings list
                // via ACTION_VPN_SETTINGS so they only need two taps
                // (pick us, open gear). The per-app sub-page itself is not
                // exposed as a public intent, so two taps is the minimum.
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 16.dp, vertical = 8.dp)
                        .clickable {
                            runCatching {
                                val intent = Intent(Settings.ACTION_VPN_SETTINGS)
                                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                                context.startActivity(intent)
                            }.onFailure {
                                Toast.makeText(
                                    context,
                                    "Could not open Android VPN settings",
                                    Toast.LENGTH_SHORT
                                ).show()
                            }
                        },
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Column(modifier = Modifier.weight(1f)) {
                        Text(
                            text = "Auto-start after reboot",
                            style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onSurface
                        )
                        Text(
                            text = "Open Android system settings to enable Always-on VPN (required — Android does not allow apps to enable this programmatically).",
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                    Icon(
                        imageVector = Icons.AutoMirrored.Filled.OpenInNew,
                        contentDescription = "Open Android VPN settings",
                        tint = MaterialTheme.colorScheme.primary,
                        modifier = Modifier.padding(start = 12.dp)
                    )
                }
            }

            // -- Connect on Demand --
            SettingsSection(title = "CONNECT ON DEMAND") {
                val cod = settings.connectOnDemand
                val networkMonitor = remember { NetworkMonitor.getInstance(context) }
                val networkState by networkMonitor.networkState.collectAsState()

                // Local state for SSID text input
                var ssidInput by remember { mutableStateOf("") }

                SettingsToggle(
                    title = "Connect on Demand",
                    description = "Automatically connect VPN based on network rules",
                    checked = cod.enabled,
                    onCheckedChange = { enabled ->
                        scope.launch {
                            val updated = cod.copy(enabled = enabled)
                            settingsRepo.updateConnectOnDemand(updated)
                            if (enabled) {
                                networkMonitor.start()
                                // Re-evaluate immediately so the live status
                                // chip reflects the current decision without
                                // waiting for the next network event.
                                networkMonitor.reevaluate()
                            } else {
                                networkMonitor.stop()
                            }
                        }
                    }
                )

                if (cod.enabled) {
                    Spacer(modifier = Modifier.height(8.dp))

                    // Network trigger selection
                    Text(
                        text = "When connected to:",
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                    Spacer(modifier = Modifier.height(4.dp))

                    SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                        val triggers = listOf(
                            "wifi" to "WiFi",
                            "mobile" to "Mobile",
                            "wifi_mobile" to "WiFi & Mobile"
                        )
                        triggers.forEachIndexed { index, (value, label) ->
                            SegmentedButton(
                                selected = cod.trigger == value,
                                onClick = {
                                    scope.launch {
                                        settingsRepo.updateConnectOnDemand(cod.copy(trigger = value))
                                        networkMonitor.reevaluate()
                                    }
                                },
                                shape = SegmentedButtonDefaults.itemShape(
                                    index = index,
                                    count = triggers.size
                                )
                            ) {
                                Text(label, style = MaterialTheme.typography.labelSmall)
                            }
                        }
                    }

                    // SSID filter (only relevant when WiFi is part of trigger)
                    if (cod.trigger == "wifi" || cod.trigger == "wifi_mobile") {
                        Spacer(modifier = Modifier.height(12.dp))

                        Text(
                            text = "WiFi Networks:",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                        Spacer(modifier = Modifier.height(4.dp))

                        SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                            val modes = listOf(
                                "all" to "All SSIDs",
                                "only" to "Only these",
                                "except" to "Except these"
                            )
                            modes.forEachIndexed { index, (value, label) ->
                                SegmentedButton(
                                    selected = cod.ssidMode == value,
                                    onClick = {
                                        ensureLocationPermissionIfNeeded(value)
                                        scope.launch {
                                            settingsRepo.updateConnectOnDemand(
                                                cod.copy(ssidMode = value)
                                            )
                                            networkMonitor.reevaluate()
                                        }
                                    },
                                    shape = SegmentedButtonDefaults.itemShape(
                                        index = index,
                                        count = modes.size
                                    )
                                ) {
                                    Text(label, style = MaterialTheme.typography.labelSmall)
                                }
                            }
                        }

                        // SSID list input (visible for "only" and "except" modes)
                        if (cod.ssidMode == "only" || cod.ssidMode == "except") {
                            Spacer(modifier = Modifier.height(8.dp))

                            Row(
                                modifier = Modifier.fillMaxWidth(),
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(8.dp)
                            ) {
                                OutlinedTextField(
                                    value = ssidInput,
                                    onValueChange = { ssidInput = it },
                                    label = { Text("Add SSID") },
                                    placeholder = { Text("Network name") },
                                    singleLine = true,
                                    modifier = Modifier.weight(1f)
                                )
                                Button(
                                    onClick = {
                                        val trimmed = ssidInput.trim()
                                        if (trimmed.isNotEmpty() && trimmed !in cod.ssidList) {
                                            scope.launch {
                                                settingsRepo.updateConnectOnDemand(
                                                    cod.copy(
                                                        ssidList = cod.ssidList + trimmed
                                                    )
                                                )
                                            }
                                            ssidInput = ""
                                        }
                                    },
                                    enabled = ssidInput.trim().isNotEmpty()
                                ) {
                                    Text("Add")
                                }
                            }

                            if (cod.ssidList.isNotEmpty()) {
                                Spacer(modifier = Modifier.height(4.dp))
                                FlowRow(
                                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                                    verticalArrangement = Arrangement.spacedBy(4.dp)
                                ) {
                                    cod.ssidList.forEach { ssid ->
                                        InputChip(
                                            selected = false,
                                            onClick = { },
                                            label = { Text(ssid) },
                                            trailingIcon = {
                                                IconButton(
                                                    onClick = {
                                                        scope.launch {
                                                            settingsRepo.updateConnectOnDemand(
                                                                cod.copy(
                                                                    ssidList = cod.ssidList - ssid
                                                                )
                                                            )
                                                        }
                                                    },
                                                    modifier = Modifier.size(18.dp)
                                                ) {
                                                    Icon(
                                                        Icons.Filled.Close,
                                                        contentDescription = "Remove $ssid",
                                                        modifier = Modifier.size(14.dp)
                                                    )
                                                }
                                            }
                                        )
                                    }
                                }
                            }
                        }
                    }

                    // Status display — verdict on first line, actual rule reason on
                    // second line so the user sees WHY (e.g. "Cannot determine SSID —
                    // grant Location permission...") instead of only the binary
                    // outcome which reads as inverted when SSID detection fails.
                    Spacer(modifier = Modifier.height(12.dp))
                    val statusText = buildString {
                        append("Status: ")
                        when (networkState.networkType) {
                            "wifi" -> {
                                append("Connected to")
                                if (networkState.ssid.isNotEmpty()) {
                                    append(" \"${networkState.ssid}\"")
                                }
                                append(" (WiFi)")
                            }
                            "mobile" -> append("Connected via Mobile")
                            "ethernet" -> append("Connected via Ethernet")
                            else -> append("No network")
                        }
                        if (networkState.ruleMatch.isNotEmpty()) {
                            append(" -- ")
                            if (networkState.shouldConnect) {
                                append("VPN will connect")
                            } else {
                                append("VPN will not connect")
                            }
                        }
                    }
                    Text(
                        text = statusText,
                        style = MaterialTheme.typography.bodySmall,
                        color = if (networkState.shouldConnect)
                            MaterialTheme.colorScheme.primary
                        else
                            MaterialTheme.colorScheme.onSurfaceVariant
                    )
                    if (networkState.ruleMatch.isNotEmpty()) {
                        Spacer(modifier = Modifier.height(2.dp))
                        Text(
                            text = networkState.ruleMatch,
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                }
            }

            // -- Network --
            SettingsSection(title = "NETWORK") {
                OutlinedTextField(
                    value = dnsOverride,
                    onValueChange = {
                        dnsOverride = it
                        scope.launch {
                            settingsRepo.updateSettings(settings.copy(dnsOverride = it))
                        }
                    },
                    label = { Text("DNS Override") },
                    placeholder = { Text("e.g. 1.1.1.1 or leave empty") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth()
                )

                Spacer(modifier = Modifier.height(8.dp))

                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = "Routing Mode",
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurface
                    )

                    SingleChoiceSegmentedButtonRow {
                        SegmentedButton(
                            selected = settings.routingMode == RoutingMode.FULL,
                            onClick = {
                                scope.launch {
                                    settingsRepo.updateSettings(settings.copy(routingMode = RoutingMode.FULL))
                                }
                            },
                            shape = SegmentedButtonDefaults.itemShape(index = 0, count = 2)
                        ) {
                            Text("Full", style = MaterialTheme.typography.labelSmall)
                        }
                        SegmentedButton(
                            selected = settings.routingMode == RoutingMode.SPLIT,
                            onClick = {
                                scope.launch {
                                    settingsRepo.updateSettings(settings.copy(routingMode = RoutingMode.SPLIT))
                                }
                            },
                            shape = SegmentedButtonDefaults.itemShape(index = 1, count = 2)
                        ) {
                            Text("Split", style = MaterialTheme.typography.labelSmall)
                        }
                    }
                }
            }

            // -- Split Tunnel --
            SettingsSection(title = "SPLIT TUNNEL") {
                Text(
                    text = "Choose which apps use the VPN connection.",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
                Spacer(modifier = Modifier.height(8.dp))
                OutlinedButton(
                    onClick = onNavigateToSplitTunnel,
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text("Configure Split Tunnel")
                }
            }

            // -- Cloud Backup --
            SettingsSection(title = "CLOUD BACKUP") {
                Text(
                    text = "Export or import VPN connections with AES-256 encryption.",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
                Spacer(modifier = Modifier.height(8.dp))
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(8.dp)
                ) {
                    OutlinedButton(
                        onClick = { exportLauncher.launch("privycs-backup.json") },
                        modifier = Modifier.weight(1f)
                    ) {
                        Text("Export")
                    }
                    OutlinedButton(
                        onClick = { importLauncher.launch(arrayOf("application/json", "*/*")) },
                        modifier = Modifier.weight(1f)
                    ) {
                        Text("Import")
                    }
                }
            }

            // -- Diagnostics --
            SettingsSection(title = "DIAGNOSTICS") {
                OutlinedButton(
                    onClick = onNavigateToLogs,
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text("View Logs")
                }
            }

            // -- Appearance --
            SettingsSection(title = "APPEARANCE") {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = "Theme",
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurface
                    )

                    SingleChoiceSegmentedButtonRow {
                        val themes = listOf(
                            AppTheme.SYSTEM to "System",
                            AppTheme.DARK to "Dark",
                            AppTheme.LIGHT to "Light"
                        )
                        themes.forEachIndexed { index, (theme, label) ->
                            SegmentedButton(
                                selected = settings.theme == theme,
                                onClick = {
                                    scope.launch { settingsRepo.updateTheme(theme) }
                                },
                                shape = SegmentedButtonDefaults.itemShape(
                                    index = index,
                                    count = themes.size
                                )
                            ) {
                                Text(label, style = MaterialTheme.typography.labelSmall)
                            }
                        }
                    }
                }
            }

            // -- About --
            SettingsSection(title = "ABOUT") {
                SettingsInfoRow("App", "Privycs VPN")
                SettingsInfoRow(
                    "Version",
                    "${com.privycs.vpn.BuildConfig.VERSION_NAME} (${com.privycs.vpn.BuildConfig.VERSION_CODE})"
                )
                // The app supports all three protocols; the per-user
                // default "Protocol" line was misleading (suggested the
                // app only spoke one). Show the full supported set.
                SettingsInfoRow("Protocols", "WireGuard, OpenVPN, IPSec")
            }

            Spacer(modifier = Modifier.height(32.dp))
        }
    }
}

@Composable
private fun SettingsSection(
    title: String,
    content: @Composable () -> Unit
) {
    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surface
        ),
        shape = RoundedCornerShape(12.dp)
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            Text(
                text = title,
                style = MaterialTheme.typography.labelSmall,
                fontWeight = FontWeight.SemiBold,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                letterSpacing = MaterialTheme.typography.labelSmall.letterSpacing
            )
            Spacer(modifier = Modifier.height(12.dp))
            content()
        }
    }
}

@Composable
private fun SettingsToggle(
    title: String,
    description: String,
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 4.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = title,
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurface
            )
            Text(
                text = description,
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant
            )
        }
        Switch(
            checked = checked,
            onCheckedChange = onCheckedChange,
            colors = SwitchDefaults.colors(
                checkedTrackColor = MaterialTheme.colorScheme.primary,
                checkedThumbColor = MaterialTheme.colorScheme.onPrimary
            )
        )
    }
}

@Composable
private fun SettingsInfoRow(label: String, value: String) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 2.dp),
        horizontalArrangement = Arrangement.SpaceBetween
    ) {
        Text(
            text = label,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant
        )
        Text(
            text = value,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurface
        )
    }
}
