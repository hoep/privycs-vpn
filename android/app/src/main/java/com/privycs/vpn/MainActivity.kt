package com.privycs.vpn

import android.Manifest
import android.content.pm.PackageManager
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.core.content.ContextCompat
import com.privycs.vpn.data.models.AppTheme
import com.privycs.vpn.navigation.AppNavigation
import com.privycs.vpn.ui.theme.PrivycsVpnTheme
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()

        val settingsRepository = PrivycsApp.instance.settingsRepository

        setContent {
            val settings by settingsRepository.settingsFlow.collectAsState(
                initial = settingsRepository.defaultSettings()
            )

            val darkTheme = when (settings.theme) {
                AppTheme.DARK -> true
                AppTheme.LIGHT -> false
                AppTheme.SYSTEM -> null
            }

            PrivycsVpnTheme(darkTheme = darkTheme) {
                Surface(
                    modifier = Modifier.fillMaxSize(),
                    color = MaterialTheme.colorScheme.background
                ) {
                    AppNavigation()

                    // Post-install location-permission rationale.
                    // Shown exactly once - guarded by
                    // settings.firstLaunchCompleted. The user can
                    // grant or skip; either way we mark it complete
                    // and never re-prompt automatically. Settings
                    // screen still has its own permission button so
                    // users who skipped can grant later.
                    //
                    // Why ACCESS_FINE_LOCATION at first launch:
                    // Android 8+ requires this permission for
                    // WifiManager.connectionInfo to return a real
                    // SSID. Without it, the COD "wifi + only/except
                    // SSID list" rules cannot match the user's
                    // network and silent rule-mismatch is the most
                    // common COD-doesn't-fire support report.
                    // Asking proactively + with a rationale closes
                    // that whole class of confusion at install time.
                    val coScope = rememberCoroutineScope()
                    var showPermissionDialog by remember {
                        mutableStateOf(false)
                    }

                    val permissionLauncher = rememberLauncherForActivityResult(
                        ActivityResultContracts.RequestPermission()
                    ) { _ ->
                        // Mark first-launch complete regardless of
                        // grant/deny. The user made their choice;
                        // re-prompting on every app open would be
                        // hostile UX.
                        coScope.launch {
                            settingsRepository.markFirstLaunchCompleted()
                        }
                    }

                    LaunchedEffect(settings.firstLaunchCompleted) {
                        if (settings.firstLaunchCompleted) return@LaunchedEffect
                        val granted = ContextCompat.checkSelfPermission(
                            this@MainActivity,
                            Manifest.permission.ACCESS_FINE_LOCATION
                        ) == PackageManager.PERMISSION_GRANTED
                        if (granted) {
                            // Already granted (e.g. app reinstalled
                            // without revoking the permission).
                            // Skip dialog, just mark complete.
                            settingsRepository.markFirstLaunchCompleted()
                        } else {
                            showPermissionDialog = true
                        }
                    }

                    if (showPermissionDialog) {
                        LocationPermissionRationaleDialog(
                            onGrant = {
                                showPermissionDialog = false
                                permissionLauncher.launch(
                                    Manifest.permission.ACCESS_FINE_LOCATION
                                )
                            },
                            onSkip = {
                                showPermissionDialog = false
                                coScope.launch {
                                    settingsRepository.markFirstLaunchCompleted()
                                }
                            }
                        )
                    }

                    // v0.9.15.24 — one-time battery-optimization
                    // exemption prompt for users who had COD enabled
                    // BEFORE this version (where the prompt was first
                    // added to the toggle handler). Fires only when:
                    //   - COD is currently enabled
                    //   - we're NOT already on the exemption list
                    //   - first-launch dialog is already done so we
                    //     don't stack two system dialogs back-to-back
                    // The system dialog itself is single-shot per
                    // launch (user grants once, OS persists). Re-
                    // checking on every app start in case the user
                    // later denied or the OS auto-revoked.
                    LaunchedEffect(
                        settings.firstLaunchCompleted,
                        settings.connectOnDemand.enabled,
                    ) {
                        if (!settings.firstLaunchCompleted) return@LaunchedEffect
                        if (!settings.connectOnDemand.enabled) return@LaunchedEffect
                        if (com.privycs.vpn.util.BatteryOptimizationHelper
                                .isIgnoringBatteryOptimizations(this@MainActivity)
                        ) return@LaunchedEffect
                        com.privycs.vpn.util.BatteryOptimizationHelper
                            .openBatteryOptimizationDialog(this@MainActivity)
                    }
                }
            }
        }
    }
}

@androidx.compose.runtime.Composable
private fun LocationPermissionRationaleDialog(
    onGrant: () -> Unit,
    onSkip: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onSkip,
        title = { Text("Allow WiFi name detection?") },
        text = {
            Text(
                "Privycs needs Location access to read the name of " +
                    "your current WiFi network. This is required if you " +
                    "want to use Connect-on-Demand rules based on WiFi " +
                    "name (e.g. \"VPN only outside my home network\").\n\n" +
                    "Privycs does NOT send location data anywhere. The " +
                    "permission is used only locally for SSID detection."
            )
        },
        confirmButton = {
            TextButton(onClick = onGrant) {
                Text("Allow")
            }
        },
        dismissButton = {
            TextButton(onClick = onSkip) {
                Text("Later")
            }
        }
    )
}
