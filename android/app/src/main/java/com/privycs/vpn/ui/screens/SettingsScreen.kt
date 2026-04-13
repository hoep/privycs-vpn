package com.privycs.vpn.ui.screens

import android.app.Activity
import android.content.Intent
import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Arrangement
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
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Error
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
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
import com.privycs.vpn.data.models.RoutingMode
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

@OptIn(ExperimentalMaterial3Api::class)
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
                    title = "Auto-Connect on Start",
                    description = "Connect VPN when app starts",
                    checked = settings.autoConnectOnStart,
                    onCheckedChange = { scope.launch { settingsRepo.updateAutoConnect(it) } }
                )

                SettingsToggle(
                    title = "Always-On VPN",
                    description = "Reconnect automatically after disconnection",
                    checked = settings.alwaysOn,
                    onCheckedChange = { scope.launch { settingsRepo.updateAlwaysOn(it) } }
                )
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
                SettingsInfoRow("Version", "0.1.0")
                SettingsInfoRow("Protocol", settings.activeProtocol.label)
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
