package com.privycs.vpn.ui.screens

import android.content.Context
import android.content.Intent
import android.provider.Settings
import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
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
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Close
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.InputChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
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
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.service.NetworkMonitor
import kotlinx.coroutines.launch

/**
 * Standalone Connect-on-Demand editor. Extracted verbatim from the
 * old Settings -> "CONNECT ON DEMAND" section in v0.9.15.73 as part
 * of the COD + Network-Rules unification (Option A1): the simple
 * trigger / SSID-list config is now the "Default behaviour" reached
 * from the unified Network Rules screen, which makes the
 * rules-first -> default-fallback precedence visible. Engine
 * behaviour is unchanged - this is a UI/IA move only.
 */
@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
fun ConnectOnDemandScreen(onBack: () -> Unit) {
    val context = LocalContext.current
    val settingsRepo = remember { PrivycsApp.instance.settingsRepository }
    val settings by settingsRepo.settingsFlow.collectAsState(
        initial = settingsRepo.defaultSettings(),
    )
    val scope = rememberCoroutineScope()
    // Process-scoped coroutine for persistence writes - same
    // rationale as SettingsScreen: a DataStore edit must survive the
    // user backing out of the screen before the write flushes.
    val persistScope = com.privycs.vpn.PrivycsApp.instance.appScope

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

    // v0.9.15.31: separate launcher for Android-13+'s NEARBY_WIFI_DEVICES
    // permission. Required alongside ACCESS_FINE_LOCATION to read
    // SSID without the OS-wide Location toggle on some Android-13+
    // ROMs. Pre-Android-13 the permission request is a silent no-op.
    val nearbyWifiPermissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { _ ->
        scope.launch {
            com.privycs.vpn.service.NetworkMonitor.getInstance(context).reevaluate()
        }
    }
    val ssidPermLauncher = SsidPermLaunchers(
        location = locationPermissionLauncher,
        nearbyWifi = nearbyWifiPermissionLauncher,
    )

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

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        "Connect on Demand",
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.SemiBold,
                    )
                },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(
                            Icons.AutoMirrored.Filled.ArrowBack,
                            contentDescription = "Back",
                        )
                    }
                },
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .padding(start = 16.dp, top = 12.dp, end = 16.dp, bottom = 16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
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
                        // Persistence on persistScope, NOT rememberCoroutineScope:
                        // the DataStore write + any follow-up disconnect must
                        // survive the user backing out of Settings or killing
                        // the app right after toggling. Compose's scope gets
                        // cancelled on composition leave, which was dropping
                        // the write and leaving COD re-enabled after reboot.
                        persistScope.launch {
                            val updated = cod.copy(enabled = enabled)
                            settingsRepo.updateConnectOnDemand(updated)
                            if (enabled) {
                                networkMonitor.start()
                                networkMonitor.reevaluate()
                                // v0.9.15.24 — auto-FGS for standby
                                // reaction. See PrivycsApp.onCreate's
                                // gate (same logic).
                                try {
                                    val intent = android.content.Intent(
                                        context,
                                        com.privycs.vpn.service.PrivycsVpnService::class.java,
                                    ).apply {
                                        action = com.privycs.vpn.service.PrivycsVpnService.ACTION_START_MONITOR
                                    }
                                    androidx.core.content.ContextCompat.startForegroundService(context, intent)
                                } catch (_: Throwable) { /* best-effort */ }

                                // v0.9.15.24 — battery-optimization
                                // exemption prompt. FGS alone is NOT
                                // enough for Doze-resistant WiFi-event
                                // delivery; the app also has to be on
                                // the OS's battery-optimization-
                                // exemption list. Empirically confirmed:
                                // user reported keepMonitorAlive=true
                                // (FGS running) AND COD still missed
                                // WiFi joins during Doze. Only the
                                // battery-opt exemption fixes it.
                                // Single per-package system dialog,
                                // user grants or denies, OS persists.
                                if (!com.privycs.vpn.util.BatteryOptimizationHelper
                                        .isIgnoringBatteryOptimizations(context)
                                ) {
                                    val act = context as? android.app.Activity
                                    if (act != null) {
                                        com.privycs.vpn.util.BatteryOptimizationHelper
                                            .openBatteryOptimizationDialog(act)
                                    }
                                }
                            } else {
                                networkMonitor.stop()
                                // Tear the tunnel down when the user turns
                                // COD off while connected. Rationale: in
                                // practice the tunnel is usually up because
                                // COD drove it up; a user turning COD off
                                // expects "stop auto-managing and stop NOW",
                                // not "stop auto-managing but leave me
                                // connected". If they want to stay connected
                                // they can just not toggle COD off. Going
                                // through ConnectCoordinator with USER source
                                // also disarms the Kill Switch so the
                                // sinkhole doesn't engage on this teardown.
                                val manager = com.privycs.vpn.service.VpnServiceManager
                                    .getInstance(context)
                                if (manager.isConnected) {
                                    com.privycs.vpn.util.ConnectCoordinator.requestDisconnect(
                                        context,
                                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.USER,
                                    )
                                }
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
                                    persistScope.launch {
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
                                        persistScope.launch {
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
                                            persistScope.launch {
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
                                                        persistScope.launch {
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

                    // v0.9.15.31: SSID-detection permissions panel.
                    // Surfaces the THREE independent gates that
                    // control whether WifiManager returns an SSID:
                    // Fine-Location permission, the OS-wide Location
                    // Services toggle, and (Android 13+) the
                    // NEARBY_WIFI_DEVICES permission. User-reported
                    // case: "Berechtigung Standort granted but SSID
                    // not detected" — almost always Location Services
                    // off OR (on Android 13+) NEARBY_WIFI_DEVICES not
                    // granted. The status pills + actions here let
                    // the user fix the right gate without hunting
                    // through system Settings.
                    Spacer(modifier = Modifier.height(12.dp))
                    Text(
                        "SSID detection",
                        style = MaterialTheme.typography.titleSmall,
                        color = MaterialTheme.colorScheme.onSurface,
                    )
                    Spacer(modifier = Modifier.height(4.dp))
                    SsidPermissionsPanel(
                        context = context,
                        ssidPermLauncher = ssidPermLauncher,
                    )
                }

                // v0.9.14.75 — opt-in foreground-keepalive. Default
                // off; turning it on starts PrivycsVpnService as a
                // foreground service even without a tunnel so
                // NetworkMonitor's tick + system NetworkCallback
                // survive Doze and on-demand reaction in standby
                // stays <1 s. Trade-off: persistent low-priority
                // notification entry. Without this, on-demand can
                // take up to 15 min to react when the screen is off
                // (WorkManager backstop is the OS hard floor).
                if (settings.connectOnDemand.enabled) {
                    Spacer(modifier = Modifier.height(8.dp))
                    SettingsToggle(
                        title = "Always monitor (faster reaction in standby)",
                        description = "Keeps a low-priority notification so on-demand rules apply within 1-2 seconds even when the screen is off. Off = up to 15 min in deep idle.",
                        checked = settings.keepMonitorAlive,
                        onCheckedChange = { enabled ->
                            persistScope.launch {
                                settingsRepo.setKeepMonitorAlive(enabled)
                                if (enabled) {
                                    val intent = android.content.Intent(
                                        context,
                                        com.privycs.vpn.service.PrivycsVpnService::class.java,
                                    ).apply {
                                        action = com.privycs.vpn.service.PrivycsVpnService.ACTION_START_MONITOR
                                    }
                                    androidx.core.content.ContextCompat.startForegroundService(
                                        context, intent
                                    )
                                } else {
                                    val intent = android.content.Intent(
                                        context,
                                        com.privycs.vpn.service.PrivycsVpnService::class.java,
                                    ).apply {
                                        action = com.privycs.vpn.service.PrivycsVpnService.ACTION_STOP_MONITOR
                                    }
                                    context.startService(intent)
                                }
                            }
                        }
                    )
                }
            }
        }
    }
}

// ============================================================================
// v0.9.15.31: SSID-detection permissions panel
// ============================================================================

/**
 * Holds the two permission launchers wired in SettingsScreen so
 * the SsidPermissionsPanel composable can fire either dialog
 * without re-wiring rememberLauncherForActivityResult internally
 * (which would lose state across recompositions).
 */
data class SsidPermLaunchers(
    val location: androidx.activity.result.ActivityResultLauncher<String>,
    val nearbyWifi: androidx.activity.result.ActivityResultLauncher<String>,
)

@androidx.compose.runtime.Composable
private fun SsidPermissionsPanel(
    context: android.content.Context,
    ssidPermLauncher: SsidPermLaunchers,
) {
    // Recompute state on every recomposition so toggles flipped
    // in system settings reflect when the user comes back into
    // our UI. Permission grants don't fire a recomposition
    // automatically, but coming back to Settings re-renders the
    // screen which re-runs these checks.
    val locationGranted = com.privycs.vpn.util.SsidPermissionsHelper
        .hasFineLocationPermission(context)
    val nearbyWifiGranted = com.privycs.vpn.util.SsidPermissionsHelper
        .hasNearbyWifiPermission(context)
    val locationServicesOn = com.privycs.vpn.util.SsidPermissionsHelper
        .isLocationServicesEnabled(context)

    Column(
        modifier = Modifier.fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        SsidPermissionRow(
            title = "Location permission",
            description = "Required by Android to read the connected Wi-Fi SSID. Pre-Android-13 it is the only gate; on Android 13+ it is paired with Nearby Wi-Fi.",
            granted = locationGranted,
            actionLabel = if (locationGranted) "App settings" else "Grant",
            onAction = {
                if (locationGranted) {
                    safeOpenSettings(
                        context,
                        com.privycs.vpn.util.SsidPermissionsHelper
                            .openAppDetailsIntent(context),
                        "Could not open App settings",
                    )
                } else {
                    ssidPermLauncher.location.launch(
                        android.Manifest.permission.ACCESS_FINE_LOCATION,
                    )
                }
            },
        )

        SsidPermissionRow(
            title = "Location services (OS toggle)",
            description = "System → Location must be ON for the Wi-Fi APIs to return the SSID — even with the permission granted. The most common reason for \"Cannot determine SSID\".",
            granted = locationServicesOn,
            actionLabel = "Location settings",
            onAction = {
                safeOpenSettings(
                    context,
                    com.privycs.vpn.util.SsidPermissionsHelper
                        .openLocationSettingsIntent(),
                    "Could not open Location settings",
                )
            },
        )

        // Nearby-Wi-Fi only matters on Android 13+. Pre-13 the
        // helper returns granted=true unconditionally; we hide
        // the row so it doesn't look like a phantom permission
        // the user can't grant.
        if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.TIRAMISU) {
            SsidPermissionRow(
                title = "Nearby Wi-Fi devices",
                description = "Android 13+: granted alongside Location permission lets the SSID-rule matching work even when the OS Location toggle is off on some ROMs.",
                granted = nearbyWifiGranted,
                actionLabel = if (nearbyWifiGranted) "App settings" else "Grant",
                onAction = {
                    if (nearbyWifiGranted) {
                        safeOpenSettings(
                            context,
                            com.privycs.vpn.util.SsidPermissionsHelper
                                .openAppDetailsIntent(context),
                            "Could not open App settings",
                        )
                    } else {
                        ssidPermLauncher.nearbyWifi.launch(
                            android.Manifest.permission.NEARBY_WIFI_DEVICES,
                        )
                    }
                },
            )
        }
    }
}

@androidx.compose.runtime.Composable
private fun SsidPermissionRow(
    title: String,
    description: String,
    granted: Boolean,
    actionLabel: String,
    onAction: () -> Unit,
) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
                Text(
                    text = title,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurface,
                )
                Spacer(modifier = Modifier.width(6.dp))
                Text(
                    text = if (granted) "Granted" else "Denied",
                    style = MaterialTheme.typography.labelSmall,
                    color = if (granted)
                        com.privycs.vpn.ui.theme.PrivycsTeal
                    else
                        MaterialTheme.colorScheme.error,
                )
            }
            Text(
                text = description,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        Spacer(modifier = Modifier.width(8.dp))
        androidx.compose.material3.TextButton(onClick = onAction) {
            Text(actionLabel)
        }
    }
}

/**
 * Launch a system-settings intent defensively. The SSID-permission
 * rows previously called context.startActivity() bare — on ROMs
 * where ACTION_LOCATION_SOURCE_SETTINGS / ACTION_APPLICATION_DETAILS
 * isn't directly resolvable this threw ActivityNotFoundException and
 * the link "failed" (user-reported). Fall back to the always-present
 * top-level system Settings, and only then surface a Toast.
 */
private fun safeOpenSettings(context: Context, primary: Intent, failMsg: String) {
    runCatching { context.startActivity(primary) }
        .onFailure {
            runCatching {
                context.startActivity(
                    Intent(Settings.ACTION_SETTINGS)
                        .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                )
            }.onFailure {
                Toast.makeText(context, failMsg, Toast.LENGTH_SHORT).show()
            }
        }
}
