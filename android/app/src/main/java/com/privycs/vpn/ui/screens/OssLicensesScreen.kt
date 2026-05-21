package com.privycs.vpn.ui.screens

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * Open-Source Licenses screen (v0.9.15.74 — B-6 GPL/OSS compliance).
 *
 * Privycs VPN bundles GPL-2.0 code (ics-openvpn, strongSwan), so the
 * whole client is GPL-3.0. The GPL requires that the license text
 * accompany the binary and that recipients can obtain the
 * corresponding source. This screen conveys both: the bundled
 * components with their licenses, the full GPL/Apache/MIT texts, and
 * the public source-code URL (the GPL "corresponding source" offer).
 */
private const val SOURCE_URL = "https://github.com/hoep/privycs-vpn"

private data class OssComponent(
    val name: String,
    val purpose: String,
    val license: String,
)

private val bundledComponents = listOf(
    OssComponent(
        "OpenVPN for Android (ics-openvpn)",
        "OpenVPN protocol support",
        "GNU GPL v2",
    ),
    OssComponent(
        "strongSwan",
        "IPSec / IKEv2 protocol support",
        "GNU GPL v2",
    ),
    OssComponent(
        "AmneziaWG (amneziawg-android)",
        "WireGuard & AmneziaWG protocol support; embeds WireGuard-go (MIT)",
        "Apache License 2.0",
    ),
    OssComponent(
        "Jetpack Compose & AndroidX",
        "UI toolkit and platform libraries",
        "Apache License 2.0",
    ),
    OssComponent(
        "Kotlin, Coroutines & Serialization",
        "Language and core libraries",
        "Apache License 2.0",
    ),
    OssComponent(
        "Ktor",
        "HTTP client for the gateway API",
        "Apache License 2.0",
    ),
    OssComponent(
        "Markwon",
        "In-app Markdown rendering (Help screen)",
        "Apache License 2.0",
    ),
    OssComponent(
        "MaxMind DB Reader",
        "Reads the bundled offline country database",
        "Apache License 2.0",
    ),
    OssComponent(
        "DB-IP IP-to-Country Lite",
        "Bundled offline country database",
        "CC BY 4.0",
    ),
    OssComponent(
        "Google Play Services (Code Scanner)",
        "QR-code scanning for one-tap setup",
        "Google APIs Terms of Service",
    ),
)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun OssLicensesScreen(onBack: () -> Unit) {
    val context = LocalContext.current
    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text("Open-Source Licenses", fontWeight = FontWeight.SemiBold)
                },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .verticalScroll(rememberScrollState())
                .padding(start = 16.dp, top = 12.dp, end = 16.dp, bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            // The app's own license + the GPL corresponding-source offer.
            SettingsSection(title = "THIS APP") {
                Text(
                    "Privycs VPN is free software, licensed under the GNU " +
                        "General Public License, version 3.",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurface,
                )
                Spacer(Modifier.height(8.dp))
                Text(
                    "In accordance with the GPL, the complete corresponding " +
                        "source code is publicly available at:",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Spacer(Modifier.height(4.dp))
                Text(
                    SOURCE_URL,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.primary,
                    textDecoration = TextDecoration.Underline,
                    modifier = Modifier.clickable {
                        runCatching {
                            context.startActivity(
                                Intent(Intent.ACTION_VIEW, Uri.parse(SOURCE_URL))
                                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                            )
                        }
                    },
                )
            }

            // Bundled third-party components and their licenses.
            SettingsSection(title = "BUNDLED COMPONENTS") {
                bundledComponents.forEachIndexed { index, component ->
                    if (index > 0) Spacer(Modifier.height(12.dp))
                    Text(
                        component.name,
                        style = MaterialTheme.typography.bodyMedium,
                        fontWeight = FontWeight.Medium,
                        color = MaterialTheme.colorScheme.onSurface,
                    )
                    Text(
                        component.purpose,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    Text(
                        component.license,
                        style = MaterialTheme.typography.labelMedium,
                        color = MaterialTheme.colorScheme.primary,
                    )
                }
            }

            // Full license texts — tap to expand.
            SettingsSection(title = "LICENSE TEXTS") {
                LicenseText("GNU General Public License v3", asset = "licenses/gpl-3.0.txt")
                Spacer(Modifier.height(4.dp))
                LicenseText("GNU General Public License v2", asset = "licenses/gpl-2.0.txt")
                Spacer(Modifier.height(4.dp))
                LicenseText("Apache License 2.0", asset = "licenses/apache-2.0.txt")
                Spacer(Modifier.height(4.dp))
                LicenseText("MIT License (WireGuard-go)", asset = null, inline = MIT_TEXT)
            }
        }
    }
}

@Composable
private fun LicenseText(
    title: String,
    asset: String?,
    inline: String? = null,
) {
    var expanded by remember { mutableStateOf(false) }
    Column(modifier = Modifier.fillMaxWidth()) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .clickable { expanded = !expanded }
                .padding(vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                title,
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurface,
                modifier = Modifier.weight(1f),
            )
            Icon(
                if (expanded) Icons.Filled.KeyboardArrowUp else Icons.Filled.KeyboardArrowDown,
                contentDescription = if (expanded) "Collapse" else "Expand",
            )
        }
        if (expanded) {
            // Loaded only when expanded; asset read happens on
            // Dispatchers.IO so the compose thread is never blocked.
            val text = inline ?: rememberAssetText(asset.orEmpty())
            Text(
                text,
                style = MaterialTheme.typography.bodySmall.copy(
                    fontFamily = FontFamily.Monospace,
                ),
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(top = 4.dp, bottom = 8.dp),
            )
        }
    }
}

@Composable
private fun rememberAssetText(assetPath: String): String {
    val context = LocalContext.current
    val text by produceState("Loading…", assetPath) {
        value = withContext(Dispatchers.IO) {
            runCatching {
                context.assets.open(assetPath).bufferedReader().use { it.readText() }
            }.getOrElse { "Failed to load license text." }
        }
    }
    return text
}

private val MIT_TEXT = """
MIT License

WireGuard-go is Copyright (C) Jason A. Donenfeld and WireGuard LLC.

Permission is hereby granted, free of charge, to any person obtaining a
copy of this software and associated documentation files (the
"Software"), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be included
in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS
OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
""".trimIndent()
