package com.privycs.vpn.ui.tv

import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.service.VpnServiceManager

/**
 * TV navigation root. Two states only (TV is deliberately simple):
 *
 *  - NOT enrolled  → [TvEnrollScreen] (device-code flow + manual fallback)
 *  - enrolled      → [TvConnectScreen] (pick server / pool, connect)
 *
 * "Enrolled" means we have a gateway URL + token to pull configs from,
 * OR we already have at least one saved connection (e.g. a previously
 * pulled config). Either is enough to show the connect surface. A
 * "Re-link / change gateway" affordance in TvConnectScreen lets the user
 * jump back to enrollment.
 */
@Composable
fun TvRootScreen(
    vpnManager: VpnServiceManager,
    onRequestConnect: () -> Unit,
) {
    val app = PrivycsApp.instance
    val settings by app.settingsRepository.settingsFlow
        .collectAsState(initial = app.settingsRepository.defaultSettings())
    val connectionRegistry by app.connectionRepository.registry.collectAsState()
    val poolRegistry by app.poolRepository.registry.collectAsState()

    val hasGateway = settings.gatewayUrl.isNotBlank() && settings.apiKey.isNotBlank()
    val hasConfigs = connectionRegistry.connections.isNotEmpty() ||
        poolRegistry.pools.isNotEmpty()

    // User can force the enrollment screen back open from TvConnectScreen
    // ("Re-link this TV / change gateway"). Local UI state, not persisted.
    var forceEnroll by remember { mutableStateOf(false) }

    if (forceEnroll || (!hasGateway && !hasConfigs)) {
        TvEnrollScreen(
            onEnrolled = { forceEnroll = false },
        )
    } else {
        TvConnectScreen(
            vpnManager = vpnManager,
            onRequestConnect = onRequestConnect,
            onRelink = { forceEnroll = true },
        )
    }
}
