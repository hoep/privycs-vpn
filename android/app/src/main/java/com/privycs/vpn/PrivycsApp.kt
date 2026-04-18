package com.privycs.vpn

import android.app.NotificationChannel
import android.app.NotificationManager
import com.privycs.vpn.data.ConnectionRepository
import com.privycs.vpn.data.SettingsRepository
import org.strongswan.android.logic.StrongSwanApplication

// Extends StrongSwanApplication so super.onCreate() and the class's static
// initializer run when our app boots:
//   - static block:  System.loadLibrary("androidbridge")
//                    Security.addProvider(LocalCertificateKeyStoreProvider)
//   - onCreate():    mContext + mInstance setup, DatabaseHelper init,
//                    ManagedConfigurationService, TrustedCertificateManager,
//                    UserCertificateManager, broadcast receiver registration.
// Required so CharonVpnService can use StrongSwanApplication.getContext()
// and the native JNI lookups succeed on first tunnel start.
class PrivycsApp : StrongSwanApplication() {

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
