package com.privycs.vpn.tile

import android.graphics.drawable.Icon
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import android.util.Log
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
import com.privycs.vpn.service.VpnServiceManager
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch

/**
 * Quick Settings tile for controlling VPN from the notification shade.
 * Shows current VPN status and toggles connect/disconnect on tap.
 */
class VpnQuickSettingsTile : TileService() {

    companion object {
        private const val TAG = "VpnQuickSettingsTile"
    }

    private var scope: CoroutineScope? = null

    override fun onStartListening() {
        super.onStartListening()
        Log.d(TAG, "Tile started listening")

        scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)

        scope?.launch {
            try {
                val manager = VpnServiceManager.getInstance(applicationContext)
                manager.status.collectLatest { status ->
                    updateTile(status.connected, status.connectionName)
                }
            } catch (e: Exception) {
                Log.e(TAG, "Error observing VPN status", e)
                updateTile(connected = false, connectionName = "")
            }
        }
    }

    override fun onStopListening() {
        scope?.cancel()
        scope = null
        super.onStopListening()
        Log.d(TAG, "Tile stopped listening")
    }

    override fun onClick() {
        super.onClick()
        Log.d(TAG, "Tile clicked")

        val manager = VpnServiceManager.getInstance(applicationContext)

        if (manager.isConnected) {
            // Same Always-On handling as the widget: if Always-On is
            // active, a raw disconnect bounces back via OS respawn, so
            // apply a pause-flag first. Then route through the
            // coordinator with TILE source.
            if (com.privycs.vpn.util.AlwaysOnDetector.detected.value) {
                com.privycs.vpn.util.AlwaysOnDetector.pauseFor(applicationContext, 15)
            }
            scope?.launch {
                com.privycs.vpn.util.ConnectCoordinator.requestDisconnect(
                    applicationContext,
                    com.privycs.vpn.util.ConnectCoordinator.IntentSource.TILE,
                )
            }
        } else {
            // Check VPN permission
            val prepareIntent = manager.prepareVpn()
            if (prepareIntent != null) {
                // VPN permission not granted; need to open app to request it
                Log.w(TAG, "VPN permission not granted, cannot connect from tile")
                return
            }
            // Pool-active wins. Same dead-end fix as everywhere
            // else - pool users have getActive() == null on the
            // single-connection repo because the active pool card
            // takes ownership of activeId.
            val poolReg = com.privycs.vpn.PrivycsApp.instance
                .poolRepository.registry.value
            val activePoolId = poolReg.activeId
            val activePool = poolReg.pools.firstOrNull { it.id == activePoolId }
            if (activePoolId.isNotEmpty() && activePool != null) {
                scope?.launch {
                    com.privycs.vpn.util.ConnectCoordinator.requestPoolConnect(
                        applicationContext,
                        com.privycs.vpn.util.ConnectCoordinator.IntentSource.TILE,
                        activePoolId,
                        activePool.name,
                    )
                }
                return
            }
            val connection = com.privycs.vpn.PrivycsApp.instance.connectionRepository.getActive()
            if (connection == null) {
                Log.w(TAG, "Tile tap: no active connection or pool")
                return
            }
            scope?.launch {
                com.privycs.vpn.util.ConnectCoordinator.requestConnect(
                    applicationContext,
                    com.privycs.vpn.util.ConnectCoordinator.IntentSource.TILE,
                    connection,
                )
            }
        }
    }

    private fun updateTile(connected: Boolean, connectionName: String) {
        val tile = qsTile ?: return

        if (connected) {
            tile.state = Tile.STATE_ACTIVE
            tile.label = if (connectionName.isNotBlank()) connectionName else getString(R.string.app_name)
            if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.Q) {
                tile.subtitle = getString(R.string.widget_status_connected)
            }
            tile.icon = Icon.createWithResource(this, R.drawable.ic_privycs_shield)
        } else {
            tile.state = Tile.STATE_INACTIVE
            tile.label = getString(R.string.app_name)
            if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.Q) {
                tile.subtitle = getString(R.string.widget_status_disconnected)
            }
            tile.icon = Icon.createWithResource(this, R.drawable.ic_privycs_shield)
        }

        tile.updateTile()
    }
}
