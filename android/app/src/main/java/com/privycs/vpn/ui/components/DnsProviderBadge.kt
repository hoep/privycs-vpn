package com.privycs.vpn.ui.components

import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.unit.dp
import com.privycs.vpn.R

/**
 * Brand-logo badge for a DNS provider id. Renders the simplified
 * brand SVG vector drawable for each provider. Mirrors desktop's
 * DnsProviderBadge.vue inline-SVG approach.
 *
 * Provider variants (cloudflare-malware, adguard-family,
 * mullvad-adblock, etc.) inherit the parent brand's icon — the
 * dropdown's text label disambiguates the specific filter level.
 *
 * Trademark note: nominative-fair-use to identify the third-party
 * service the user is configuring. Same pattern as NextDNS,
 * ControlD, Pi-hole admin GUI.
 */
@Composable
fun DnsProviderBadge(id: String, sizeDp: Int = 24) {
    val resId = drawableForProvider(id)
    Image(
        painter = painterResource(id = resId),
        contentDescription = id,
        modifier = Modifier.size(sizeDp.dp),
    )
}

private fun drawableForProvider(id: String): Int = when {
    id.startsWith("cloudflare") -> R.drawable.ic_dns_cloudflare
    id == "google" -> R.drawable.ic_dns_google
    id == "quad9" -> R.drawable.ic_dns_quad9
    id.startsWith("adguard") -> R.drawable.ic_dns_adguard
    id.startsWith("mullvad") -> R.drawable.ic_dns_mullvad
    // Fallback: reuse the privycs shield icon for unknown providers
    // (existed before; cleaner than shipping a placeholder square).
    else -> R.drawable.ic_privycs_shield
}
