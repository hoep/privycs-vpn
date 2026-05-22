package com.privycs.vpn.ui.components

import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import com.privycs.vpn.R

/**
 * Generic "this is a Pro feature" dialog, shown when a Free user taps a
 * gated control. [featureName] is the localised name of the feature the
 * user tried to use; [onUpgrade] navigates to the Pro upgrade screen.
 *
 * The gate call sites themselves are behind EntitlementRepository.
 * GATING_ENABLED — while that flag is off this dialog is never shown.
 */
@Composable
fun UpgradeDialog(
    featureName: String,
    onDismiss: () -> Unit,
    onUpgrade: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.pro_dialog_title)) },
        text = { Text(stringResource(R.string.pro_dialog_body, featureName)) },
        confirmButton = {
            TextButton(
                onClick = {
                    onDismiss()
                    onUpgrade()
                },
            ) {
                Text(stringResource(R.string.pro_dialog_see_pro))
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text(stringResource(R.string.pro_dialog_not_now))
            }
        },
    )
}
