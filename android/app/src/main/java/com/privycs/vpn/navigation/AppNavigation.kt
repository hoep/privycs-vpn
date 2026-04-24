package com.privycs.vpn.navigation

import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Scaffold
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.navigation.NavHostController
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument
import com.privycs.vpn.ui.components.BottomNavBar
import com.privycs.vpn.ui.screens.AddConnectionScreen
import com.privycs.vpn.ui.screens.ConnectScreen
import com.privycs.vpn.ui.screens.ConnectionsScreen
import com.privycs.vpn.ui.screens.LogsScreen
import com.privycs.vpn.ui.screens.SettingsScreen
import com.privycs.vpn.ui.screens.PerAppVpnScreen

object Routes {
    const val CONNECT = "connect"
    const val CONNECTIONS = "connections"
    // Accepts an optional connectionId arg. When set, AddConnectionScreen runs
    // in "add protocol to this connection" mode (analog to the desktop flow
    // where /add?connectionId=X appends a protocol to an existing group).
    const val ADD = "add?connectionId={connectionId}"
    const val SETTINGS = "settings"
    const val LOGS = "logs"
    const val PER_APP_VPN = "per_app_vpn"

    fun addForConnection(connectionId: String? = null): String =
        if (connectionId.isNullOrBlank()) "add" else "add?connectionId=$connectionId"
}

@Composable
fun AppNavigation(
    navController: NavHostController = rememberNavController()
) {
    val navBackStackEntry by navController.currentBackStackEntryAsState()
    val currentRoute = navBackStackEntry?.destination?.route

    Scaffold(
        bottomBar = {
            BottomNavBar(
                currentRoute = currentRoute ?: Routes.CONNECT,
                onNavigate = { route ->
                    if (route != currentRoute) {
                        navController.navigate(route) {
                            popUpTo(Routes.CONNECT) {
                                saveState = true
                            }
                            launchSingleTop = true
                            restoreState = true
                        }
                    }
                }
            )
        }
    ) { innerPadding ->
        NavHost(
            navController = navController,
            startDestination = Routes.CONNECT,
            modifier = Modifier.padding(innerPadding)
        ) {
            composable(Routes.CONNECT) {
                ConnectScreen(
                    onNavigateToAdd = {
                        navController.navigate(Routes.addForConnection())
                    },
                    onNavigateToConnections = {
                        navController.navigate(Routes.CONNECTIONS)
                    }
                )
            }

            composable(Routes.CONNECTIONS) {
                ConnectionsScreen(
                    onNavigateToAdd = { connectionId ->
                        navController.navigate(Routes.addForConnection(connectionId))
                    },
                    onNavigateToConnect = {
                        navController.navigate(Routes.CONNECT) {
                            popUpTo(Routes.CONNECT) { inclusive = true }
                        }
                    }
                )
            }

            composable(
                route = Routes.ADD,
                arguments = listOf(
                    navArgument("connectionId") {
                        type = NavType.StringType
                        nullable = true
                        defaultValue = null
                    }
                )
            ) { backStackEntry ->
                val connectionId = backStackEntry.arguments?.getString("connectionId")
                AddConnectionScreen(
                    connectionId = connectionId,
                    onConnectionAdded = {
                        navController.navigate(Routes.CONNECT) {
                            popUpTo(Routes.CONNECT) { inclusive = true }
                        }
                    },
                    onBack = {
                        navController.popBackStack()
                    }
                )
            }

            composable(Routes.SETTINGS) {
                SettingsScreen(
                    onNavigateToLogs = {
                        navController.navigate(Routes.LOGS)
                    },
                    onNavigateToPerAppVpn = {
                        navController.navigate(Routes.PER_APP_VPN)
                    }
                )
            }

            composable(Routes.PER_APP_VPN) {
                PerAppVpnScreen(
                    onBack = {
                        navController.popBackStack()
                    }
                )
            }

            composable(Routes.LOGS) {
                LogsScreen(
                    onBack = {
                        navController.popBackStack()
                    }
                )
            }
        }
    }
}
