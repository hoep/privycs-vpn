package com.privycs.vpn.ui.components

import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.HelpOutline
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material.icons.outlined.Add
import androidx.compose.material.icons.outlined.Dns
import androidx.compose.material.icons.outlined.HelpOutline
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.material.icons.outlined.Shield
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationBarItemDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.annotation.StringRes
import com.privycs.vpn.R
import com.privycs.vpn.navigation.Routes

data class NavItem(
    val route: String,
    @StringRes val labelRes: Int,
    val selectedIcon: ImageVector,
    val unselectedIcon: ImageVector
)

private val navItems = listOf(
    NavItem(Routes.CONNECT, R.string.nav_label_connect, Icons.Filled.Shield, Icons.Outlined.Shield),
    NavItem(Routes.CONNECTIONS, R.string.nav_label_configs, Icons.Filled.Dns, Icons.Outlined.Dns),
    NavItem(Routes.ADD, R.string.nav_label_add, Icons.Filled.Add, Icons.Outlined.Add),
    NavItem(Routes.SETTINGS, R.string.nav_label_settings, Icons.Filled.Settings, Icons.Outlined.Settings),
    NavItem(Routes.HELP, R.string.nav_label_help, Icons.Filled.HelpOutline, Icons.Outlined.HelpOutline),
)

@Composable
fun BottomNavBar(
    currentRoute: String,
    onNavigate: (String) -> Unit
) {
    NavigationBar(
        containerColor = MaterialTheme.colorScheme.surface,
        contentColor = MaterialTheme.colorScheme.onSurface
    ) {
        navItems.forEach { item ->
            val selected = currentRoute == item.route
            val itemLabel = stringResource(item.labelRes)
            NavigationBarItem(
                selected = selected,
                onClick = { onNavigate(item.route) },
                icon = {
                    Icon(
                        imageVector = if (selected) item.selectedIcon else item.unselectedIcon,
                        contentDescription = itemLabel
                    )
                },
                label = {
                    Text(
                        text = itemLabel,
                        style = MaterialTheme.typography.labelSmall,
                        // Single line — long localised labels (de/es/fr)
                        // must not wrap the nav bar onto two rows.
                        maxLines = 1
                    )
                },
                colors = NavigationBarItemDefaults.colors(
                    selectedIconColor = MaterialTheme.colorScheme.primary,
                    selectedTextColor = MaterialTheme.colorScheme.primary,
                    unselectedIconColor = MaterialTheme.colorScheme.onSurfaceVariant,
                    unselectedTextColor = MaterialTheme.colorScheme.onSurfaceVariant,
                    indicatorColor = MaterialTheme.colorScheme.primary.copy(alpha = 0.12f)
                )
            )
        }
    }
}
