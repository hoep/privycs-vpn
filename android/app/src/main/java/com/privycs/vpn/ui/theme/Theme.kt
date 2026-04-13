package com.privycs.vpn.ui.theme

import android.app.Activity
import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.SideEffect
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp
import androidx.core.view.WindowCompat

// Privycs brand colors
val PrivycsTeal = Color(0xFF00CDAB)
val PrivycsTealDark = Color(0xFF00A88C)
val PrivycsTealLight = Color(0xFF33D7BC)

// Background colors
val DarkBackground = Color(0xFF0F1419)
val DarkSurface = Color(0xFF1A1F2E)
val DarkSurfaceVariant = Color(0xFF242938)

val LightBackground = Color(0xFFF8FAFB)
val LightSurface = Color(0xFFFFFFFF)
val LightSurfaceVariant = Color(0xFFF0F2F5)

// Status colors
val StatusConnected = Color(0xFF00CDAB)
val StatusDisconnected = Color(0xFF6B7280)
val StatusError = Color(0xFFEF4444)
val StatusWarning = Color(0xFFF59E0B)

// Protocol badge colors
val WireGuardRed = Color(0xFF88171A)
val OpenVpnOrange = Color(0xFFEA7E20)
val IpSecBlue = Color(0xFF2563EB)

private val DarkColorScheme = darkColorScheme(
    primary = PrivycsTeal,
    onPrimary = Color.White,
    primaryContainer = PrivycsTealDark,
    onPrimaryContainer = Color.White,
    secondary = Color(0xFF4B5563),
    onSecondary = Color.White,
    secondaryContainer = Color(0xFF374151),
    onSecondaryContainer = Color(0xFFD1D5DB),
    tertiary = Color(0xFF6366F1),
    onTertiary = Color.White,
    background = DarkBackground,
    onBackground = Color(0xFFE5E7EB),
    surface = DarkSurface,
    onSurface = Color(0xFFE5E7EB),
    surfaceVariant = DarkSurfaceVariant,
    onSurfaceVariant = Color(0xFF9CA3AF),
    error = StatusError,
    onError = Color.White,
    outline = Color(0xFF374151),
    outlineVariant = Color(0xFF1F2937),
)

private val LightColorScheme = lightColorScheme(
    primary = PrivycsTeal,
    onPrimary = Color.White,
    primaryContainer = PrivycsTealLight,
    onPrimaryContainer = Color(0xFF002019),
    secondary = Color(0xFF6B7280),
    onSecondary = Color.White,
    secondaryContainer = Color(0xFFE5E7EB),
    onSecondaryContainer = Color(0xFF374151),
    tertiary = Color(0xFF6366F1),
    onTertiary = Color.White,
    background = LightBackground,
    onBackground = Color(0xFF111827),
    surface = LightSurface,
    onSurface = Color(0xFF111827),
    surfaceVariant = LightSurfaceVariant,
    onSurfaceVariant = Color(0xFF6B7280),
    error = StatusError,
    onError = Color.White,
    outline = Color(0xFFD1D5DB),
    outlineVariant = Color(0xFFE5E7EB),
)

@Composable
fun PrivycsVpnTheme(
    darkTheme: Boolean? = null,
    content: @Composable () -> Unit
) {
    val useDarkTheme = darkTheme ?: isSystemInDarkTheme()
    val colorScheme = if (useDarkTheme) DarkColorScheme else LightColorScheme

    val view = LocalView.current
    if (!view.isInEditMode) {
        SideEffect {
            val window = (view.context as Activity).window
            window.statusBarColor = Color.Transparent.toArgb()
            window.navigationBarColor = Color.Transparent.toArgb()
            WindowCompat.getInsetsController(window, view).apply {
                isAppearanceLightStatusBars = !useDarkTheme
                isAppearanceLightNavigationBars = !useDarkTheme
            }
        }
    }

    MaterialTheme(
        colorScheme = colorScheme,
        content = content
    )
}
