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
import com.privycs.vpn.ui.screens.AddPoolHost
import com.privycs.vpn.ui.screens.ConnectScreen
import com.privycs.vpn.ui.screens.ConnectionsScreen
import com.privycs.vpn.ui.screens.LogsScreen
import com.privycs.vpn.ui.screens.PoolDetailHost
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
    const val NETWORK_RULES = "network_rules"
    const val CONNECT_ON_DEMAND = "connect_on_demand"
    const val LICENSES = "licenses"
    const val HELP = "help"

    // Pool routes. POOL_ADD reuses the "Add" tab via a flag so the
    // BottomNavBar's Add button surfaces a chooser between Single
    // Connection and Pool. POOL_DETAIL is a deep link from the
    // ConnectionsScreen pool list.
    const val POOL_ADD = "pool/add"
    const val POOL_DETAIL = "pool/{poolId}"

    fun addForConnection(connectionId: String? = null): String =
        if (connectionId.isNullOrBlank()) "add" else "add?connectionId=$connectionId"

    fun poolDetail(poolId: String): String = "pool/$poolId"
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
                    },
                    onNavigateToPoolAdd = {
                        navController.navigate(Routes.POOL_ADD)
                    },
                    onNavigateToPoolDetail = { poolId ->
                        navController.navigate(Routes.poolDetail(poolId))
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
                    },
                    onNavigateToNetworkRules = {
                        navController.navigate(Routes.NETWORK_RULES)
                    },
                    onNavigateToLicenses = {
                        navController.navigate(Routes.LICENSES)
                    },
                )
            }

            composable(Routes.LICENSES) {
                com.privycs.vpn.ui.screens.OssLicensesScreen(
                    onBack = { navController.popBackStack() },
                )
            }

            composable(Routes.PER_APP_VPN) {
                PerAppVpnScreen(
                    onBack = {
                        navController.popBackStack()
                    }
                )
            }

            composable(Routes.NETWORK_RULES) {
                com.privycs.vpn.ui.screens.NetworkRulesScreen(
                    onBack = { navController.popBackStack() },
                    onEditDefault = {
                        navController.navigate(Routes.CONNECT_ON_DEMAND)
                    },
                )
            }

            composable(Routes.CONNECT_ON_DEMAND) {
                com.privycs.vpn.ui.screens.ConnectOnDemandScreen(
                    onBack = { navController.popBackStack() },
                )
            }

            composable(Routes.LOGS) {
                LogsScreen(
                    onBack = {
                        navController.popBackStack()
                    }
                )
            }

            composable(Routes.HELP) {
                com.privycs.vpn.ui.screens.HelpScreen()
            }

            // Pool: import + create flow.
            composable(Routes.POOL_ADD) {
                AddPoolHost(
                    onCancel = { navController.popBackStack() },
                    onCreated = {
                        navController.navigate(Routes.CONNECTIONS) {
                            popUpTo(Routes.CONNECT) { inclusive = false }
                        }
                    }
                )
            }

            // Pool: detail screen with member list, settings, activate.
            composable(
                route = Routes.POOL_DETAIL,
                arguments = listOf(
                    navArgument("poolId") {
                        type = NavType.StringType
                        nullable = false
                    }
                )
            ) { backStackEntry ->
                val poolId = backStackEntry.arguments?.getString("poolId").orEmpty()
                PoolDetailHost(
                    poolId = poolId,
                    onBack = { navController.popBackStack() },
                    onActivated = {
                        navController.navigate(Routes.CONNECT) {
                            popUpTo(Routes.CONNECT) { inclusive = true }
                        }
                    },
                    onDeleted = {
                        navController.navigate(Routes.CONNECTIONS) {
                            popUpTo(Routes.CONNECTIONS) { inclusive = true }
                        }
                    }
                )
            }
        }
    }
}
