package com.privycs.vpn.ui.screens

import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import android.widget.Toast
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
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
import com.privycs.vpn.billing.BillingManager
import com.privycs.vpn.data.EntitlementRepository
import com.privycs.vpn.ui.components.LicenseKeyEntryDialog

/**
 * "Privycs Pro" upgrade screen — the one-time purchase that unlocks the
 * advanced features, via Google Play Billing.
 *
 * Privycs is a self-hosted VPN *management* app (the user runs their own
 * gateway), so the copy says "advanced VPN management" — never "secure
 * VPN service", which would mis-suggest a hosted tunnel.
 *
 * The cross-platform bundle (Android + Desktop + iOS, sold on
 * privycs.com) is mentioned as plain text only — no in-app link, per
 * Play's anti-steering stance. Its licence-key redemption path is not
 * wired yet; EntitlementRepository.Source.BUNDLE is the prepared hook.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ProUpgradeScreen(onBack: () -> Unit) {
    val billing = remember { PrivycsApp.instance.billingManager }
    val entitlementRepo = remember { PrivycsApp.instance.entitlementRepository }
    val isPro by entitlementRepo.isPro.collectAsState()
    var showLicenseDialog by remember { mutableStateOf(false) }
    val context = LocalContext.current

    if (showLicenseDialog) {
        LicenseKeyEntryDialog(
            repo = entitlementRepo,
            onActivated = { sku ->
                showLicenseDialog = false
                Toast.makeText(
                    context,
                    context.getString(R.string.pro_activated_toast, sku),
                    Toast.LENGTH_SHORT,
                ).show()
            },
            onDismiss = { showLicenseDialog = false },
        )
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        stringResource(R.string.pro_title),
                        fontWeight = FontWeight.SemiBold,
                    )
                },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(
                            Icons.AutoMirrored.Filled.ArrowBack,
                            contentDescription = stringResource(R.string.pro_back),
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
                .padding(horizontal = 20.dp),
        ) {
            Spacer(Modifier.height(8.dp))
            Text(
                stringResource(R.string.pro_headline),
                style = MaterialTheme.typography.headlineSmall,
                fontWeight = FontWeight.Bold,
            )
            Spacer(Modifier.height(6.dp))
            Text(
                stringResource(R.string.pro_subhead),
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(20.dp))

            Card(
                colors = CardDefaults.cardColors(
                    containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f),
                ),
                modifier = Modifier.fillMaxWidth(),
            ) {
                Column(modifier = Modifier.padding(vertical = 8.dp, horizontal = 16.dp)) {
                    for (res in PRO_FEATURE_STRINGS) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Icon(
                                Icons.Filled.CheckCircle,
                                contentDescription = null,
                                tint = MaterialTheme.colorScheme.primary,
                                modifier = Modifier.size(20.dp),
                            )
                            Spacer(Modifier.width(12.dp))
                            Text(
                                stringResource(res),
                                style = MaterialTheme.typography.bodyMedium,
                                modifier = Modifier.padding(vertical = 8.dp),
                            )
                        }
                    }
                }
            }
            Spacer(Modifier.height(20.dp))

            if (isPro) {
                ProActiveCard()
            } else if (EntitlementRepository.PLAY_BILLING_ENABLED) {
                ProPurchaseSection(billing)
            } else {
                // v1.0.5.4: Play Billing UI is hidden until the
                // `privycs_pro_lifetime` managed product is live in
                // Play Console (i.e. once we leave Closed Testing
                // for Production). The bundle-key activation path
                // below stays visible so cross-platform-bundle
                // holders can still activate Pro on Android during
                // Alpha / Closed Testing.
                Text(
                    stringResource(R.string.pro_billing_coming_soon),
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }

            Spacer(Modifier.height(20.dp))
            // Cross-platform bundle redemption — present regardless of
            // the Play-purchase state so a user who bought the bundle
            // on Desktop can also activate it here. The dialog is a
            // pure addition; the Play path stays primary.
            OutlinedButton(
                onClick = { showLicenseDialog = true },
                modifier = Modifier.fillMaxWidth(),
            ) {
                Text(stringResource(R.string.pro_activate_with_key))
            }
            Spacer(Modifier.height(20.dp))
            Text(
                stringResource(R.string.pro_bundle_note),
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(24.dp))
        }
    }
}

private val PRO_FEATURE_STRINGS = listOf(
    R.string.pro_feature_multiprotocol,
    R.string.pro_feature_multiconnection,
    R.string.pro_feature_rules,
    R.string.pro_feature_gateway,
    R.string.pro_feature_pools,
    R.string.pro_feature_splittunnel,
)

@Composable
private fun ProActiveCard() {
    Card(
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.primaryContainer,
        ),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Row(
            modifier = Modifier.padding(16.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(
                Icons.Filled.CheckCircle,
                contentDescription = null,
                tint = MaterialTheme.colorScheme.primary,
            )
            Spacer(Modifier.width(12.dp))
            Text(
                stringResource(R.string.pro_active),
                style = MaterialTheme.typography.titleSmall,
                fontWeight = FontWeight.SemiBold,
            )
        }
    }
}

@Composable
private fun ProPurchaseSection(billing: BillingManager?) {
    val context = LocalContext.current
    if (billing == null) {
        Text(
            stringResource(R.string.pro_billing_unavailable),
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.error,
        )
        return
    }
    val price by billing.price.collectAsState()
    val purchaseFailed = stringResource(R.string.pro_purchase_failed)
    val restoring = stringResource(R.string.pro_restoring)

    Button(
        onClick = {
            val activity = context.findActivity()
            val started = activity != null && billing.launchPurchase(activity)
            if (!started) {
                Toast.makeText(context, purchaseFailed, Toast.LENGTH_SHORT).show()
            }
        },
        modifier = Modifier.fillMaxWidth(),
    ) {
        Text(
            if (price != null) {
                stringResource(R.string.pro_unlock_priced, price!!)
            } else {
                stringResource(R.string.pro_unlock)
            },
        )
    }
    Spacer(Modifier.height(8.dp))
    OutlinedButton(
        onClick = {
            billing.refreshPurchases()
            Toast.makeText(context, restoring, Toast.LENGTH_SHORT).show()
        },
        modifier = Modifier.fillMaxWidth(),
    ) {
        Text(stringResource(R.string.pro_restore))
    }
}

/** Unwrap the hosting Activity from a (possibly wrapped) Compose context. */
private fun Context.findActivity(): Activity? {
    var ctx: Context = this
    while (ctx is ContextWrapper) {
        if (ctx is Activity) return ctx
        ctx = ctx.baseContext
    }
    return null
}
