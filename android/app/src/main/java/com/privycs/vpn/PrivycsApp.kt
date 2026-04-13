package com.privycs.vpn

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.os.Build
import com.privycs.vpn.data.ConnectionRepository
import com.privycs.vpn.data.SettingsRepository

class PrivycsApp : Application() {

    lateinit var connectionRepository: ConnectionRepository
        private set

    lateinit var settingsRepository: SettingsRepository
        private set

    override fun onCreate() {
        super.onCreate()
        instance = this
        connectionRepository = ConnectionRepository(this)
        settingsRepository = SettingsRepository(this)
        createNotificationChannels()
    }

    private fun createNotificationChannels() {
        val channel = NotificationChannel(
            NOTIFICATION_CHANNEL_VPN,
            getString(R.string.vpn_notification_channel),
            NotificationManager.IMPORTANCE_LOW
        ).apply {
            description = "Shows VPN connection status"
            setShowBadge(false)
        }

        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(channel)
    }

    companion object {
        const val NOTIFICATION_CHANNEL_VPN = "vpn_status"
        const val NOTIFICATION_ID_VPN = 1

        lateinit var instance: PrivycsApp
            private set
    }
}
