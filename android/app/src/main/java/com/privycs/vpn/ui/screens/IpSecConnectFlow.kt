package com.privycs.vpn.ui.screens

import android.app.Activity
import android.content.Context
import android.security.KeyChain
import android.util.Log
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.Composable
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalContext
import com.privycs.vpn.data.models.VpnConnection
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.service.IpSecTunnel

/**
 * Orchestrates the IPSec pre-connect checks that must happen from an
 * Activity context (KeyChain install + alias selection) before the
 * regular VpnServiceManager.connect() call.
 *
 * Typical use in a screen composable:
 *
 *   val ipSecPrep = rememberIpSecConnectPrep(
 *     onReady = { vpnManager.connect() },
 *     onError = { msg -> ... }
 *   )
 *   ...
 *   onClick = {
 *     if (connection.activeProtocol == VpnProtocol.IPSEC) {
 *       ipSecPrep(connection)
 *     } else {
 *       vpnManager.connect()
 *     }
 *   }
 *
 * The returned lambda either drives a two-step install/pick flow via
 * ActivityResultLaunchers and a KeyChain callback, or calls onReady
 * immediately when the PKCS#12 is already installed and the alias is
 * remembered.
 */
@Composable
fun rememberIpSecConnectPrep(
    onReady: () -> Unit,
    onError: (String) -> Unit
): (VpnConnection) -> Unit {
    val context = LocalContext.current
    val pendingConnection = remember { mutableStateOf<VpnConnection?>(null) }

    // Two-step flow: first the system's PKCS#12 install dialog, then the
    // KeyChain.choosePrivateKeyAlias callback. The launcher handles the
    // first hop; the KeyChain callback handles the second.
    val installLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.StartActivityForResult()
    ) { result ->
        val conn = pendingConnection.value
        pendingConnection.value = null
        if (result.resultCode != Activity.RESULT_OK || conn == null) {
            onError("PKCS#12 install cancelled")
            return@rememberLauncherForActivityResult
        }
        val activity = context as? Activity
        if (activity == null) {
            onError("Cannot access Activity to pick KeyChain alias")
            return@rememberLauncherForActivityResult
        }
        val config = conn.getActiveConfig() ?: return@rememberLauncherForActivityResult
        pickAliasAndConnect(activity, context, conn, config.configContent, onReady, onError)
    }

    return remember(onReady, onError) {
        prep@ { connection ->
            val config = connection.getActiveConfig()
            if (config == null) {
                onError("No active config for ${connection.name}")
                return@prep
            }
            val tunnel = IpSecTunnel(context)
            try {
                tunnel.parseConfig(config.configContent)
            } catch (e: Exception) {
                onError("Failed to parse .sswan profile: ${e.message}")
                return@prep
            }

            // Already installed? Skip the two-step dance.
            if (tunnel.getInstalledAlias() != null) {
                onReady()
                return@prep
            }

            val installIntent = tunnel.createKeyChainInstallIntent(connection.name)
            if (installIntent == null) {
                onError("Profile does not contain a PKCS#12 bundle")
                return@prep
            }
            pendingConnection.value = connection
            installLauncher.launch(installIntent)
        }
    }
}

/**
 * After PKCS#12 install, prompt the user to pick the freshly-installed
 * alias. The callback fires on a background thread, so marshaling back
 * to the UI thread happens explicitly.
 */
private fun pickAliasAndConnect(
    activity: Activity,
    context: Context,
    connection: VpnConnection,
    configContent: String,
    onReady: () -> Unit,
    onError: (String) -> Unit
) {
    KeyChain.choosePrivateKeyAlias(
        activity,
        { alias ->
            activity.runOnUiThread {
                if (alias == null) {
                    onError("No certificate selected")
                    return@runOnUiThread
                }
                try {
                    val tunnel = IpSecTunnel(context)
                    tunnel.parseConfig(configContent)
                    tunnel.rememberInstalledAlias(alias)
                    Log.d("IpSecConnectFlow", "Remembered alias='$alias' for ${connection.name}")
                    onReady()
                } catch (e: Exception) {
                    onError("Failed to record alias: ${e.message}")
                }
            }
        },
        /* keyTypes = */ null,
        /* issuers = */ null,
        /* host = */ null,
        /* port = */ -1,
        /* alias = */ null
    )
}

/**
 * Exposed so ConnectScreen can trivially check whether to route through
 * the IPSec prep flow at all.
 */
fun VpnConnection.needsKeyChainPrep(): Boolean =
    activeProtocol == VpnProtocol.IPSEC
