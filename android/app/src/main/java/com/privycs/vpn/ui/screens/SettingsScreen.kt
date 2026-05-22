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
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Error
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material.icons.automirrored.filled.OpenInNew
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.RadioButton
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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
import com.privycs.vpn.api.GatewayApiClient
import com.privycs.vpn.backup.CloudBackupManager
import com.privycs.vpn.data.models.AppTheme
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(
    onNavigateToLogs: () -> Unit,
    onNavigateToPerAppVpn: () -> Unit = {},
    onNavigateToNetworkRules: () -> Unit = {},
    onNavigateToLicenses: () -> Unit = {}
) {
    val context = LocalContext.current
    val settingsRepo = remember { PrivycsApp.instance.settingsRepository }
    val settings by settingsRepo.settingsFlow.collectAsState(initial = settingsRepo.defaultSettings())
    val scope = rememberCoroutineScope()
    // Process-scoped coroutine for persistence writes. Settings writes
    // that happen on a rememberCoroutineScope() get cancelled if the
    // user backs out of the screen before the DataStore edit flushes
    // to disk - observed concretely: toggling Connect-on-Demand off
    // then closing the app left COD re-enabled after reboot because
    // the write coroutine was cancelled mid-flush.
    val persistScope = com.privycs.vpn.PrivycsApp.instance.appScope

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
                    text = if (passwordDialogMode == "export")
                        stringResource(R.string.settings_backup_export_dialog_title)
                    else
                        stringResource(R.string.settings_backup_import_dialog_title),
                    style = MaterialTheme.typography.titleMedium
                )
            },
            text = {
                Column {
                    Text(
                        text = if (passwordDialogMode == "export")
                            stringResource(R.string.settings_backup_export_dialog_text)
                        else
                            stringResource(R.string.settings_backup_import_dialog_text),
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                    Spacer(modifier = Modifier.height(12.dp))
                    OutlinedTextField(
                        value = backupPassword,
                        onValueChange = { backupPassword = it },
                        label = { Text(stringResource(R.string.settings_backup_password_label)) },
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
                                    val msg = if (mode == "export")
                                        context.getString(R.string.settings_backup_export_success)
                                    else
                                        context.getString(R.string.settings_backup_import_success)
                                    Toast.makeText(context, msg, Toast.LENGTH_SHORT).show()
                                } catch (e: Exception) {
                                    val errMsg = e.message
                                        ?: context.getString(R.string.settings_backup_operation_failed)
                                    Toast.makeText(context, errMsg, Toast.LENGTH_LONG).show()
                                }
                            }
                        }
                        pendingImportUri = null
                    },
                    enabled = backupPassword.isNotBlank()
                ) {
                    Text(
                        if (passwordDialogMode == "export")
                            stringResource(R.string.settings_backup_export_button)
                        else
                            stringResource(R.string.settings_backup_import_button)
                    )
                }
            },
            dismissButton = {
                TextButton(onClick = {
                    showPasswordDialog = false
                    backupPassword = ""
                    pendingImportUri = null
                }) {
                    Text(stringResource(R.string.settings_cancel))
                }
            }
        )
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        stringResource(R.string.settings_title),
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
                // v0.9.14.91: asymmetric padding (8dp bottom instead
                // of 16dp) reclaims wasted space between the last
                // card and the bottom-nav bar. Top stays at 12dp so
                // the first card has visible breathing room below
                // the TopAppBar; horizontal stays at 16dp.
                .padding(start = 16.dp, top = 12.dp, end = 16.dp, bottom = 8.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)
        ) {
            // -- Privycs Gateway --
            SettingsSection(title = stringResource(R.string.settings_section_gateway)) {
                OutlinedTextField(
                    value = gatewayUrl,
                    onValueChange = {
                        gatewayUrl = it
                        persistScope.launch {
                            settingsRepo.updateGatewayConfig(it, apiKey)
                        }
                    },
                    label = { Text(stringResource(R.string.settings_gateway_url_label)) },
                    placeholder = { Text("https://app.privycs.com") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth()
                )

                Spacer(modifier = Modifier.height(8.dp))

                OutlinedTextField(
                    value = apiKey,
                    onValueChange = {
                        apiKey = it
                        persistScope.launch {
                            settingsRepo.updateGatewayConfig(gatewayUrl, it)
                        }
                    },
                    label = { Text(stringResource(R.string.settings_gateway_api_key_label)) },
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
                                    verifyResult = Pair(
                                        true,
                                        context.getString(
                                            R.string.settings_gateway_verify_result,
                                            profile.user,
                                            profile.count,
                                        ),
                                    )
                                } catch (e: Exception) {
                                    verifyResult = Pair(
                                        false,
                                        e.message
                                            ?: context.getString(R.string.settings_gateway_connection_failed),
                                    )
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
                            Text(stringResource(R.string.settings_gateway_verify_button))
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
            SettingsSection(title = stringResource(R.string.settings_section_connection)) {
                val alwaysOnActive by com.privycs.vpn.util.AlwaysOnDetector.detected.collectAsState()
                val killSwitchDescription = if (alwaysOnActive) {
                    stringResource(R.string.settings_kill_switch_desc_always_on)
                } else {
                    stringResource(R.string.settings_kill_switch_desc_default)
                }
                SettingsToggle(
                    title = stringResource(R.string.settings_kill_switch_title),
                    description = killSwitchDescription,
                    checked = settings.killSwitchEnabled,
                    onCheckedChange = { persistScope.launch { settingsRepo.updateKillSwitch(it) } }
                )

                // App-level "Auto-connect on boot" toggle removed in
                // v0.9.9.6: it was redundant with Connect-on-Demand.
                // With COD enabled (default trigger: wifi_mobile),
                // BootReceiver already hands off to NetworkMonitor,
                // which connects whenever the rule matches - and the
                // default rule matches right after boot as soon as
                // Android has a non-VPN network. The separate toggle
                // just duplicated that behaviour with coarser logic.
                // The System Always-On VPN row below still links to
                // Android Settings for OS-level enforcement, which is
                // a different feature and stays.

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
                                    context.getString(R.string.settings_vpn_settings_open_failed),
                                    Toast.LENGTH_SHORT
                                ).show()
                            }
                        },
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Column(modifier = Modifier.weight(1f)) {
                        Text(
                            text = stringResource(R.string.settings_system_always_on_title),
                            style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onSurface
                        )
                        Text(
                            text = stringResource(R.string.settings_system_always_on_desc),
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                    Icon(
                        imageVector = Icons.AutoMirrored.Filled.OpenInNew,
                        contentDescription = stringResource(R.string.settings_open_vpn_settings_cd),
                        tint = MaterialTheme.colorScheme.primary,
                        modifier = Modifier.padding(start = 12.dp)
                    )
                }
            }

            // -- Network --
            SettingsSection(title = stringResource(R.string.settings_section_network)) {
                // Live validation: parse the entry, show inline
                // error listing invalid IPs. Provider hint surfaces
                // when the user pasted servers belonging to a known
                // public resolver (Cloudflare, Quad9, etc.) so they
                // can be told to additionally enable Private DNS
                // for DoT encryption.
                val dnsInvalid = remember(dnsOverride) {
                    com.privycs.vpn.util.DnsValidator.invalidEntries(dnsOverride)
                }
                val dnsProvider = remember(dnsOverride) {
                    com.privycs.vpn.util.DnsValidator.detectProvider(dnsOverride)
                }
                OutlinedTextField(
                    value = dnsOverride,
                    onValueChange = {
                        dnsOverride = it
                        persistScope.launch {
                            settingsRepo.updateSettings(settings.copy(dnsOverride = it))
                        }
                    },
                    label = { Text(stringResource(R.string.settings_dns_override_label)) },
                    placeholder = { Text(stringResource(R.string.settings_dns_override_placeholder)) },
                    singleLine = true,
                    isError = dnsInvalid.isNotEmpty(),
                    modifier = Modifier.fillMaxWidth(),
                    supportingText = {
                        when {
                            dnsInvalid.isNotEmpty() -> Text(
                                stringResource(
                                    R.string.settings_dns_invalid,
                                    dnsInvalid.joinToString(", "),
                                ),
                                color = MaterialTheme.colorScheme.error
                            )
                            dnsProvider != null -> Text(
                                stringResource(
                                    R.string.settings_dns_detected,
                                    dnsProvider.label,
                                    dnsProvider.dotHost,
                                ),
                                color = MaterialTheme.colorScheme.primary
                            )
                            else -> Text(
                                stringResource(R.string.settings_dns_override_hint)
                            )
                        }
                    }
                )

                // DNS provider preset dropdown. Picking a preset
                // populates the override field with the dual-stack
                // server list. Same canonical table as desktop
                // GetDnsProviders so the two platforms stay in sync.
                // Refactored in v0.9.14.4 to use the shared
                // DnsPresetPicker composable so the per-connection
                // and per-pool DNS UIs use the same options.
                Spacer(Modifier.height(8.dp))
                com.privycs.vpn.ui.components.DnsPresetPicker(
                    onPick = { joined ->
                        dnsOverride = joined
                        persistScope.launch {
                            settingsRepo.updateSettings(settings.copy(dnsOverride = joined))
                        }
                    },
                )

                // Test-DNS button. Synchronously resolves a known
                // hostname and shows the result so the user gets a
                // visible "DNS works / returned X in Yms" signal
                // without having to wait for an actual VPN connect.
                Spacer(Modifier.height(8.dp))
                var dnsTestResult by remember {
                    mutableStateOf<com.privycs.vpn.util.DnsValidator.TestResult?>(null)
                }
                var dnsTesting by remember { mutableStateOf(false) }
                val testScope = rememberCoroutineScope()
                Row(verticalAlignment = Alignment.CenterVertically) {
                    OutlinedButton(
                        onClick = {
                            dnsTesting = true
                            testScope.launch {
                                val res = withContext(Dispatchers.IO) {
                                    com.privycs.vpn.util.DnsValidator.testResolution()
                                }
                                dnsTestResult = res
                                dnsTesting = false
                            }
                        },
                        enabled = !dnsTesting
                    ) {
                        Text(
                            if (dnsTesting)
                                stringResource(R.string.settings_dns_test_in_progress)
                            else
                                stringResource(R.string.settings_dns_test_button)
                        )
                    }
                    Spacer(Modifier.width(12.dp))
                    val res = dnsTestResult
                    if (res != null) {
                        Text(
                            text = if (res.error != null)
                                stringResource(R.string.settings_dns_test_error, res.error)
                            else
                                stringResource(
                                    R.string.settings_dns_test_success,
                                    res.host,
                                    res.addresses.firstOrNull() ?: "?",
                                    res.durationMs,
                                ),
                            style = MaterialTheme.typography.labelSmall,
                            color = if (res.error != null) MaterialTheme.colorScheme.error
                                    else MaterialTheme.colorScheme.onSurfaceVariant
                        )
                    }
                }

                // Android Private DNS hint. We can't toggle the
                // system setting from here (Network → Private DNS
                // is system-managed), but we can guide the user
                // there when their override IPs match a known DoT
                // provider. Closes the most common DoH/DoT support
                // gap without bundling our own DoH proxy.
                if (dnsProvider != null) {
                    Spacer(Modifier.height(8.dp))
                    Card(
                        modifier = Modifier.fillMaxWidth(),
                        colors = CardDefaults.cardColors(
                            containerColor = MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.4f)
                        )
                    ) {
                        Column(modifier = Modifier.padding(12.dp)) {
                            Text(
                                stringResource(R.string.settings_dns_tip_title),
                                style = MaterialTheme.typography.labelMedium,
                                color = MaterialTheme.colorScheme.primary
                            )
                            Spacer(Modifier.height(4.dp))
                            Text(
                                stringResource(
                                    R.string.settings_dns_tip_body,
                                    dnsProvider.label,
                                    dnsProvider.dotHost,
                                ),
                                style = MaterialTheme.typography.bodySmall
                            )
                        }
                    }
                }
            }

            // -- Tunnel Health (Phase 1 visible UX) --
            SettingsSection(title = stringResource(R.string.settings_section_tunnel_health)) {
                Text(
                    text = stringResource(R.string.settings_tunnel_health_desc),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
                Spacer(modifier = Modifier.height(8.dp))
                val mode = settings.tunnelHealthMode
                Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    listOf(
                        "auto" to stringResource(R.string.settings_tunnel_health_mode_auto),
                        "always" to stringResource(R.string.settings_tunnel_health_mode_always),
                        "off" to stringResource(R.string.settings_tunnel_health_mode_off),
                    ).forEach { (value, label) ->
                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            RadioButton(
                                selected = mode == value,
                                onClick = {
                                    persistScope.launch {
                                        settingsRepo.updateSettings(
                                            settings.copy(tunnelHealthMode = value),
                                        )
                                    }
                                },
                            )
                            Text(label, style = MaterialTheme.typography.bodyMedium)
                        }
                    }
                }
                Spacer(modifier = Modifier.height(8.dp))
                var pingTarget by remember(settings.tunnelHealthTarget) {
                    mutableStateOf(settings.tunnelHealthTarget)
                }
                val pingTargetInvalid = remember(pingTarget) {
                    pingTarget.isNotBlank() &&
                        !com.privycs.vpn.util.DnsValidator.isValidIp(pingTarget)
                }
                OutlinedTextField(
                    value = pingTarget,
                    onValueChange = {
                        pingTarget = it
                        persistScope.launch {
                            settingsRepo.updateSettings(
                                settings.copy(tunnelHealthTarget = it.trim()),
                            )
                        }
                    },
                    label = { Text(stringResource(R.string.settings_ping_target_label)) },
                    placeholder = { Text(stringResource(R.string.settings_ping_target_placeholder)) },
                    singleLine = true,
                    isError = pingTargetInvalid,
                    modifier = Modifier.fillMaxWidth(),
                    supportingText = {
                        when {
                            pingTargetInvalid -> Text(
                                stringResource(R.string.settings_ping_target_invalid),
                                color = MaterialTheme.colorScheme.error,
                            )
                            pingTarget.isBlank() -> Text(
                                stringResource(R.string.settings_ping_target_empty_hint),
                            )
                            else -> Text(stringResource(R.string.settings_ping_target_custom))
                        }
                    },
                )

                // v0.9.15.30: probe cadence overrides. Defaults
                // 5 s × 2 = max 10 s; user can dial up for battery
                // savings or down for tighter failover detection.
                Spacer(modifier = Modifier.height(8.dp))
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    var intervalText by remember(settings.tunnelHealthPingIntervalSec) {
                        mutableStateOf(settings.tunnelHealthPingIntervalSec.toString())
                    }
                    var thresholdText by remember(settings.tunnelHealthDeadThreshold) {
                        mutableStateOf(settings.tunnelHealthDeadThreshold.toString())
                    }
                    OutlinedTextField(
                        value = intervalText,
                        onValueChange = { newVal ->
                            intervalText = newVal.filter { it.isDigit() }.take(3)
                            intervalText.toIntOrNull()
                                ?.coerceIn(1, 120)
                                ?.let { v ->
                                    persistScope.launch {
                                        settingsRepo.updateSettings(
                                            settings.copy(tunnelHealthPingIntervalSec = v),
                                        )
                                    }
                                }
                        },
                        label = { Text(stringResource(R.string.settings_tunnel_health_interval_label)) },
                        singleLine = true,
                        modifier = Modifier.weight(1f),
                    )
                    OutlinedTextField(
                        value = thresholdText,
                        onValueChange = { newVal ->
                            thresholdText = newVal.filter { it.isDigit() }.take(2)
                            thresholdText.toIntOrNull()
                                ?.coerceIn(1, 10)
                                ?.let { v ->
                                    persistScope.launch {
                                        settingsRepo.updateSettings(
                                            settings.copy(tunnelHealthDeadThreshold = v),
                                        )
                                    }
                                }
                        },
                        label = { Text(stringResource(R.string.settings_tunnel_health_dead_fails_label)) },
                        singleLine = true,
                        modifier = Modifier.weight(1f),
                    )
                }
                Text(
                    stringResource(R.string.settings_tunnel_health_cadence_hint),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(top = 4.dp),
                )
            }

            // -- On-Demand & Network Rules (unified — v0.9.15.73) --
            SettingsSection(title = stringResource(R.string.settings_section_on_demand)) {
                Text(
                    text = stringResource(R.string.settings_on_demand_desc),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
                Spacer(modifier = Modifier.height(8.dp))
                OutlinedButton(
                    onClick = onNavigateToNetworkRules,
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text(stringResource(R.string.settings_on_demand_open_button))
                }
            }

            // -- Protocol Failover Order (v0.9.15.70) --
            SettingsSection(title = stringResource(R.string.settings_section_protocol_failover)) {
                Text(
                    text = stringResource(R.string.settings_protocol_failover_desc),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
                Spacer(modifier = Modifier.height(12.dp))
                val order = settings.protocolFailoverOrder.ifEmpty {
                    listOf(
                        com.privycs.vpn.data.models.VpnProtocol.AMNEZIAWG,
                        com.privycs.vpn.data.models.VpnProtocol.WIREGUARD,
                        com.privycs.vpn.data.models.VpnProtocol.OPENVPN,
                        com.privycs.vpn.data.models.VpnProtocol.IPSEC,
                    )
                }
                fun moveProtocol(from: Int, to: Int) {
                    if (to < 0 || to >= order.size || from == to) return
                    val mut = order.toMutableList()
                    val item = mut.removeAt(from)
                    mut.add(to, item)
                    scope.launch {
                        settingsRepo.updateSettings(
                            settings.copy(protocolFailoverOrder = mut),
                        )
                    }
                }
                order.forEachIndexed { idx, proto ->
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(vertical = 4.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Text(
                            text = "${idx + 1}.",
                            style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.padding(end = 8.dp),
                        )
                        // Brand-coloured icon — mirrors the ConnectScreen
                        // pill row: single mono-path drawable + brand-
                        // colour tint via Icon's tint param so AmneziaWG
                        // renders in its purple/indigo (#6366F1), WG in
                        // its dark red (#88171A), OpenVPN in orange
                        // (#EA7E20), IPSec in blue (#2563EB).
                        Icon(
                            painter = androidx.compose.ui.res.painterResource(
                                id = when (proto) {
                                    com.privycs.vpn.data.models.VpnProtocol.AMNEZIAWG ->
                                        com.privycs.vpn.R.drawable.ic_protocol_amneziawg
                                    com.privycs.vpn.data.models.VpnProtocol.WIREGUARD ->
                                        com.privycs.vpn.R.drawable.ic_protocol_wireguard
                                    com.privycs.vpn.data.models.VpnProtocol.OPENVPN ->
                                        com.privycs.vpn.R.drawable.ic_protocol_openvpn
                                    com.privycs.vpn.data.models.VpnProtocol.IPSEC ->
                                        com.privycs.vpn.R.drawable.ic_protocol_strongswan
                                },
                            ),
                            contentDescription = proto.label,
                            tint = when (proto) {
                                com.privycs.vpn.data.models.VpnProtocol.AMNEZIAWG ->
                                    com.privycs.vpn.ui.theme.AmneziaWgIndigo
                                com.privycs.vpn.data.models.VpnProtocol.WIREGUARD ->
                                    com.privycs.vpn.ui.theme.WireGuardRed
                                com.privycs.vpn.data.models.VpnProtocol.OPENVPN ->
                                    com.privycs.vpn.ui.theme.OpenVpnOrange
                                com.privycs.vpn.data.models.VpnProtocol.IPSEC ->
                                    com.privycs.vpn.ui.theme.IpSecBlue
                            },
                            modifier = Modifier.size(28.dp),
                        )
                        Spacer(modifier = Modifier.width(12.dp))
                        Text(
                            text = proto.label,
                            style = MaterialTheme.typography.bodyLarge,
                            modifier = Modifier.weight(1f),
                        )
                        IconButton(
                            onClick = { moveProtocol(idx, idx - 1) },
                            enabled = idx > 0,
                        ) {
                            Icon(
                                Icons.Filled.KeyboardArrowUp,
                                contentDescription = stringResource(R.string.settings_protocol_move_up),
                            )
                        }
                        IconButton(
                            onClick = { moveProtocol(idx, idx + 1) },
                            enabled = idx < order.size - 1,
                        ) {
                            Icon(
                                Icons.Filled.KeyboardArrowDown,
                                contentDescription = stringResource(R.string.settings_protocol_move_down),
                            )
                        }
                    }
                }
            }

            // -- Per-App VPN --
            SettingsSection(title = stringResource(R.string.settings_section_per_app)) {
                Text(
                    text = stringResource(R.string.settings_per_app_desc),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
                Spacer(modifier = Modifier.height(8.dp))
                OutlinedButton(
                    onClick = onNavigateToPerAppVpn,
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text(stringResource(R.string.settings_per_app_configure_button))
                }
            }

            // -- Cloud Backup --
            SettingsSection(title = stringResource(R.string.settings_section_cloud_backup)) {
                Text(
                    text = stringResource(R.string.settings_cloud_backup_desc),
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
                        Text(stringResource(R.string.settings_backup_export_button))
                    }
                    OutlinedButton(
                        onClick = { importLauncher.launch(arrayOf("application/json", "*/*")) },
                        modifier = Modifier.weight(1f)
                    ) {
                        Text(stringResource(R.string.settings_backup_import_button))
                    }
                }
            }

            // -- Diagnostics --
            SettingsSection(title = stringResource(R.string.settings_section_diagnostics)) {
                OutlinedButton(
                    onClick = onNavigateToLogs,
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text(stringResource(R.string.settings_view_logs_button))
                }
                Spacer(modifier = Modifier.height(8.dp))
                OutlinedButton(
                    onClick = {
                        // Event notifications (security / status /
                        // diagnostics) are configured in Android's
                        // per-app notification settings — open them
                        // directly; fall back to the app detail page.
                        try {
                            context.startActivity(
                                Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).apply {
                                    putExtra(
                                        Settings.EXTRA_APP_PACKAGE,
                                        context.packageName,
                                    )
                                    addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                                },
                            )
                        } catch (_: Exception) {
                            try {
                                context.startActivity(
                                    Intent(
                                        Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                                        android.net.Uri.parse(
                                            "package:${context.packageName}",
                                        ),
                                    ).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                                )
                            } catch (_: Exception) {
                            }
                        }
                    },
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text(stringResource(R.string.settings_notification_settings_button))
                }
            }

            // -- Appearance --
            SettingsSection(title = stringResource(R.string.settings_section_appearance)) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = stringResource(R.string.settings_theme_label),
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurface
                    )

                    SingleChoiceSegmentedButtonRow {
                        val themes = listOf(
                            AppTheme.SYSTEM to stringResource(R.string.settings_theme_system),
                            AppTheme.DARK to stringResource(R.string.settings_theme_dark),
                            AppTheme.LIGHT to stringResource(R.string.settings_theme_light)
                        )
                        themes.forEachIndexed { index, (theme, label) ->
                            SegmentedButton(
                                selected = settings.theme == theme,
                                onClick = {
                                    persistScope.launch { settingsRepo.updateTheme(theme) }
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
            SettingsSection(title = stringResource(R.string.settings_section_about)) {
                SettingsInfoRow(stringResource(R.string.settings_about_app_label), "Privycs VPN")
                SettingsInfoRow(
                    stringResource(R.string.settings_about_version_label),
                    "${com.privycs.vpn.BuildConfig.VERSION_NAME} (${com.privycs.vpn.BuildConfig.VERSION_CODE})"
                )
                // The app supports all three protocols; the per-user
                // default "Protocol" line was misleading (suggested the
                // app only spoke one). Show the full supported set.
                SettingsInfoRow(
                    stringResource(R.string.settings_about_protocols_label),
                    "WireGuard, OpenVPN, IPSec"
                )
                SettingsInfoRow(
                    stringResource(R.string.settings_about_license_label),
                    stringResource(R.string.settings_about_license_value)
                )
                Spacer(modifier = Modifier.height(8.dp))
                OutlinedButton(
                    onClick = onNavigateToLicenses,
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text(stringResource(R.string.settings_open_source_licenses_button))
                }
                Spacer(modifier = Modifier.height(8.dp))
                OutlinedButton(
                    onClick = {
                        runCatching {
                            context.startActivity(
                                Intent(
                                    Intent.ACTION_VIEW,
                                    android.net.Uri.parse(
                                        "https://www.privycs.com/docs/android-client-privacy",
                                    ),
                                ).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                            )
                        }
                    },
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Text(stringResource(R.string.settings_privacy_policy_button))
                }
            }

            // v0.9.14.91: dropped the 32dp trailing spacer that
            // was creating ~48dp of black wasted area between the
            // last card and the bottom-nav bar (32dp Spacer +
            // 16dp Column padding-bottom). The Column's own
            // bottom padding is enough breathing room.
        }
    }
}

@Composable
internal fun SettingsSection(
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
internal fun SettingsToggle(
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
