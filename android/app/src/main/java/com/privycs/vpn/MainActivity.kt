package com.privycs.vpn

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
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

// v0.9.15.75: the open-test build ships WITHOUT ACCESS_BACKGROUND_LOCATION
// — declaring it would trigger Google Play's background-location
// declaration form + demo-video review gate and block the open-test
// launch. SSID-based Connect-on-Demand therefore runs foreground-only
// for now (NetworkMonitor already degrades: the OS redacts the SSID to
// a backgrounded app without this permission). To re-enable for the
// production release: flip this to true AND re-add the
// <uses-permission ACCESS_BACKGROUND_LOCATION> line in AndroidManifest.xml
// (then prepare the Play Console declaration form + demo video).
private const val BACKGROUND_LOCATION_ENABLED = false

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
                    var showPermissionDialog by remember { mutableStateOf(false) }
                    var showBackgroundDialog by remember { mutableStateOf(false) }

                    fun finishFirstRun() {
                        coScope.launch {
                            settingsRepository.markFirstLaunchCompleted()
                        }
                    }

                    // Background-location request (Android 10+). Asked
                    // ONLY after foreground location is granted (OS
                    // requirement) and SOLELY so Android reveals the
                    // Wi-Fi name to the backgrounded app for on-demand
                    // rules. Never used for geolocation; nothing leaves
                    // the device. Ends the first-run flow either way.
                    val backgroundLocationLauncher = rememberLauncherForActivityResult(
                        ActivityResultContracts.RequestPermission()
                    ) { _ -> finishFirstRun() }

                    // Foreground batch: notifications (13+) + fine
                    // location + nearby-wifi (13+). On result, if fine
                    // location is granted and the OS gates background
                    // separately, surface the background rationale;
                    // otherwise the first-run flow ends here.
                    val foregroundPermsLauncher = rememberLauncherForActivityResult(
                        ActivityResultContracts.RequestMultiplePermissions()
                    ) { _ ->
                        val fineGranted = ContextCompat.checkSelfPermission(
                            this@MainActivity,
                            Manifest.permission.ACCESS_FINE_LOCATION,
                        ) == PackageManager.PERMISSION_GRANTED
                        if (BACKGROUND_LOCATION_ENABLED && fineGranted &&
                            Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q
                        ) {
                            showBackgroundDialog = true
                        } else {
                            finishFirstRun()
                        }
                    }

                    LaunchedEffect(settings.firstLaunchCompleted) {
                        if (settings.firstLaunchCompleted) return@LaunchedEffect
                        val fineAlready = ContextCompat.checkSelfPermission(
                            this@MainActivity,
                            Manifest.permission.ACCESS_FINE_LOCATION
                        ) == PackageManager.PERMISSION_GRANTED
                        if (fineAlready) {
                            // Foreground already granted (e.g. reinstall).
                            // Offer the background step if the OS gates
                            // it separately and it isn't granted yet;
                            // otherwise finish.
                            val bgMissing = BACKGROUND_LOCATION_ENABLED &&
                                Build.VERSION.SDK_INT >=
                                Build.VERSION_CODES.Q &&
                                ContextCompat.checkSelfPermission(
                                    this@MainActivity,
                                    Manifest.permission.ACCESS_BACKGROUND_LOCATION,
                                ) != PackageManager.PERMISSION_GRANTED
                            if (bgMissing) {
                                showBackgroundDialog = true
                            } else {
                                settingsRepository.markFirstLaunchCompleted()
                            }
                        } else {
                            showPermissionDialog = true
                        }
                    }

                    if (showPermissionDialog) {
                        FirstRunPermissionDisclosureDialog(
                            onGrant = {
                                showPermissionDialog = false
                                val perms = buildList {
                                    add(Manifest.permission.ACCESS_FINE_LOCATION)
                                    if (Build.VERSION.SDK_INT >=
                                        Build.VERSION_CODES.TIRAMISU
                                    ) {
                                        add(Manifest.permission.POST_NOTIFICATIONS)
                                        add(Manifest.permission.NEARBY_WIFI_DEVICES)
                                    }
                                }.toTypedArray()
                                foregroundPermsLauncher.launch(perms)
                            },
                            onSkip = {
                                showPermissionDialog = false
                                finishFirstRun()
                            }
                        )
                    }

                    if (showBackgroundDialog) {
                        BackgroundLocationRationaleDialog(
                            onGrant = {
                                showBackgroundDialog = false
                                backgroundLocationLauncher.launch(
                                    Manifest.permission.ACCESS_BACKGROUND_LOCATION
                                )
                            },
                            onSkip = {
                                showBackgroundDialog = false
                                finishFirstRun()
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

// Prominent disclosure (Google Play requirement): shown BEFORE any
// system permission prompt, clearly stating what is accessed and why,
// with an explicit accept/decline.
@androidx.compose.runtime.Composable
private fun FirstRunPermissionDisclosureDialog(
    onGrant: () -> Unit,
    onSkip: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onSkip,
        title = { Text("Permissions Privycs uses") },
        text = {
            Text(
                "To work fully, Privycs asks for a few permissions now:\n\n" +
                    "• Notifications — to show VPN status and on-demand " +
                    "events.\n" +
                    "• Location / Nearby Wi-Fi — required by Android only " +
                    "so the app can read the NAME of your current Wi-Fi " +
                    "network for Connect-on-Demand rules (e.g. \"VPN off " +
                    "on my home Wi-Fi\"). It is never used to determine " +
                    "your geographic location, and nothing is ever sent " +
                    "off your device.\n\n" +
                    if (BACKGROUND_LOCATION_ENABLED) {
                        "After that you'll be asked to \"Allow all the " +
                            "time\" — Android needs that so on-demand " +
                            "rules keep working while the app is closed " +
                            "or the screen is off. You can change any " +
                            "of this later in Settings."
                    } else {
                        "Connect-on-Demand by Wi-Fi name works while " +
                            "Privycs is open on screen. You can change " +
                            "any of this later in Settings."
                    }
            )
        },
        confirmButton = {
            TextButton(onClick = onGrant) { Text("Continue") }
        },
        dismissButton = {
            TextButton(onClick = onSkip) { Text("Not now") }
        }
    )
}

@androidx.compose.runtime.Composable
private fun BackgroundLocationRationaleDialog(
    onGrant: () -> Unit,
    onSkip: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onSkip,
        title = { Text("Allow Wi-Fi rules in the background?") },
        text = {
            Text(
                "On the next screen, choose \"Allow all the time\". " +
                    "Android only reveals your Wi-Fi network name to a " +
                    "backgrounded app with this setting — without it, " +
                    "Connect-on-Demand rules based on Wi-Fi name only " +
                    "work while Privycs is open on screen.\n\n" +
                    "Privycs uses this solely to read the Wi-Fi name " +
                    "locally for your rules. It never derives or " +
                    "transmits your location."
            )
        },
        confirmButton = {
            TextButton(onClick = onGrant) { Text("Continue") }
        },
        dismissButton = {
            TextButton(onClick = onSkip) { Text("Skip") }
        }
    )
}
