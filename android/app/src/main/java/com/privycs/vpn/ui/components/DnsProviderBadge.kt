package com.privycs.vpn.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp

/**
 * Brand-colored badge for a DNS provider id. Mirrors desktop's
 * DnsProviderBadge.vue — five brand identities (Cloudflare,
 * Google, Quad9, AdGuard, Mullvad) covering all 10+ provider
 * variants. Variant-providers (cloudflare-malware,
 * adguard-family, mullvad-adblock, etc.) inherit the parent
 * brand's color and letter; the dropdown's text label
 * disambiguates which specific variant is being shown.
 *
 * Pragmatic approach over real brand SVGs: avoids the trademark-
 * use complications of shipping actual provider logos in our
 * binary, while still giving each provider the instant-recognition
 * cue of its brand color.
 */
@Composable
fun DnsProviderBadge(id: String, sizeDp: Int = 24) {
    val (bg, letter, fg) = providerVisual(id)
    Box(
        modifier = Modifier
            .size(sizeDp.dp)
            .background(bg, RoundedCornerShape(4.dp)),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            text = letter,
            color = fg,
            fontWeight = FontWeight.Bold,
            style = MaterialTheme.typography.labelMedium,
        )
    }
}

private data class BadgeVisual(
    val bg: Color,
    val letter: String,
    val fg: Color,
) {
    operator fun component1() = bg
    operator fun component2() = letter
    operator fun component3() = fg
}

private fun providerVisual(id: String): BadgeVisual = when {
    id.startsWith("cloudflare") -> BadgeVisual(Color(0xFFF38020), "C", Color.White)
    id == "google" -> BadgeVisual(Color(0xFF4285F4), "G", Color.White)
    id == "quad9" -> BadgeVisual(Color(0xFF005AAB), "9", Color.White)
    id.startsWith("adguard") -> BadgeVisual(Color(0xFF67B279), "A", Color.White)
    id.startsWith("mullvad") -> BadgeVisual(Color(0xFFFFD23F), "M", Color.Black)
    else -> BadgeVisual(Color.Gray, "?", Color.White)
}
