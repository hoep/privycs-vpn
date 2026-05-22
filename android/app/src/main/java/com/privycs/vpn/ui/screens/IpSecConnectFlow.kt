package com.privycs.vpn.ui.screens

import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.security.KeyChain
import android.util.Log
import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalContext
import com.privycs.vpn.R
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
    val connectionRepo = com.privycs.vpn.PrivycsApp.instance.connectionRepository

    // Two-step flow: first the system's PKCS#12 install dialog, then the
    // KeyChain.choosePrivateKeyAlias callback. The launcher handles the
    // first hop; the KeyChain callback handles the second.
    //
    // We intentionally do NOT capture the pending connection in remember{}
    // state: the Android KeyChain install dialog can cause process recreation
    // on low-memory devices, which wipes non-saveable Compose state and
    // leaves the result callback with null. Re-reading the active connection
    // from the repository at callback time is stateless and safe across
    // recreation.
    val installLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode != Activity.RESULT_OK) {
            onError(context.getString(R.string.ipsecflow_error_install_cancelled))
            return@rememberLauncherForActivityResult
        }
        val activity = context as? Activity
        if (activity == null) {
            onError(context.getString(R.string.ipsecflow_error_no_activity))
            return@rememberLauncherForActivityResult
        }
        val conn = connectionRepo.getActive()
        val config = conn?.getActiveConfig()
        if (conn == null || config == null) {
            onError(context.getString(R.string.ipsecflow_error_connection_disappeared))
            return@rememberLauncherForActivityResult
        }
        pickAliasAndConnect(activity, context, conn, config.configContent, onReady, onError)
    }

    return remember(onReady, onError) {
        prep@ { connection ->
            val config = connection.getActiveConfig()
            if (config == null) {
                onError(context.getString(R.string.ipsecflow_error_no_active_config, connection.name))
                return@prep
            }
            val tunnel = IpSecTunnel(context)
            try {
                tunnel.parseConfig(config.configContent)
            } catch (e: Exception) {
                onError(context.getString(R.string.ipsecflow_error_parse_failed, e.message ?: ""))
                return@prep
            }

            // Already installed? Skip the two-step dance.
            if (tunnel.getInstalledAlias() != null) {
                onReady()
                return@prep
            }

            val installIntent = tunnel.createKeyChainInstallIntent(connection.name)
            if (installIntent == null) {
                onError(context.getString(R.string.ipsecflow_error_no_pkcs12_bundle))
                return@prep
            }
            // Android's KeyChain install dialog has no API to pre-fill the
            // PKCS#12 password - the user must type it manually. Copy it to
            // the clipboard and toast the visible prompt so they can paste
            // straight into the dialog.
            val p12Password = tunnel.getP12Password()
            if (p12Password.isNotEmpty()) {
                val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE)
                        as? ClipboardManager
                clipboard?.setPrimaryClip(
                    ClipData.newPlainText("PKCS#12 password", p12Password)
                )
                Toast.makeText(
                    context,
                    context.getString(R.string.ipsecflow_toast_pkcs12_password, p12Password),
                    Toast.LENGTH_LONG
                ).show()
            }
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
                    onError(context.getString(R.string.ipsecflow_error_no_certificate_selected))
                    return@runOnUiThread
                }
                try {
                    val tunnel = IpSecTunnel(context)
                    tunnel.parseConfig(configContent)
                    tunnel.rememberInstalledAlias(alias)
                    Log.d("IpSecConnectFlow", "Remembered alias='$alias' for ${connection.name}")
                    onReady()
                } catch (e: Exception) {
                    onError(context.getString(R.string.ipsecflow_error_record_alias_failed, e.message ?: ""))
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
