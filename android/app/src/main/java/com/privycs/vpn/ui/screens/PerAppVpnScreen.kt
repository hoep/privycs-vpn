package com.privycs.vpn.ui.screens

import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.graphics.drawable.Drawable
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.graphics.drawable.toBitmap
import com.privycs.vpn.PrivycsApp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Split tunnel mode: include only selected apps or exclude selected apps from VPN.
 */
enum class PerAppVpnMode {
    EXCLUDE,
    INCLUDE
}

data class AppEntry(
    val packageName: String,
    val label: String,
    val icon: Drawable?,
    var selected: Boolean = false
)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PerAppVpnScreen(
    onBack: () -> Unit
) {
    val context = LocalContext.current
    val settingsPrefs = remember {
        context.getSharedPreferences("split_tunnel", android.content.Context.MODE_PRIVATE)
    }

    var mode by remember {
        mutableStateOf(
            if (settingsPrefs.getString("mode", "exclude") == "include")
                PerAppVpnMode.INCLUDE
            else
                PerAppVpnMode.EXCLUDE
        )
    }

    val savedPackages = remember {
        settingsPrefs.getStringSet("packages", emptySet()) ?: emptySet()
    }

    val apps = remember { mutableStateListOf<AppEntry>() }
    var loading by remember { mutableStateOf(true) }

    // Load installed apps
    LaunchedEffect(Unit) {
        withContext(Dispatchers.IO) {
            val pm = context.packageManager
            val installedApps = pm.getInstalledApplications(PackageManager.GET_META_DATA)
                .filter { app ->
                    // Show user-installed apps and common system apps with launcher intent
                    (app.flags and ApplicationInfo.FLAG_SYSTEM == 0) ||
                        pm.getLaunchIntentForPackage(app.packageName) != null
                }
                .filter { it.packageName != context.packageName } // Exclude self
                .map { app ->
                    AppEntry(
                        packageName = app.packageName,
                        label = pm.getApplicationLabel(app).toString(),
                        icon = try { pm.getApplicationIcon(app) } catch (e: Exception) { null },
                        selected = savedPackages.contains(app.packageName)
                    )
                }
                .sortedWith(compareByDescending<AppEntry> { it.selected }.thenBy { it.label.lowercase() })

            withContext(Dispatchers.Main) {
                apps.clear()
                apps.addAll(installedApps)
                loading = false
            }
        }
    }

    fun saveSettings() {
        val selectedPackages = apps.filter { it.selected }.map { it.packageName }.toSet()
        settingsPrefs.edit()
            .putString("mode", if (mode == PerAppVpnMode.INCLUDE) "include" else "exclude")
            .putStringSet("packages", selectedPackages)
            .apply()
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Text(
                        "Per-App VPN",
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.SemiBold
                    )
                },
                navigationIcon = {
                    IconButton(onClick = onBack) {
                        Icon(
                            imageVector = Icons.AutoMirrored.Filled.ArrowBack,
                            contentDescription = "Back"
                        )
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.background
                )
            )
        }
    ) { padding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding)
                .padding(horizontal = 16.dp)
        ) {
            // Mode selector
            Card(
                modifier = Modifier.fillMaxWidth(),
                colors = CardDefaults.cardColors(
                    containerColor = MaterialTheme.colorScheme.surface
                ),
                shape = RoundedCornerShape(12.dp)
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Text(
                        text = "MODE",
                        style = MaterialTheme.typography.labelSmall,
                        fontWeight = FontWeight.SemiBold,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                    Spacer(modifier = Modifier.height(8.dp))

                    SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
                        SegmentedButton(
                            selected = mode == PerAppVpnMode.EXCLUDE,
                            onClick = {
                                mode = PerAppVpnMode.EXCLUDE
                                saveSettings()
                            },
                            shape = SegmentedButtonDefaults.itemShape(index = 0, count = 2)
                        ) {
                            Text("Exclude Selected", style = MaterialTheme.typography.labelSmall)
                        }
                        SegmentedButton(
                            selected = mode == PerAppVpnMode.INCLUDE,
                            onClick = {
                                mode = PerAppVpnMode.INCLUDE
                                saveSettings()
                            },
                            shape = SegmentedButtonDefaults.itemShape(index = 1, count = 2)
                        ) {
                            Text("Include Only", style = MaterialTheme.typography.labelSmall)
                        }
                    }

                    Spacer(modifier = Modifier.height(8.dp))

                    Text(
                        text = when (mode) {
                            PerAppVpnMode.EXCLUDE -> "Selected apps will bypass the VPN and use the normal network."
                            PerAppVpnMode.INCLUDE -> "Only selected apps will use the VPN. All other traffic bypasses it."
                        },
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }
            }

            Spacer(modifier = Modifier.height(12.dp))

            // App count
            val selectedCount = apps.count { it.selected }
            Text(
                text = "$selectedCount of ${apps.size} apps selected",
                style = MaterialTheme.typography.labelMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(bottom = 8.dp)
            )

            if (loading) {
                Text(
                    text = "Loading installed apps...",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(top = 24.dp)
                )
            } else {
                // App list
                LazyColumn(
                    modifier = Modifier.fillMaxSize(),
                    verticalArrangement = Arrangement.spacedBy(2.dp)
                ) {
                    items(apps, key = { it.packageName }) { appEntry ->
                        AppRow(
                            app = appEntry,
                            onToggle = { enabled ->
                                val index = apps.indexOfFirst { it.packageName == appEntry.packageName }
                                if (index >= 0) {
                                    apps[index] = apps[index].copy(selected = enabled)
                                    saveSettings()
                                }
                            }
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun AppRow(
    app: AppEntry,
    onToggle: (Boolean) -> Unit
) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 6.dp, horizontal = 4.dp),
        verticalAlignment = Alignment.CenterVertically
    ) {
        // App icon
        app.icon?.let { drawable ->
            val bitmap = remember(app.packageName) {
                drawable.toBitmap(48, 48).asImageBitmap()
            }
            Image(
                bitmap = bitmap,
                contentDescription = app.label,
                modifier = Modifier.size(36.dp)
            )
        }

        Spacer(modifier = Modifier.width(12.dp))

        // App name and package
        Column(modifier = Modifier.weight(1f)) {
            Text(
                text = app.label,
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurface,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
            Text(
                text = app.packageName,
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis
            )
        }

        Switch(
            checked = app.selected,
            onCheckedChange = onToggle,
            colors = SwitchDefaults.colors(
                checkedTrackColor = MaterialTheme.colorScheme.primary,
                checkedThumbColor = MaterialTheme.colorScheme.onPrimary
            )
        )
    }
}
