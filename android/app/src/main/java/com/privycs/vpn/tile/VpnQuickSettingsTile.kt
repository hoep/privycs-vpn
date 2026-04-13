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
            manager.disconnect()
        } else {
            // Check VPN permission
            val prepareIntent = manager.prepareVpn()
            if (prepareIntent != null) {
                // VPN permission not granted; need to open app to request it
                Log.w(TAG, "VPN permission not granted, cannot connect from tile")
                return
            }
            manager.connect()
        }
    }

    private fun updateTile(connected: Boolean, connectionName: String) {
        val tile = qsTile ?: return

        if (connected) {
            tile.state = Tile.STATE_ACTIVE
            tile.label = if (connectionName.isNotBlank()) connectionName else "Privycs VPN"
            if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.Q) {
                tile.subtitle = "Connected"
            }
            tile.icon = Icon.createWithResource(this, android.R.drawable.ic_lock_lock)
        } else {
            tile.state = Tile.STATE_INACTIVE
            tile.label = "Privycs VPN"
            if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.Q) {
                tile.subtitle = "Disconnected"
            }
            tile.icon = Icon.createWithResource(this, android.R.drawable.ic_lock_idle_lock)
        }

        tile.updateTile()
    }
}
