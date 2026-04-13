package com.privycs.vpn.navigation

import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Scaffold
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.navigation.NavHostController
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import com.privycs.vpn.ui.components.BottomNavBar
import com.privycs.vpn.ui.screens.AddConnectionScreen
import com.privycs.vpn.ui.screens.ConnectScreen
import com.privycs.vpn.ui.screens.ConnectionsScreen
import com.privycs.vpn.ui.screens.LogsScreen
import com.privycs.vpn.ui.screens.SettingsScreen

object Routes {
    const val CONNECT = "connect"
    const val CONNECTIONS = "connections"
    const val ADD = "add"
    const val SETTINGS = "settings"
    const val LOGS = "logs"
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
                        navController.navigate(Routes.ADD)
                    },
                    onNavigateToConnections = {
                        navController.navigate(Routes.CONNECTIONS)
                    }
                )
            }

            composable(Routes.CONNECTIONS) {
                ConnectionsScreen(
                    onNavigateToAdd = {
                        navController.navigate(Routes.ADD)
                    },
                    onNavigateToConnect = {
                        navController.navigate(Routes.CONNECT) {
                            popUpTo(Routes.CONNECT) { inclusive = true }
                        }
                    }
                )
            }

            composable(Routes.ADD) {
                AddConnectionScreen(
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
