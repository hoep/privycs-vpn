package com.privycs.vpn.ui.theme

import android.app.Activity
import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
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
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp
import androidx.core.view.WindowCompat
import com.privycs.vpn.R

// Privycs brand colors — design system teal family
val PrivycsTeal = Color(0xFF00CDAB)        // --teal (brand / dark accent)
val PrivycsTealDark = Color(0xFF00A88C)
val PrivycsTealLight = Color(0xFF33D7BC)
val PrivycsTealBright = Color(0xFF16E0BE)  // --teal-2 (glow / gradients)
val PrivycsTealDeep = Color(0xFF0F766E)    // --teal-deep
// Darkened teal for accents on LIGHT surfaces — #00CDAB fails contrast
// on white, so the design system uses #0a8f78 for teal text/icons in
// light mode (the dark scheme keeps the bright #00CDAB).
val PrivycsTealInk = Color(0xFF0A8F78)

// Background colors — design system "command-console" dark ramp
// (--ink / --surface / --surface-3), teal-tinted near-black.
val DarkBackground = Color(0xFF070B0E)        // --ink
val DarkSurface = Color(0xFF0E161C)           // --surface
val DarkSurfaceVariant = Color(0xFF17242E)    // --surface-3

// Light ramp — faint teal-tinted page + clean surfaces (--ink/--surface).
val LightBackground = Color(0xFFEDF3F2)       // --ink (light)
val LightSurface = Color(0xFFFFFFFF)          // --surface (light)
val LightSurfaceVariant = Color(0xFFF2F7F6)   // --surface-2 (light)

// Status colors
val StatusConnected = Color(0xFF00CDAB)
val StatusDisconnected = Color(0xFF6B7280)
val StatusError = Color(0xFFEF4444)
val StatusWarning = Color(0xFFF59E0B)

// Protocol badge colors
val WireGuardRed = Color(0xFF88171A)
val OpenVpnOrange = Color(0xFFEA7E20)
val IpSecBlue = Color(0xFF2563EB)
// AmneziaWG indigo — coordinates with the multi-colour brand mark
// (indigo is the dominant cool accent in the SVG). Stays visually
// distinct from the red of vanilla WireGuard so they read as
// different protocols even at thumbnail size.
val AmneziaWgIndigo = Color(0xFF6366F1)

// ── Design-system typography: Inter (sans) + Fira Code (mono) ──
// Bundled static weights under res/font/. Inter is the global UI
// typeface; FiraCodeFamily is exposed for technical labels / stats /
// chips / monospaced numerals (use Modifier `fontFamily = FiraCodeFamily`).
val InterFamily = FontFamily(
    Font(R.font.inter_regular, FontWeight.Normal),
    Font(R.font.inter_medium, FontWeight.Medium),
    Font(R.font.inter_semibold, FontWeight.SemiBold),
    Font(R.font.inter_bold, FontWeight.Bold),
)
val FiraCodeFamily = FontFamily(
    Font(R.font.fira_code_regular, FontWeight.Normal),
    Font(R.font.fira_code_medium, FontWeight.Medium),
    Font(R.font.fira_code_semibold, FontWeight.SemiBold),
)

// Full Material 3 type scale rebound to Inter (M3 dropped the
// defaultFontFamily shortcut, so each style is copied explicitly).
private val Base = Typography()
val PrivycsTypography = Typography(
    displayLarge = Base.displayLarge.copy(fontFamily = InterFamily),
    displayMedium = Base.displayMedium.copy(fontFamily = InterFamily),
    displaySmall = Base.displaySmall.copy(fontFamily = InterFamily),
    headlineLarge = Base.headlineLarge.copy(fontFamily = InterFamily),
    headlineMedium = Base.headlineMedium.copy(fontFamily = InterFamily),
    headlineSmall = Base.headlineSmall.copy(fontFamily = InterFamily),
    titleLarge = Base.titleLarge.copy(fontFamily = InterFamily),
    titleMedium = Base.titleMedium.copy(fontFamily = InterFamily),
    titleSmall = Base.titleSmall.copy(fontFamily = InterFamily),
    bodyLarge = Base.bodyLarge.copy(fontFamily = InterFamily),
    bodyMedium = Base.bodyMedium.copy(fontFamily = InterFamily),
    bodySmall = Base.bodySmall.copy(fontFamily = InterFamily),
    labelLarge = Base.labelLarge.copy(fontFamily = InterFamily),
    labelMedium = Base.labelMedium.copy(fontFamily = InterFamily),
    labelSmall = Base.labelSmall.copy(fontFamily = InterFamily),
)

private val DarkColorScheme = darkColorScheme(
    primary = PrivycsTeal,
    onPrimary = Color.White,
    primaryContainer = PrivycsTealDark,
    onPrimaryContainer = Color.White,
    secondary = Color(0xFF5C7280),          // --fg-3
    onSecondary = Color.White,
    secondaryContainer = Color(0xFF17242E),  // --surface-3
    onSecondaryContainer = Color(0xFF9DB2BD), // --fg-2
    tertiary = AmneziaWgIndigo,
    onTertiary = Color.White,
    background = DarkBackground,             // --ink
    onBackground = Color(0xFFEAF1F3),        // --fg
    surface = DarkSurface,                   // --surface
    onSurface = Color(0xFFEAF1F3),           // --fg
    surfaceVariant = DarkSurfaceVariant,     // --surface-3
    onSurfaceVariant = Color(0xFF9DB2BD),    // --fg-2
    error = StatusError,
    onError = Color.White,
    // Hairline borders — translucent white (--line 8% / --line-2 14%)
    // for the "techy" console look instead of solid gray.
    outline = Color(0x14FFFFFF),             // white @ 8%
    outlineVariant = Color(0x24FFFFFF),      // white @ 14%
)

private val LightColorScheme = lightColorScheme(
    // Teal accent darkened for contrast on light surfaces (#0a8f78).
    primary = PrivycsTealInk,
    onPrimary = Color.White,
    primaryContainer = PrivycsTealLight,
    onPrimaryContainer = Color(0xFF002019),
    secondary = Color(0xFF71848A),           // --fg-3 (light)
    onSecondary = Color.White,
    secondaryContainer = Color(0xFFE7EEED),  // --surface-3 (light)
    onSecondaryContainer = Color(0xFF44585E), // --fg-2 (light)
    tertiary = AmneziaWgIndigo,
    onTertiary = Color.White,
    background = LightBackground,            // --ink (light)
    onBackground = Color(0xFF08191C),        // --fg (light)
    surface = LightSurface,                  // --surface (light)
    onSurface = Color(0xFF08191C),           // --fg (light)
    surfaceVariant = LightSurfaceVariant,    // --surface-2 (light)
    onSurfaceVariant = Color(0xFF44585E),    // --fg-2 (light)
    error = StatusError,
    onError = Color.White,
    outline = Color(0x14000000),             // black @ 8% hairline
    outlineVariant = Color(0x0F000000),      // black @ 6%
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
        typography = PrivycsTypography,
        content = content
    )
}
