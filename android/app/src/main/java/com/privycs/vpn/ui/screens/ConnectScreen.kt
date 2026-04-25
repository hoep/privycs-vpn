package com.privycs.vpn.ui.screens

import android.app.Activity
import android.content.Intent
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.animateColorAsState
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ArrowDropDown
import androidx.compose.material.icons.filled.GppGood
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material.icons.outlined.ArrowDownward
import androidx.compose.material.icons.outlined.ArrowUpward
import androidx.compose.material.icons.outlined.Shield
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.privycs.vpn.PrivycsApp
import com.privycs.vpn.R
import com.privycs.vpn.data.models.VpnConnection
import com.privycs.vpn.data.models.VpnProtocol
import com.privycs.vpn.service.VpnServiceManager
import com.privycs.vpn.ui.components.AlwaysOnDisconnectSheet
import com.privycs.vpn.ui.components.ManualPauseSheet
import com.privycs.vpn.ui.components.SpeedSparkline
import com.privycs.vpn.util.AlwaysOnDetector
import com.privycs.vpn.util.ConnectCoordinator
import com.privycs.vpn.util.SpeedTracker
import com.privycs.vpn.util.KillSwitchManager
import com.privycs.vpn.util.VpnPauseTimer
import androidx.compose.material.icons.filled.GppBad
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.combinedClickable
import androidx.compose.material3.TextButton
import kotlinx.coroutines.delay
import com.privycs.vpn.ui.theme.IpSecBlue
import com.privycs.vpn.ui.theme.OpenVpnOrange
import com.privycs.vpn.ui.theme.PrivycsTeal
import com.privycs.vpn.ui.theme.PrivycsTealDark
import com.privycs.vpn.ui.theme.StatusConnected
import com.privycs.vpn.ui.theme.WireGuardRed
import kotlinx.coroutines.launch

@Composable
fun ConnectScreen(
    onNavigateToAdd: () -> Unit,
    @Suppress("UNUSED_PARAMETER") onNavigateToConnections: () -> Unit,
) {
    val context = LocalContext.current
    val vpnManager = remember { VpnServiceManager.getInstance(context) }
    val status by vpnManager.status.collectAsState()
    val isConnecting by vpnManager.isConnecting.collectAsState()
    // Speed history buffers feed the per-card sparklines. SpeedTracker
    // itself is populated from VpnServiceManager.updateStatus() so every
    // polled status push moves both the byte counters and the sparkline
    // in lockstep.
    val rxSpeedHistory by SpeedTracker.rxSpeedHistory.collectAsState()
    val txSpeedHistory by SpeedTracker.txSpeedHistory.collectAsState()
    // If Android's system-level Always-On VPN is detected, the plain
    // Disconnect tap is effectively useless (OS auto-respawns within
    // ~1 s). We then route the tap into a bottom sheet offering Pause
    // or Open-System-Settings instead.
    val alwaysOnDetected by AlwaysOnDetector.detected.collectAsState()
    val killSwitchState by KillSwitchManager.state.collectAsState()
    val isSinkholeActive = killSwitchState == KillSwitchManager.State.SINKHOLE
    var showAlwaysOnSheet by remember { mutableStateOf(false) }
    var showManualPauseSheet by remember { mutableStateOf(false) }

    // Observe the manual-pause timer. When active, the Connect
    // button label switches to "Paused — mm:ss" and a Resume-now
    // link appears under it; [VpnPauseTimer] takes care of the
    // auto-reconnect at expiry.
    val pauseUntilMs by VpnPauseTimer.pauseUntilEpochMs.collectAsState()
    var pauseNowMs by remember { mutableStateOf(System.currentTimeMillis()) }
    val isManuallyPaused = pauseUntilMs > pauseNowMs
    LaunchedEffect(pauseUntilMs) {
        // Tick once per second while a pause is active so the
        // countdown label animates smoothly. Loop exits as soon as
        // pauseUntilMs drops to 0 (user resumed or expiry hit).
        while (pauseUntilMs > System.currentTimeMillis()) {
            pauseNowMs = System.currentTimeMillis()
            delay(1000)
        }
        pauseNowMs = System.currentTimeMillis()
    }
    val pauseRemainingSec = if (isManuallyPaused) {
        ((pauseUntilMs - pauseNowMs) / 1000L).toInt().coerceAtLeast(0)
    } else 0

    val connectionRepo = remember { PrivycsApp.instance.connectionRepository }
    val registry by connectionRepo.registry.collectAsState()
    val connections = registry.connections
    val activeConnection = connectionRepo.getActive()

    var showConnectionPicker by remember { mutableStateOf(false) }

    // Scope for the on-demand reconnect coroutine (disconnect -> wait 400ms ->
    // re-evaluate rules -> reconnect if still matching). Tied to the Connect
    // screen's composable lifetime so navigating away cancels any pending
    // reconnect rather than racing into a stale vpnManager.connect() call.
    val coroutineScope = rememberCoroutineScope()

    // IPSec KeyChain install + alias pick orchestrator. The first time the
    // user connects an IPSec profile, this drives the two-step Android
    // credential install; subsequent connects skip straight to onReady.
    // Declared BEFORE vpnPermissionLauncher because that launcher's callback
    // re-enters ipSecPrep after the permission dialog comes back.
    val ipSecPrep = rememberIpSecConnectPrep(
        onReady = { vpnManager.connect() },
        onError = { msg ->
            android.widget.Toast.makeText(context, msg, android.widget.Toast.LENGTH_LONG).show()
        }
    )

    // VPN permission launcher
    val vpnPermissionLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.StartActivityForResult()
    ) { result ->
        if (result.resultCode == Activity.RESULT_OK) {
            // After VPN permission we re-enter the flow; IPSec profiles
            // still need the KeyChain install check, WG/OpenVPN can connect
            // directly. The same branch lives in onClick below.
            val conn = connectionRepo.getActive()
            if (conn != null && conn.needsKeyChainPrep()) {
                ipSecPrep(conn)
            } else {
                vpnManager.connect()
            }
        }
    }

    LaunchedEffect(Unit) {
        vpnManager.refreshStatus()
    }

    val isConnected = status.connected

    // Welcome screen if no connections
    if (connections.isEmpty()) {
        WelcomeView(onNavigateToAdd = onNavigateToAdd)
        return
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(horizontal = 20.dp),
        horizontalAlignment = Alignment.CenterHorizontally
    ) {
        Spacer(modifier = Modifier.height(24.dp))

        // Connect button
        // Prefer the live-status protocol (reflects what the tunnel is
        // actually running) over the stored active protocol (reflects
        // what the user has picked in the dropdown). Falls back to the
        // active connection's activeProtocol when status.activeProtocol
        // is null, e.g. right after app start before VpnStatus has its
        // first update.
        val activeProtocolForIcon = status.activeProtocol
            ?: connectionRepo.getActive()?.activeProtocol
        ConnectButton(
            isConnected = isConnected,
            isConnecting = isConnecting,
            isSinkholeActive = isSinkholeActive,
            activeProtocol = activeProtocolForIcon,
            onLongClick = {
                // Long-press on the toggle while connected opens the
                // pause bottom sheet. When Always-On is active we
                // already route the short tap there (different
                // sheet), so suppress the long-press in that case.
                if (isConnected && !alwaysOnDetected) {
                    showManualPauseSheet = true
                }
            },
            onClick = {
                // Hardcore Kill Switch lock: when the sinkhole is engaged
                // the only valid release is the user toggling KS off in
                // Settings. Tapping the connect button (which renders as
                // the kill-switch shield icon in this state) does NOT
                // trigger a connect attempt - no spinner, no service
                // intent, no state change. We surface a one-line toast
                // so the user understands the lock instead of just
                // seeing a non-responsive button.
                if (isSinkholeActive) {
                    android.widget.Toast.makeText(
                        context,
                        "Kill Switch is active. Toggle Kill Switch off in Settings to release.",
                        android.widget.Toast.LENGTH_LONG,
                    ).show()
                } else {
                    val networkMonitor = com.privycs.vpn.service.NetworkMonitor.getInstance(context)
                    if (isConnected && alwaysOnDetected) {
                        // Always-On gate: plain disconnect is neutered by
                        // the OS auto-respawn when Always-On is active, so
                        // route through the pause/settings bottom sheet
                        // instead of firing a disconnect the OS will
                        // immediately undo.
                        showAlwaysOnSheet = true
                    } else if (isConnected) {
                        // User explicitly-disconnected but on-demand is in
                        // charge: if the current network still satisfies
                        // the rules, bring the tunnel back up immediately.
                        // Direct orchestration here (rather than relying
                        // on NetworkMonitor's VpnStatus.collect chain) so
                        // that users who tap Disconnect while the rule
                        // banner reads "VPN will connect" see the tunnel
                        // re-enter within roughly one second, every time.
                        vpnManager.disconnect()
                        coroutineScope.launch {
                            val settings = PrivycsApp.instance.settingsRepository.getSettingsBlocking()
                            if (!settings.connectOnDemand.enabled) return@launch
                            // Give the disconnect a moment to propagate
                            // (service stopSelf + scope.cancel + status=empty).
                            kotlinx.coroutines.delay(400)
                            networkMonitor.reevaluate()
                            val ns = networkMonitor.networkState.value
                            if (ns.shouldConnect && !vpnManager.isConnected) {
                                com.privycs.vpn.util.PrivycsLogger.i(
                                    "ConnectScreen",
                                    "On-demand reconnect after manual disconnect (${ns.ruleMatch})"
                                )
                                vpnManager.connect()
                            }
                        }
                    } else {
                        val prepareIntent = vpnManager.prepareVpn()
                        if (prepareIntent != null) {
                            vpnPermissionLauncher.launch(prepareIntent)
                        } else {
                            val conn = connectionRepo.getActive()
                            if (conn != null && conn.needsKeyChainPrep()) {
                                ipSecPrep(conn)
                            } else {
                                vpnManager.connect()
                            }
                        }
                    }
                }
            }
        )

        Spacer(modifier = Modifier.height(12.dp))

        // Manual-pause countdown + Resume link. Only shown while a
        // user-initiated pause is active. VpnPauseTimer drives the
        // auto-reconnect at expiry; the Resume link cancels the
        // timer and issues an immediate USER-source connect.
        if (isManuallyPaused) {
            val mins = pauseRemainingSec / 60
            val secs = pauseRemainingSec % 60
            Text(
                text = "Paused — ${String.format("%d:%02d", mins, secs)} remaining",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            TextButton(
                onClick = {
                    VpnPauseTimer.cancel()
                    val conn = connectionRepo.getActive()
                    if (conn != null) {
                        coroutineScope.launch {
                            ConnectCoordinator.requestConnect(
                                context,
                                ConnectCoordinator.IntentSource.USER,
                                conn,
                            )
                        }
                    }
                },
            ) {
                Text("Resume now")
            }
            Spacer(modifier = Modifier.height(8.dp))
        }

        // Uptime
        if (isConnected && status.uptime > 0) {
            Text(
                text = formatUptime(status.uptime),
                style = MaterialTheme.typography.headlineSmall,
                fontFamily = FontFamily.Monospace,
                color = MaterialTheme.colorScheme.onBackground
            )
            Spacer(modifier = Modifier.height(8.dp))
        }

        // Connection name with picker
        if (activeConnection != null) {
            Box {
                Row(
                    modifier = Modifier
                        .clickable { showConnectionPicker = !showConnectionPicker }
                        .padding(4.dp),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = status.connectionName.ifBlank { activeConnection.name },
                        style = MaterialTheme.typography.bodyMedium,
                        fontWeight = FontWeight.Medium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                    Icon(
                        imageVector = if (showConnectionPicker) Icons.Filled.KeyboardArrowUp
                        else Icons.Filled.KeyboardArrowDown,
                        contentDescription = "Switch connection",
                        modifier = Modifier.size(18.dp),
                        tint = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }

                DropdownMenu(
                    expanded = showConnectionPicker && connections.size > 1,
                    onDismissRequest = { showConnectionPicker = false }
                ) {
                    connections.forEach { conn ->
                        DropdownMenuItem(
                            text = {
                                Row(verticalAlignment = Alignment.CenterVertically) {
                                    Box(
                                        modifier = Modifier
                                            .size(6.dp)
                                            .clip(CircleShape)
                                            .background(
                                                if (conn.id == registry.activeId)
                                                    MaterialTheme.colorScheme.primary
                                                else MaterialTheme.colorScheme.outline
                                            )
                                    )
                                    Spacer(modifier = Modifier.width(8.dp))
                                    Text(
                                        text = conn.name,
                                        style = MaterialTheme.typography.bodySmall
                                    )
                                    Spacer(modifier = Modifier.weight(1f))
                                    Text(
                                        text = conn.availableProtocols().joinToString("/") { it.shortLabel },
                                        style = MaterialTheme.typography.labelSmall,
                                        color = MaterialTheme.colorScheme.onSurfaceVariant
                                    )
                                }
                            },
                            onClick = {
                                showConnectionPicker = false
                                if (conn.id != registry.activeId) {
                                    // Warn if KS is currently armed AND a tunnel
                                    // is up: switchActiveConnection will tear
                                    // the current tunnel down (forceSinkhole
                                    // engages on disconnect-with-KS-armed),
                                    // and the subsequent reconnect to the new
                                    // connection will be refused by the
                                    // hardcore-lock sinkhole gate. The user
                                    // must toggle KS off to allow the switch.
                                    if (vpnManager.isConnected &&
                                        com.privycs.vpn.util.KillSwitchManager.isArmed()
                                    ) {
                                        android.widget.Toast.makeText(
                                            context,
                                            "Kill Switch active. This will block your reconnect!",
                                            android.widget.Toast.LENGTH_LONG,
                                        ).show()
                                    }
                                    vpnManager.switchActiveConnection(conn.id)
                                }
                            }
                        )
                    }
                }
            }

            Spacer(modifier = Modifier.height(12.dp))

            // Protocol badges
            ProtocolBadges(
                availableProtocols = activeConnection.availableProtocols(),
                activeProtocol = status.activeProtocol ?: activeConnection.activeProtocol,
                onSelect = { protocol ->
                    vpnManager.switchProtocol(protocol)
                }
            )

            Spacer(modifier = Modifier.height(8.dp))

            // Server endpoint
            val endpoint = status.serverEndpoint.ifBlank {
                activeConnection.getActiveConfig()?.serverAddress ?: ""
            }
            if (endpoint.isNotBlank()) {
                Text(
                    text = endpoint,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    textAlign = TextAlign.Center
                )
                Spacer(modifier = Modifier.height(16.dp))
            }
        }

        // Transfer stats
        if (isConnected) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                TransferCard(
                    label = "Download",
                    bytes = status.rxBytes,
                    icon = Icons.Outlined.ArrowDownward,
                    iconTint = Color(0xFF22C55E),
                    speedHistory = rxSpeedHistory,
                    sparklineColor = Color(0xFF4ADE80),
                    modifier = Modifier.weight(1f)
                )
                TransferCard(
                    label = "Upload",
                    bytes = status.txBytes,
                    icon = Icons.Outlined.ArrowUpward,
                    iconTint = Color(0xFF3B82F6),
                    speedHistory = txSpeedHistory,
                    sparklineColor = Color(0xFF60A5FA),
                    modifier = Modifier.weight(1f)
                )
            }
            Spacer(modifier = Modifier.height(16.dp))
        }

        // Connection details
        if (activeConnection != null) {
            ConnectionDetails(
                localAddress = status.localAddress,
                serverAddress = status.serverEndpoint,
                lastHandshake = status.lastHandshake
            )
        }

        // Error
        if (!status.error.isNullOrBlank()) {
            Spacer(modifier = Modifier.height(12.dp))
            Text(
                text = status.error!!,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.error,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth()
            )
        }

        Spacer(modifier = Modifier.height(24.dp))
    }

    // Always-On disconnect sheet renders outside the scrollable Column
    // so it slides up over the whole screen and is not affected by the
    // Column's scroll state. Visibility is purely state-driven.
    if (showAlwaysOnSheet) {
        AlwaysOnDisconnectSheet(
            onDismiss = { showAlwaysOnSheet = false },
            onPauseSelected = { minutes ->
                AlwaysOnDetector.pauseFor(context, minutes)
                vpnManager.disconnect()
            },
        )
    }

    // Manual-pause sheet (long-press on the Connect button when
    // connected without Always-On). Three timed options + plain
    // disconnect. Driven by VpnPauseTimer which handles the
    // auto-reconnect at expiry via COD re-evaluation.
    if (showManualPauseSheet) {
        ManualPauseSheet(
            onDismiss = { showManualPauseSheet = false },
            onDisconnect = {
                vpnManager.disconnect()
            },
            onPauseSelected = { minutes ->
                VpnPauseTimer.pauseFor(context, minutes)
            },
        )
    }
}

@Composable
private fun WelcomeView(onNavigateToAdd: () -> Unit) {
    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(32.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center
    ) {
        Box(
            modifier = Modifier
                .size(80.dp)
                .clip(RoundedCornerShape(20.dp))
                .background(MaterialTheme.colorScheme.primary.copy(alpha = 0.15f)),
            contentAlignment = Alignment.Center
        ) {
            Icon(
                imageVector = Icons.Outlined.Shield,
                contentDescription = null,
                modifier = Modifier.size(40.dp),
                tint = MaterialTheme.colorScheme.primary
            )
        }

        Spacer(modifier = Modifier.height(16.dp))

        Text(
            text = "Welcome to Privycs VPN",
            style = MaterialTheme.typography.titleLarge,
            fontWeight = FontWeight.Bold,
            color = MaterialTheme.colorScheme.onBackground
        )

        Spacer(modifier = Modifier.height(8.dp))

        Text(
            text = "Import a VPN config to get started",
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant
        )

        Spacer(modifier = Modifier.height(24.dp))

        Button(
            onClick = onNavigateToAdd,
            colors = ButtonDefaults.buttonColors(
                containerColor = MaterialTheme.colorScheme.primary
            )
        ) {
            Text("Add Connection")
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun ConnectButton(
    isConnected: Boolean,
    isConnecting: Boolean,
    isSinkholeActive: Boolean = false,
    activeProtocol: VpnProtocol?,
    onClick: () -> Unit,
    onLongClick: () -> Unit = {},
) {
    val buttonSize = 140.dp
    val outerSize = 170.dp

    // Danger palette for the kill-switch sinkhole state. Tailwind
    // red-600 / red-700 - high-contrast warning that reads
    // unambiguously as "traffic is being blocked, this is not a
    // normal connected state". Takes precedence over isConnected
    // in every color choice below because a sinkhole can only be
    // engaged AFTER a successful connect.
    val dangerRed = Color(0xFFDC2626)
    val dangerRedDark = Color(0xFFB91C1C)

    val showGlowRing = isConnected || isSinkholeActive
    val glowRingColor = when {
        isSinkholeActive -> dangerRed.copy(alpha = 0.5f)
        isConnected -> PrivycsTeal.copy(alpha = 0.3f)
        else -> Color.Transparent
    }
    val hasSolidFill = isConnected || isSinkholeActive

    Box(
        modifier = Modifier.size(outerSize),
        contentAlignment = Alignment.Center
    ) {
        // Outer glow ring when connected or kill-switch-active
        if (showGlowRing) {
            Box(
                modifier = Modifier
                    .size(outerSize)
                    .clip(CircleShape)
                    .border(
                        width = 2.dp,
                        color = glowRingColor,
                        shape = CircleShape
                    )
            )
        }

        // Main button
        Box(
            modifier = Modifier
                .size(buttonSize)
                .shadow(
                    elevation = if (hasSolidFill) 12.dp else 4.dp,
                    shape = CircleShape,
                    ambientColor = when {
                        isSinkholeActive -> dangerRed.copy(alpha = 0.25f)
                        isConnected -> PrivycsTeal.copy(alpha = 0.25f)
                        else -> Color.Black.copy(alpha = 0.1f)
                    },
                    spotColor = when {
                        isSinkholeActive -> dangerRed.copy(alpha = 0.4f)
                        isConnected -> PrivycsTeal.copy(alpha = 0.4f)
                        else -> Color.Black.copy(alpha = 0.15f)
                    }
                )
                .clip(CircleShape)
                .then(
                    when {
                        isSinkholeActive -> Modifier.background(
                            Brush.linearGradient(
                                colors = listOf(dangerRed, dangerRedDark)
                            )
                        )
                        isConnected -> Modifier.background(
                            Brush.linearGradient(
                                colors = listOf(PrivycsTeal, PrivycsTealDark)
                            )
                        )
                        else -> Modifier
                            .background(MaterialTheme.colorScheme.surface)
                            .border(2.dp, MaterialTheme.colorScheme.outline, CircleShape)
                    }
                )
                .combinedClickable(
                    enabled = !isConnecting,
                    onClick = onClick,
                    onLongClick = onLongClick,
                ),
            contentAlignment = Alignment.Center
        ) {
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                if (isConnecting) {
                    CircularProgressIndicator(
                        modifier = Modifier.size(32.dp),
                        color = if (isConnected) Color.White else MaterialTheme.colorScheme.primary,
                        strokeWidth = 3.dp
                    )
                } else if (isSinkholeActive) {
                    // Kill-switch sinkhole: shield-with-x icon on the
                    // danger-red button communicates "protection by
                    // blocking" at a glance, without the user needing
                    // to read the status text.
                    Icon(
                        imageVector = Icons.Filled.GppBad,
                        contentDescription = "Kill Switch Active",
                        modifier = Modifier.size(56.dp),
                        tint = Color.White,
                    )
                } else {
                    // Show the active protocol's brand icon inside the
                    // connect button so the user sees at-a-glance which
                    // stack will run (or is running). Falls back to the
                    // generic shield-check when no protocol is selected
                    // yet (first-run, empty connection repo).
                    val tint = if (isConnected) Color.White
                    else MaterialTheme.colorScheme.onSurfaceVariant
                    val iconRes = when (activeProtocol) {
                        VpnProtocol.WIREGUARD -> R.drawable.ic_protocol_wireguard
                        VpnProtocol.OPENVPN -> R.drawable.ic_protocol_openvpn
                        VpnProtocol.IPSEC -> R.drawable.ic_protocol_strongswan
                        null -> null
                    }
                    if (iconRes != null) {
                        Icon(
                            painter = androidx.compose.ui.res.painterResource(id = iconRes),
                            contentDescription = activeProtocol?.label,
                            modifier = Modifier.size(56.dp),
                            tint = tint
                        )
                    } else {
                        Icon(
                            imageVector = Icons.Filled.GppGood,
                            contentDescription = "Privycs",
                            modifier = Modifier.size(56.dp),
                            tint = tint
                        )
                    }
                }
                Spacer(modifier = Modifier.height(4.dp))
                Text(
                    text = when {
                        isConnecting && isConnected -> "Disconnecting..."
                        isConnecting -> "Connecting..."
                        isConnected -> "Connected"
                        else -> "Connect"
                    },
                    style = MaterialTheme.typography.labelSmall,
                    fontWeight = FontWeight.SemiBold,
                    color = if (isConnected) Color.White.copy(alpha = 0.9f)
                    else MaterialTheme.colorScheme.onSurfaceVariant
                )
            }
        }
    }
}

@Composable
private fun ProtocolBadges(
    availableProtocols: List<VpnProtocol>,
    activeProtocol: VpnProtocol,
    onSelect: (VpnProtocol) -> Unit
) {
    LazyRow(
        horizontalArrangement = Arrangement.spacedBy(6.dp)
    ) {
        items(availableProtocols) { protocol ->
            val isActive = protocol == activeProtocol
            val badgeColor = when (protocol) {
                VpnProtocol.WIREGUARD -> WireGuardRed
                VpnProtocol.OPENVPN -> OpenVpnOrange
                VpnProtocol.IPSEC -> IpSecBlue
            }

            val bgColor by animateColorAsState(
                targetValue = if (isActive) badgeColor.copy(alpha = 0.2f)
                else MaterialTheme.colorScheme.surfaceVariant,
                label = "badgeColor"
            )

            val textColor by animateColorAsState(
                targetValue = if (isActive) badgeColor
                else MaterialTheme.colorScheme.onSurfaceVariant,
                label = "badgeTextColor"
            )

            Row(
                modifier = Modifier
                    .clip(RoundedCornerShape(50))
                    .background(bgColor)
                    .then(
                        if (isActive) Modifier.border(1.dp, badgeColor.copy(alpha = 0.3f), RoundedCornerShape(50))
                        else Modifier
                    )
                    .clickable { onSelect(protocol) }
                    .padding(horizontal = 12.dp, vertical = 6.dp),
                verticalAlignment = Alignment.CenterVertically
            ) {
                androidx.compose.material3.Icon(
                    painter = androidx.compose.ui.res.painterResource(id = protocol.badgeDrawable()),
                    contentDescription = null,
                    tint = textColor,
                    modifier = Modifier.size(14.dp)
                )
                Spacer(modifier = Modifier.width(4.dp))
                Text(
                    text = protocol.label,
                    style = MaterialTheme.typography.labelSmall,
                    fontWeight = FontWeight.Medium,
                    color = textColor
                )
            }
        }
    }
}

/**
 * Official brand drawables per protocol, ported from the privycs web
 * frontend (UsersView.vue for WireGuard/OpenVPN, StrongSwanIcon.vue for
 * strongSwan). Drawn monochrome so the badge Row's tint can color them
 * with the protocol's accent at runtime.
 */
private fun VpnProtocol.badgeDrawable(): Int = when (this) {
    VpnProtocol.WIREGUARD -> com.privycs.vpn.R.drawable.ic_protocol_wireguard
    VpnProtocol.OPENVPN   -> com.privycs.vpn.R.drawable.ic_protocol_openvpn
    VpnProtocol.IPSEC     -> com.privycs.vpn.R.drawable.ic_protocol_strongswan
}

@Composable
private fun TransferCard(
    label: String,
    bytes: Long,
    icon: androidx.compose.ui.graphics.vector.ImageVector,
    iconTint: Color,
    speedHistory: List<Float>,
    sparklineColor: Color,
    modifier: Modifier = Modifier
) {
    Card(
        modifier = modifier,
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surface
        )
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(12.dp),
            horizontalAlignment = Alignment.CenterHorizontally
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(
                    imageVector = icon,
                    contentDescription = null,
                    modifier = Modifier.size(12.dp),
                    tint = iconTint
                )
                Spacer(modifier = Modifier.width(4.dp))
                Text(
                    text = label,
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant
                )
            }
            Spacer(modifier = Modifier.height(4.dp))
            Text(
                text = formatBytes(bytes),
                style = MaterialTheme.typography.titleMedium,
                fontWeight = FontWeight.SemiBold,
                color = MaterialTheme.colorScheme.onSurface
            )
            // Current per-second speed derived from the most recent
            // delta - the cumulative byte counter above tells the user
            // "how much in total", this tiny label tells them "how fast
            // right now". Matches the desktop card's "12.3 KB/s" line.
            Text(
                text = SpeedTracker.formatSpeed(speedHistory.lastOrNull() ?: 0f),
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant
            )
            Spacer(modifier = Modifier.height(4.dp))
            SpeedSparkline(
                data = speedHistory,
                color = sparklineColor,
            )
        }
    }
}

@Composable
private fun ConnectionDetails(
    localAddress: String,
    serverAddress: String,
    lastHandshake: String
) {
    Column(
        modifier = Modifier.fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(6.dp)
    ) {
        if (localAddress.isNotBlank()) {
            DetailRow(label = "VPN IP", value = localAddress)
        }
        if (serverAddress.isNotBlank()) {
            DetailRow(label = "Endpoint", value = serverAddress)
        }
        if (lastHandshake.isNotBlank()) {
            DetailRow(label = "Handshake", value = lastHandshake)
        }
    }
}

@Composable
private fun DetailRow(label: String, value: String) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(8.dp))
            .background(MaterialTheme.colorScheme.surface)
            .padding(horizontal = 12.dp, vertical = 8.dp),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically
    ) {
        Text(
            text = label,
            style = MaterialTheme.typography.labelSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant
        )
        Text(
            text = value,
            style = MaterialTheme.typography.labelSmall,
            fontFamily = FontFamily.Monospace,
            color = MaterialTheme.colorScheme.onSurface
        )
    }
}

private fun formatUptime(millis: Long): String {
    val totalSeconds = millis / 1000
    val hours = totalSeconds / 3600
    val minutes = (totalSeconds % 3600) / 60
    val seconds = totalSeconds % 60
    return "%02d:%02d:%02d".format(hours, minutes, seconds)
}

private fun formatBytes(bytes: Long): String {
    if (bytes <= 0) return "0 B"
    val units = arrayOf("B", "KB", "MB", "GB", "TB")
    val digitGroups = (Math.log(bytes.toDouble()) / Math.log(1024.0)).toInt()
    val idx = digitGroups.coerceIn(0, units.size - 1)
    return "%.1f %s".format(bytes / Math.pow(1024.0, idx.toDouble()), units[idx])
}
