package com.privycs.vpn.ui.tv

import android.app.Activity
import android.content.Context
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import com.privycs.vpn.data.models.AppTheme
import com.privycs.vpn.service.VpnServiceManager
import com.privycs.vpn.ui.theme.PrivycsVpnTheme
import com.privycs.vpn.util.AppLocale

/**
 * Leanback (Android TV / Google TV) entry point.
 *
 * Separate from [com.privycs.vpn.MainActivity] so the phone and TV entry
 * points stay clean: the manifest gives THIS activity the
 * LEANBACK_LAUNCHER intent-filter + a TV banner, while MainActivity keeps
 * the ordinary LAUNCHER filter for phones/tablets. Both drive the SAME
 * engine — [VpnServiceManager] / ConnectCoordinator / the repositories /
 * the protocol stack are reused unchanged; this activity only supplies a
 * D-pad / focus-friendly Compose-for-TV UI form factor.
 *
 * The TV form factor deliberately drops QR import, per-app VPN, the
 * network-rules engine and IPSec setup (TV_PORT_PLAN.md §5). Enrollment
 * is the device-code flow (TvEnrollScreen) with a manual gateway-URL +
 * token fallback.
 */
class TvActivity : ComponentActivity() {

    // Mirror MainActivity: apply the in-app language choice before the UI
    // inflates (Android 8–12 path; 13+ uses the framework LocaleManager).
    override fun attachBaseContext(newBase: Context) {
        super.attachBaseContext(AppLocale.wrap(newBase))
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val settingsRepository =
            com.privycs.vpn.PrivycsApp.instance.settingsRepository
        val vpnManager = VpnServiceManager.getInstance(applicationContext)

        setContent {
            val settings by settingsRepository.settingsFlow.collectAsState(
                initial = settingsRepository.defaultSettings()
            )

            val darkTheme = when (settings.theme) {
                AppTheme.DARK -> true
                AppTheme.LIGHT -> false
                // TVs are overwhelmingly used in dark living rooms and
                // many cheap panels report no system dark-mode signal;
                // default the "system" choice to dark on TV for legibility.
                AppTheme.SYSTEM -> true
            }

            PrivycsVpnTheme(darkTheme = darkTheme) {
                Surface(
                    modifier = Modifier.fillMaxSize(),
                    color = MaterialTheme.colorScheme.background,
                ) {
                    // VpnService consent launcher. VpnService.prepare()'s
                    // system consent dialog is fully D-pad navigable on
                    // Android TV, so the same ActivityResult pattern the
                    // phone uses works here. On RESULT_OK we kick the
                    // connect the user asked for.
                    val vpnPermissionLauncher = rememberLauncherForActivityResult(
                        contract = ActivityResultContracts.StartActivityForResult()
                    ) { result ->
                        if (result.resultCode == Activity.RESULT_OK) {
                            vpnManager.connect()
                        }
                    }

                    val requestConnect = remember(vpnManager) {
                        {
                            val prepareIntent = vpnManager.prepareVpn()
                            if (prepareIntent != null) {
                                vpnPermissionLauncher.launch(prepareIntent)
                            } else {
                                vpnManager.connect()
                            }
                        }
                    }

                    TvRootScreen(
                        vpnManager = vpnManager,
                        onRequestConnect = requestConnect,
                    )
                }
            }
        }
    }
}
