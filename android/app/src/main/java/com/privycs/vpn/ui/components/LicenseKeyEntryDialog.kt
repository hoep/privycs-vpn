package com.privycs.vpn.ui.components

import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import com.privycs.vpn.R
import com.privycs.vpn.data.EntitlementRepository
import com.privycs.vpn.license.License

/**
 * Modal for redeeming a Privycs cross-platform bundle license key.
 *
 * Two entry methods, both feed the same [EntitlementRepository
 * .activateLicenseKey] call:
 *  - **Paste** — user pastes the PRVC-...-... string from the purchase
 *    email into the text field.
 *  - **File pick** — user selects a `.privycs-license` file (the same
 *    email's attachment) via SAF; we read the body into the text field.
 *
 * On success: emits the granted SKU back via [onActivated]; the parent
 * (ProUpgradeScreen) re-renders the active card off the existing
 * EntitlementRepository.isPro StateFlow.
 *
 * On failure: an inline error line maps the License.ErrorKind to a
 * localised message — bad-signature vs wrong-platform vs malformed
 * each get their own copy. Generic "activation failed" only when the
 * error kind is opaque.
 */
@Composable
fun LicenseKeyEntryDialog(
    repo: EntitlementRepository,
    onActivated: (sku: String) -> Unit,
    onDismiss: () -> Unit,
) {
    val context = LocalContext.current
    var keyInput by remember { mutableStateOf("") }
    var errorMessage by remember { mutableStateOf<String?>(null) }
    var activating by remember { mutableStateOf(false) }

    // SAF file picker — accepts any text-y MIME because the upstream
    // emails use various types (text/plain, application/octet-stream,
    // x-privycs-license …). We sniff the content client-side.
    val filePicker = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.OpenDocument(),
    ) { uri: Uri? ->
        if (uri != null) {
            try {
                val bytes = context.contentResolver.openInputStream(uri)?.use { it.readBytes() }
                if (bytes != null) {
                    keyInput = String(bytes, Charsets.UTF_8).trim()
                    errorMessage = null
                }
            } catch (_: Throwable) {
                errorMessage = context.getString(R.string.pro_errors_file_read_failed)
            }
        }
    }

    // Error-mapping table: map License.ErrorKind to a string resource so
    // bad-sig / wrong-platform get tailored copy. The generic kind is a
    // fallback for ErrorKind enum additions we forgot to handle.
    fun mapErrorKind(kind: License.ErrorKind): String {
        val resId = when (kind) {
            License.ErrorKind.BAD_SIGNATURE -> R.string.pro_errors_bad_signature
            License.ErrorKind.WRONG_PLATFORM -> R.string.pro_errors_wrong_platform
            License.ErrorKind.MALFORMED -> R.string.pro_errors_malformed
            License.ErrorKind.UNSUPPORTED_VERSION -> R.string.pro_errors_unsupported_version
            License.ErrorKind.NO_PUBLIC_KEY -> R.string.pro_errors_no_public_key
            License.ErrorKind.UNKNOWN_TIER -> R.string.pro_errors_generic
        }
        return context.getString(resId)
    }

    AlertDialog(
        onDismissRequest = { if (!activating) onDismiss() },
        title = { Text(stringResource(R.string.pro_activate_title)) },
        text = {
            Column {
                Text(
                    stringResource(R.string.pro_activate_subtitle),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Spacer(Modifier.height(12.dp))
                OutlinedTextField(
                    value = keyInput,
                    onValueChange = {
                        keyInput = it
                        errorMessage = null
                    },
                    placeholder = { Text(stringResource(R.string.pro_activate_placeholder)) },
                    enabled = !activating,
                    modifier = Modifier
                        .fillMaxWidth()
                        .height(120.dp),
                    minLines = 3,
                    maxLines = 5,
                )
                Spacer(Modifier.height(8.dp))
                OutlinedButton(
                    onClick = {
                        // Trigger SAF picker. Empty MIME-type list ==
                        // "show everything text-ish"; SAF will let the
                        // user pick any file regardless of extension.
                        filePicker.launch(arrayOf("*/*"))
                    },
                    enabled = !activating,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text(stringResource(R.string.pro_activate_choose_file))
                }
                if (errorMessage != null) {
                    Spacer(Modifier.height(8.dp))
                    Text(
                        errorMessage!!,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.error,
                    )
                }
            }
        },
        confirmButton = {
            TextButton(
                onClick = {
                    if (keyInput.isBlank()) return@TextButton
                    activating = true
                    when (val res = repo.activateLicenseKey(keyInput.trim())) {
                        is EntitlementRepository.LicenseActivationResult.Ok -> {
                            activating = false
                            onActivated(res.sku)
                        }

                        is EntitlementRepository.LicenseActivationResult.Err -> {
                            activating = false
                            errorMessage = mapErrorKind(res.kind)
                        }
                    }
                },
                enabled = !activating && keyInput.isNotBlank(),
            ) {
                Text(
                    if (activating) {
                        stringResource(R.string.pro_activate_activating)
                    } else {
                        stringResource(R.string.pro_activate_activate)
                    },
                )
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss, enabled = !activating) {
                Text(stringResource(R.string.pro_activate_cancel))
            }
        },
    )
}
