package com.privycs.vpn

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import com.privycs.vpn.data.models.AppTheme
import com.privycs.vpn.navigation.AppNavigation
import com.privycs.vpn.ui.theme.PrivycsVpnTheme

class MainActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()

        val settingsRepository = PrivycsApp.instance.settingsRepository

        setContent {
            val settings by settingsRepository.settingsFlow.collectAsState(
                initial = settingsRepository.defaultSettings()
            )

            val darkTheme = when (settings.theme) {
                AppTheme.DARK -> true
                AppTheme.LIGHT -> false
                AppTheme.SYSTEM -> null // let system decide
            }

            PrivycsVpnTheme(darkTheme = darkTheme) {
                Surface(
                    modifier = Modifier.fillMaxSize(),
                    color = MaterialTheme.colorScheme.background
                ) {
                    AppNavigation()
                }
            }
        }
    }
}
