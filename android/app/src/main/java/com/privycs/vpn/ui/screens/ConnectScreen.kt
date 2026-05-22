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
import androidx.compose.ui.res.stringResource
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
import com.privycs.vpn.ui.theme.AmneziaWgIndigo
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

    // Pool indicator wiring. When a pool is active (poolRegistry's
    // activeId is set) the card replaces the visual position the
    // single-connection name takes, displaying current member +
    // countdown to next rotation.
    val poolRepoForIndicator = remember { PrivycsApp.instance.poolRepository }
    val poolRegistryState by poolRepoForIndicator.registry.collectAsState()
    val activePool = remember(poolRegistryState.activeId, poolRegistryState.pools) {
        poolRegistryState.pools.firstOrNull { it.id == poolRegistryState.activeId }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(horizontal = 20.dp),
        horizontalAlignment = Alignment.CenterHorizontally
    ) {
        // Top spacer trimmed 24 → 16 in v0.9.14.32 to bring the
        // tunnel-health pill above the fold on shorter phones (Pixel
        // 3a, Galaxy A series). User-reported: pill rendered offscreen
        // on every connect because all the spacing above it summed past
        // the viewport. Eight spacers across this screen each give back
        // 4-8dp; total -40dp moves the pill into safe zone without
        // making the layout feel cramped on larger phones.
        Spacer(modifier = Modifier.height(16.dp))

        // Pool indicator card — only when a pool is the active selection.
        if (activePool != null) {
            // Consolidated single-poll: previously two separate
            // produceState blocks each polled the state-repo every
            // 1s, producing 2 lock acquisitions per second per
            // recomposition cycle. The PoolListItem below is keyed
            // on (activeMemberId, pendingMemberId) so a unified
            // poll keeps the recompose semantics identical while
            // halving the lock-contention rate.
            val poolMemberIds by androidx.compose.runtime.produceState(
                "" to "", activePool.id
            ) {
                while (true) {
                    val a = poolRepoForIndicator.activeMemberId(activePool.id)
                    val p = poolRepoForIndicator.pendingMemberId(activePool.id)
                    value = a to p
                    delay(1000)
                }
            }
            val activeMemberId = poolMemberIds.first
            val pendingMemberId = poolMemberIds.second
            val activeMember = activePool.memberById(activeMemberId)
            val pendingMember = activePool.memberById(pendingMemberId)
            val item = remember(activePool, activeMemberId, pendingMemberId) {
                com.privycs.vpn.data.models.PoolListItem(
                    id = activePool.id,
                    name = activePool.name,
                    policy = activePool.policy,
                    memberCount = activePool.members.size,
                    activeMemberId = activeMemberId,
                    activeMemberName = activeMember?.name.orEmpty(),
                    activeMemberCountry = activeMember?.country.orEmpty(),
                    pendingMemberId = pendingMemberId,
                    pendingMemberName = pendingMember?.name.orEmpty(),
                    pendingMemberCountry = pendingMember?.country.orEmpty(),
                    isActive = true
                )
            }
            // Round-Robin → countdown anchored to the actual scheduled
            // rotation timestamp from VpnStatus, set authoritatively
            // by PrivycsVpnService.broadcastPoolStatus on every
            // pool connect / rotation / pre-warm. Zero means no
            // active rotation (non-RR pool, or pool not yet
            // connected via the service path).
            //
            // Earlier draft passed `intervalMin * 60 * 1000` as a
            // delta - a constant value that remember() never saw
            // change, so the countdown stuck at 00:00 after the
            // first tick reached zero. The fix is the timestamp:
            // each rotation pushes a fresh `nextRotationAt`,
            // remember() invalidates, the countdown restarts.
            com.privycs.vpn.ui.components.PoolIndicatorCard(
                pool = item,
                nextRotationAt = status.nextRotationAt,
                pendingMemberName = pendingMember?.name,
                pendingMemberCountry = pendingMember?.country,
                onClick = { onNavigateToConnections() }
            )
            Spacer(modifier = Modifier.height(12.dp))
        }

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
                        context.getString(R.string.connect_kill_switch_active_toast),
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
                            if (!PrivycsApp.instance.networkRulesRepository.hasRules) return@launch
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

        Spacer(modifier = Modifier.height(8.dp))

        // Manual-pause countdown + Resume link. Only shown while a
        // user-initiated pause is active. VpnPauseTimer drives the
        // auto-reconnect at expiry; the Resume link cancels the
        // timer and issues an immediate USER-source connect.
        if (isManuallyPaused) {
            val mins = pauseRemainingSec / 60
            val secs = pauseRemainingSec % 60
            Text(
                text = stringResource(
                    R.string.connect_paused_remaining,
                    String.format("%d:%02d", mins, secs)
                ),
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
                Text(stringResource(R.string.connect_resume_now))
            }
            Spacer(modifier = Modifier.height(8.dp))
        }

        // Uptime
        // v0.9.14.5: shrunk from headlineSmall (~24sp) to titleMedium
        // (~16sp) so the tunnel-health pill rendered further down
        // remains visible without scrolling. User feedback: the big
        // uptime number was eating screen real-estate that pushed
        // the health pill below the fold on shorter phones.
        if (isConnected && status.uptime > 0) {
            Text(
                text = formatUptime(status.uptime),
                style = MaterialTheme.typography.titleMedium,
                fontFamily = FontFamily.Monospace,
                color = MaterialTheme.colorScheme.onBackground
            )
            // v0.9.14.17: tighter spacer below uptime — was 8dp,
            // 4dp keeps visual breathing room while saving vertical
            // height so the connect-dropdown + content below fit
            // without scroll on shorter phones.
            Spacer(modifier = Modifier.height(4.dp))
        }

        // Connection name with picker.
        //
        // Earlier draft was `if (activeConnection != null)` which
        // hid the entire dropdown trigger when a pool was the active
        // selection (single-connection's activeId was cleared on
        // pool activate, so getActive() returned null). The dropdown
        // content already supports both connections AND pools - we
        // just need to render it whenever EITHER is present.
        val hasAnySelection = activeConnection != null || activePool != null
        if (hasAnySelection) {
            // Label preference order:
            //   1. tunnel-reported status.connectionName (live, e.g.
            //      pool name pushed by broadcastPoolStatus)
            //   2. active pool name (when pool is selected but
            //      connect has not yet broadcast)
            //   3. active single-connection name
            val labelText = status.connectionName.ifBlank {
                activePool?.name ?: activeConnection?.name.orEmpty()
            }
            Box {
                Row(
                    modifier = Modifier
                        .clickable { showConnectionPicker = !showConnectionPicker }
                        // v0.9.14.17: vertical padding 4dp -> 2dp.
                        // Touch-target requirement (48dp) easily met
                        // by text+icon at bodyMedium; the extra 2dp
                        // top + 2dp bottom were pure white-space.
                        .padding(horizontal = 4.dp, vertical = 2.dp),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    Text(
                        text = labelText,
                        style = MaterialTheme.typography.bodyMedium,
                        fontWeight = FontWeight.Medium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                    Icon(
                        imageVector = if (showConnectionPicker) Icons.Filled.KeyboardArrowUp
                        else Icons.Filled.KeyboardArrowDown,
                        contentDescription = stringResource(R.string.connect_switch_connection),
                        // 18dp -> 16dp matches the Material icon
                        // baseline for in-line-with-bodyMedium
                        // contexts; visually paired better with
                        // the body text + saves 2dp height.
                        modifier = Modifier.size(16.dp),
                        tint = MaterialTheme.colorScheme.onSurfaceVariant
                    )
                }

                // The picker now lists single connections AND pools.
                // Previously it only listed connections, so users with
                // a configured pool could not switch into pool mode
                // from this screen — a pool only became active via the
                // ConnectionsScreen + tap-into-pool-detail flow. We
                // expand whenever the user has more than one selectable
                // target across both kinds (1 conn + 1 pool counts).
                val poolEntries = poolRegistryState.pools
                val totalSelectable = connections.size + poolEntries.size
                DropdownMenu(
                    expanded = showConnectionPicker && totalSelectable > 1,
                    onDismissRequest = { showConnectionPicker = false }
                ) {
                    if (connections.isNotEmpty()) {
                        Text(
                            text = stringResource(R.string.connect_section_connections),
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.padding(horizontal = 12.dp, vertical = 6.dp)
                        )
                    }
                    connections.forEach { conn ->
                        DropdownMenuItem(
                            text = {
                                Row(verticalAlignment = Alignment.CenterVertically) {
                                    Box(
                                        modifier = Modifier
                                            .size(6.dp)
                                            .clip(CircleShape)
                                            .background(
                                                if (conn.id == registry.activeId &&
                                                    poolRegistryState.activeId.isEmpty())
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
                                    // Brand-coloured protocol icons replacing
                                    // the previous "WG/OVPN/IPSec" text join
                                    // (joinToString { it.shortLabel }) for
                                    // visual consistency with the Desktop
                                    // ConnectionView dropdown and the rest of
                                    // the app's icon-language. Each protocol
                                    // gets its own tinted vector drawable.
                                    Row(
                                        verticalAlignment = Alignment.CenterVertically,
                                        horizontalArrangement = Arrangement.spacedBy(4.dp)
                                    ) {
                                        conn.availableProtocols().forEach { p ->
                                            val iconRes = when (p) {
                                                VpnProtocol.AMNEZIAWG -> R.drawable.ic_protocol_amneziawg
                                                VpnProtocol.WIREGUARD -> R.drawable.ic_protocol_wireguard
                                                VpnProtocol.OPENVPN -> R.drawable.ic_protocol_openvpn
                                                VpnProtocol.IPSEC -> R.drawable.ic_protocol_strongswan
                                            }
                                            val iconTint = when (p) {
                                                VpnProtocol.AMNEZIAWG -> AmneziaWgIndigo
                                                VpnProtocol.WIREGUARD -> com.privycs.vpn.ui.theme.WireGuardRed
                                                VpnProtocol.OPENVPN -> com.privycs.vpn.ui.theme.OpenVpnOrange
                                                VpnProtocol.IPSEC -> com.privycs.vpn.ui.theme.IpSecBlue
                                            }
                                            // Icon (tinted) so AWG looks
                                            // and behaves like WG/OVPN/
                                            // IPSec — single mono path
                                            // drawable + brand-color tint.
                                            // Previous Image-based render
                                            // shipped the AWG multi-colour
                                            // brand mark which clashed
                                            // visually with the tint cascade
                                            // applied to the other three.
                                            Icon(
                                                painter = androidx.compose.ui.res.painterResource(id = iconRes),
                                                contentDescription = p.shortLabel,
                                                tint = iconTint,
                                                modifier = Modifier.size(14.dp)
                                            )
                                        }
                                    }
                                }
                            },
                            onClick = {
                                showConnectionPicker = false
                                // If a pool was active, deselect it so
                                // the picked single connection becomes
                                // the actual active target.
                                if (poolRegistryState.activeId.isNotEmpty()) {
                                    coroutineScope.launch {
                                        poolRepoForIndicator.setActiveId("")
                                    }
                                }
                                if (conn.id != registry.activeId) {
                                    val willReconnect = vpnManager.switchActiveConnection(conn.id)
                                    if (willReconnect &&
                                        com.privycs.vpn.util.KillSwitchManager.isArmed()
                                    ) {
                                        android.widget.Toast.makeText(
                                            context,
                                            context.getString(R.string.connect_kill_switch_block_reconnect_toast),
                                            android.widget.Toast.LENGTH_LONG,
                                        ).show()
                                    }
                                }
                            }
                        )
                    }
                    if (poolEntries.isNotEmpty()) {
                        if (connections.isNotEmpty()) {
                            androidx.compose.material3.HorizontalDivider(
                                modifier = Modifier.padding(vertical = 4.dp)
                            )
                        }
                        Text(
                            text = stringResource(R.string.connect_section_pools),
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.padding(horizontal = 12.dp, vertical = 6.dp)
                        )
                        poolEntries.forEach { p ->
                            DropdownMenuItem(
                                text = {
                                    Row(verticalAlignment = Alignment.CenterVertically) {
                                        Box(
                                            modifier = Modifier
                                                .size(6.dp)
                                                .clip(CircleShape)
                                                .background(
                                                    if (p.id == poolRegistryState.activeId)
                                                        MaterialTheme.colorScheme.primary
                                                    else MaterialTheme.colorScheme.outline
                                                )
                                        )
                                        Spacer(modifier = Modifier.width(8.dp))
                                        Text(
                                            text = p.name,
                                            style = MaterialTheme.typography.bodySmall
                                        )
                                        Spacer(modifier = Modifier.weight(1f))
                                        Text(
                                            text = "${p.policy.displayName} · ${p.members.size}",
                                            style = MaterialTheme.typography.labelSmall,
                                            color = MaterialTheme.colorScheme.onSurfaceVariant
                                        )
                                    }
                                },
                                onClick = {
                                    showConnectionPicker = false
                                    // Funnel through VpnServiceManager
                                    // .switchActivePool - same single
                                    // entry point used by every other
                                    // pool-pick path. Selection is
                                    // never an auto-connect; connect
                                    // is owned by COD (when on) or
                                    // the big Connect button (when
                                    // off). Re-tapping the same pool
                                    // is idempotent (returns false
                                    // without firing anything).
                                    vpnManager.switchActivePool(p.id)
                                }
                            )
                        }
                    }
                }
            }

            Spacer(modifier = Modifier.height(8.dp))

            // Protocol badges. Single-connection only: pools mix
            // members with potentially different protocols, so a
            // single-protocol picker UI is meaningless. When a pool
            // is active, skip the badges entirely - the pool
            // indicator card above already shows policy/member info
            // in the appropriate framing.
            //
            // Defence-in-depth: pool wins over connectionRepo's
            // activeId. Pre-fix, paths that activated a pool
            // without clearing connectionRepo.setActive("") (the
            // ConnectScreen picker was one of them) left
            // activeConnection non-null while activePool was set,
            // so this row showed protocol pills from the previous
            // single connection underneath the pool card. Adding
            // `activePool == null` as a gate makes the UI correct
            // even if a future caller forgets to clear single-
            // active when activating a pool.
            if (activeConnection != null && activePool == null) {
                ProtocolBadges(
                    configs = activeConnection.orderedConfigs(),
                    activeConfigId = activeConnection.activeConfigId,
                    onSelect = { configId ->
                        vpnManager.switchConfig(configId)
                    }
                )

                Spacer(modifier = Modifier.height(4.dp))
            }

            // Server endpoint. Source preference:
            //   1. status.serverEndpoint (live tunnel push - works
            //      for both pool members AND single connections)
            //   2. single-connection's stored config endpoint
            //   3. blank (pool with no broadcast yet - the pool
            //      indicator card carries the member info instead)
            // v0.9.14.76: defensive filter against "{" / "[" / "<"
            // glyphs leaking through. The pre-v0.9.14.70 ConfigParser
            // line-based parser would extract "{" from the .sswan
            // JSON-object-opening line and persist it as
            // ProtocolConfig.serverAddress. The v0.9.14.70 heal
            // (ConnectionRepository.load → isCorruptServerAddress)
            // catches the disk values, but if any code path
            // resurrects a corrupt value at runtime (e.g. a stale
            // VpnStatus.serverEndpoint after disconnect, or a
            // future edge case nobody anticipated), the UI must
            // still render gracefully. Same condition mirrored from
            // the repository heal — keep them in sync.
            val rawEndpoint = status.serverEndpoint.ifBlank {
                activeConnection?.getActiveConfig()?.serverAddress.orEmpty()
            }
            val endpoint = sanitizeEndpointForDisplay(rawEndpoint)
            if (endpoint.isNotBlank()) {
                Text(
                    text = endpoint,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    textAlign = TextAlign.Center
                )

                // Country flag + city + country line. Pool path uses
                // VpnStatus.activeMemberCountry + activeMemberName
                // (broadcast by the service after pickAndConnect).
                // Single-connection path falls back to parsing the
                // connection name (which often follows the same
                // "<cc>-<city3>-<n>" pattern when imported from a
                // commercial provider).
                val cc = status.activeMemberCountry.ifBlank { "" }
                val labelName = status.activeMemberName.ifBlank {
                    status.connectionName.ifBlank { activeConnection?.name.orEmpty() }
                }
                val flag = com.privycs.vpn.data.PoolHostnameLabels.flagEmojiFromCode(cc)
                val city = com.privycs.vpn.data.PoolHostnameLabels.cityFromHostname(labelName)
                val country = com.privycs.vpn.data.PoolHostnameLabels.countryNameFromCode(cc)
                val locationLine = buildString {
                    if (flag.isNotEmpty()) append(flag).append("  ")
                    when {
                        city.isNotEmpty() && country.isNotEmpty() ->
                            append("$city, $country")
                        city.isNotEmpty() -> append(city)
                        country.isNotEmpty() -> append(country)
                    }
                }.trim()
                if (locationLine.isNotEmpty()) {
                    Spacer(modifier = Modifier.height(2.dp))
                    Text(
                        text = locationLine,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        textAlign = TextAlign.Center
                    )
                }
                Spacer(modifier = Modifier.height(12.dp))
            }
        }

        // Transfer stats
        if (isConnected) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                TransferCard(
                    label = stringResource(R.string.connect_transfer_download),
                    bytes = status.rxBytes,
                    icon = Icons.Outlined.ArrowDownward,
                    iconTint = Color(0xFF22C55E),
                    speedHistory = rxSpeedHistory,
                    sparklineColor = Color(0xFF4ADE80),
                    modifier = Modifier.weight(1f)
                )
                TransferCard(
                    label = stringResource(R.string.connect_transfer_upload),
                    bytes = status.txBytes,
                    icon = Icons.Outlined.ArrowUpward,
                    iconTint = Color(0xFF3B82F6),
                    speedHistory = txSpeedHistory,
                    sparklineColor = Color(0xFF60A5FA),
                    modifier = Modifier.weight(1f)
                )
            }
            Spacer(modifier = Modifier.height(8.dp))
        }

        // Connection details (VPN IP / Endpoint / last handshake).
        // Pre-fix this was gated on `activeConnection != null`,
        // which is null for pools because pool activation clears
        // the single-connection activeId. Result: the detail panel
        // disappeared entirely on the Connect screen for pool
        // users, even when the tunnel was up. Now we render the
        // panel when EITHER a single connection or a pool is the
        // active selection. The DetailRow components inside
        // ConnectionDetails are individually conditional on their
        // value being non-blank, so empty fields stay hidden -
        // e.g. lastHandshake stays out of view for OpenVPN/IPSec
        // protocols that don't track it.
        if (activeConnection != null || activePool != null) {
            ConnectionDetails(
                localAddress = status.localAddress,
                serverAddress = sanitizeEndpointForDisplay(status.serverEndpoint),
                lastHandshake = status.lastHandshake
            )
        }

        // Tunnel-health pill. Visible only while a tunnel is up
        // AND the monitor is running (state != INACTIVE). The
        // AND-with-connected gate is defensive: TunnelHealthMonitor
        // is a process singleton whose state persists across screen
        // recompositions; relying on stop() alone leaves the pill
        // visible if the connected->disconnected transition is
        // missed (e.g. status-poll dropped or stop() raced by a
        // recompose). connected == false plainly means there is no
        // tunnel to be healthy/degraded about, so hide the pill.
        val healthState by com.privycs.vpn.service.TunnelHealthMonitor.state.collectAsState()
        if (status.connected &&
            healthState != com.privycs.vpn.service.TunnelHealthMonitor.State.INACTIVE) {
            Spacer(modifier = Modifier.height(4.dp))
            HealthPill(healthState)
        }

        // Error display. Card with errorContainer background gives
        // pool-related failures (which can be long messages like
        // "Pool X: all members marked unreachable - tap Reset...")
        // enough visual weight that the user notices them. Earlier
        // bodySmall + plain text was easy to miss when buried below
        // the transfer cards.
        if (!status.error.isNullOrBlank()) {
            Spacer(modifier = Modifier.height(12.dp))
            androidx.compose.material3.Card(
                modifier = Modifier.fillMaxWidth(),
                colors = androidx.compose.material3.CardDefaults.cardColors(
                    containerColor = MaterialTheme.colorScheme.errorContainer
                )
            ) {
                Text(
                    text = status.error!!,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onErrorContainer,
                    textAlign = TextAlign.Center,
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(12.dp)
                )
            }
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
            text = stringResource(R.string.connect_welcome_title),
            style = MaterialTheme.typography.titleLarge,
            fontWeight = FontWeight.Bold,
            color = MaterialTheme.colorScheme.onBackground
        )

        Spacer(modifier = Modifier.height(8.dp))

        Text(
            text = stringResource(R.string.connect_welcome_subtitle),
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
            Text(stringResource(R.string.connect_add_connection))
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
                        contentDescription = stringResource(R.string.connect_kill_switch_active_desc),
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
                        // AWG behaves like WG: single monochrome
                        // path-drawable that takes the tint cascade.
                        // Connect-button background already drives
                        // the contrast (green when connected, surface
                        // variant when not) so we just hand it a
                        // tintable silhouette and Icon does the rest.
                        // The mono PNG (alpha-aware single-color) is
                        // safe with Compose painterResource; the old
                        // ic_protocol_amneziawg_badge layer-list was
                        // not and crashed the UI thread.
                        VpnProtocol.AMNEZIAWG -> R.drawable.ic_protocol_amneziawg
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
                            contentDescription = stringResource(R.string.connect_app_name_desc),
                            modifier = Modifier.size(56.dp),
                            tint = tint
                        )
                    }
                }
                Spacer(modifier = Modifier.height(4.dp))
                Text(
                    text = when {
                        isConnecting && isConnected -> stringResource(R.string.connect_status_disconnecting)
                        isConnecting -> stringResource(R.string.connect_status_connecting)
                        isConnected -> stringResource(R.string.connect_status_connected)
                        else -> stringResource(R.string.connect_status_connect)
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

@OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)
@Composable
private fun ProtocolBadges(
    configs: List<com.privycs.vpn.data.models.ProtocolConfig>,
    activeConfigId: String,
    onSelect: (String) -> Unit
) {
    // One pill = one protocol type. Configs of the same protocol
    // type fold into a single pill; tapping a multi-config pill
    // opens a bottom-sheet picker (analog Pool member-switcher)
    // so the user can pick which underlying config goes active.
    // Pre-v0.9.15.18 we rendered one pill per config and disambig'd
    // via filename/nickname — produced cluttered rows of
    // "Privycs Shielded" / "Home-UDP" labels where users wanted
    // simply "WireGuard". New model: pill row stays protocol-
    // focused, disambig happens on demand.
    val groups = remember(configs) {
        configs.groupBy { it.protocol }
            .map { (protocol, list) -> protocol to list }
            .sortedBy { it.first.ordinal }
    }
    // Which protocol's picker sheet is open. null = none.
    var openPickerProtocol by remember { mutableStateOf<VpnProtocol?>(null) }

    LazyRow(
        modifier = Modifier.fillMaxWidth(),
        contentPadding = androidx.compose.foundation.layout.PaddingValues(horizontal = 4.dp),
        horizontalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        items(groups) { (protocol, groupConfigs) ->
            val isActive = groupConfigs.any { it.id == activeConfigId }
            val badgeColor = when (protocol) {
                VpnProtocol.AMNEZIAWG -> AmneziaWgIndigo
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

            val multi = groupConfigs.size > 1

            Row(
                modifier = Modifier
                    .clip(RoundedCornerShape(50))
                    .background(bgColor)
                    .then(
                        if (isActive) Modifier.border(1.dp, badgeColor.copy(alpha = 0.3f), RoundedCornerShape(50))
                        else Modifier
                    )
                    .clickable {
                        if (multi) {
                            openPickerProtocol = protocol
                        } else {
                            onSelect(groupConfigs.first().id)
                        }
                    }
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
                if (multi) {
                    // Count superscript + caret affordance. Mirrors the
                    // desktop ConnectionView.vue picker's `²ˇ` styling.
                    Spacer(modifier = Modifier.width(3.dp))
                    Text(
                        text = groupConfigs.size.toString(),
                        style = MaterialTheme.typography.labelSmall.copy(
                            fontSize = 9.sp,
                            fontWeight = FontWeight.SemiBold,
                        ),
                        color = textColor,
                    )
                    Spacer(modifier = Modifier.width(2.dp))
                    Icon(
                        imageVector = Icons.Filled.ArrowDropDown,
                        contentDescription = stringResource(R.string.connect_pick_config),
                        tint = textColor,
                        modifier = Modifier.size(12.dp),
                    )
                }
            }
        }
    }

    val pickerProtocol = openPickerProtocol
    if (pickerProtocol != null) {
        val pickerConfigs = groups.firstOrNull { it.first == pickerProtocol }?.second ?: emptyList()
        if (pickerConfigs.size > 1) {
            MultiConfigPickerSheet(
                protocol = pickerProtocol,
                configs = pickerConfigs,
                activeConfigId = activeConfigId,
                onSelect = { configId ->
                    openPickerProtocol = null
                    onSelect(configId)
                },
                onDismiss = { openPickerProtocol = null },
            )
        }
    }
}

/**
 * Bottom-sheet picker shown when a protocol pill represents more
 * than one ProtocolConfig (multi-config-per-protocol). Lists the
 * configs of that protocol with their display label + stored server
 * endpoint and the current "active" marker. Pattern adapted from
 * the existing pool member-switcher modal — for protocol-configs
 * we use a bottom-sheet because the list is usually short (2-5
 * entries) and stays anchored to the bottom of the screen for
 * thumb-reach.
 */
@OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)
@Composable
private fun MultiConfigPickerSheet(
    protocol: VpnProtocol,
    configs: List<com.privycs.vpn.data.models.ProtocolConfig>,
    activeConfigId: String,
    onSelect: (String) -> Unit,
    onDismiss: () -> Unit,
) {
    val badgeColor = when (protocol) {
        VpnProtocol.AMNEZIAWG -> AmneziaWgIndigo
        VpnProtocol.WIREGUARD -> WireGuardRed
        VpnProtocol.OPENVPN -> OpenVpnOrange
        VpnProtocol.IPSEC -> IpSecBlue
    }
    androidx.compose.material3.ModalBottomSheet(
        onDismissRequest = onDismiss,
    ) {
        Column(modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
            Text(
                text = stringResource(R.string.connect_choose_config, protocol.label),
                style = MaterialTheme.typography.titleSmall,
                fontWeight = FontWeight.SemiBold,
                color = MaterialTheme.colorScheme.onSurface,
                modifier = Modifier.padding(bottom = 8.dp)
            )
            for (cfg in configs) {
                val isActive = cfg.id == activeConfigId
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(8.dp))
                        .clickable { onSelect(cfg.id) }
                        .padding(horizontal = 12.dp, vertical = 10.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Box(
                        modifier = Modifier
                            .size(8.dp)
                            .clip(CircleShape)
                            .background(
                                if (isActive) badgeColor
                                else MaterialTheme.colorScheme.outline
                            )
                    )
                    Spacer(modifier = Modifier.width(12.dp))
                    Column(modifier = Modifier.weight(1f)) {
                        Text(
                            text = cfg.displayLabel(),
                            style = MaterialTheme.typography.bodyMedium,
                            color = if (isActive) badgeColor
                            else MaterialTheme.colorScheme.onSurface,
                            fontWeight = if (isActive) FontWeight.SemiBold else FontWeight.Normal,
                        )
                        if (cfg.serverAddress.isNotBlank()) {
                            Text(
                                text = cfg.serverAddress,
                                style = MaterialTheme.typography.labelSmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                    }
                    if (isActive) {
                        Text(
                            text = stringResource(R.string.connect_config_active),
                            style = MaterialTheme.typography.labelSmall,
                            color = badgeColor,
                        )
                    }
                }
            }
            Spacer(modifier = Modifier.height(8.dp))
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
    // AWG → mono variant so the Icon tint cascade applies (matches
    // WG's behaviour). The multi-colour ic_protocol_amneziawg PNG
    // would ignore the tint and look out of place next to the
    // tinted WG / OVPN / IPSec icons.
    VpnProtocol.AMNEZIAWG -> com.privycs.vpn.R.drawable.ic_protocol_amneziawg
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
            DetailRow(label = stringResource(R.string.connect_detail_vpn_ip), value = localAddress)
        }
        if (serverAddress.isNotBlank()) {
            DetailRow(label = stringResource(R.string.connect_detail_endpoint), value = serverAddress)
        }
        if (lastHandshake.isNotBlank()) {
            DetailRow(label = stringResource(R.string.connect_detail_handshake), value = lastHandshake)
        }
    }
}

@Composable
private fun DetailRow(label: String, value: String) {
    // Split comma-separated values onto their own right-aligned
    // line. WireGuard's Address field is typically a list like
    // "10.66.245.13/32, fd6f:fc81:c4f8::e/128" - rendering that as
    // a single line wraps mid-IP at narrow widths and is hard to
    // scan. Stacking each entry right-aligned keeps the IPv4
    // above the IPv6 (insertion order) and makes both readable.
    // Single-value fields (Endpoint, Handshake) just render as a
    // single right-aligned line via the same code path.
    val parts = value.split(",").map { it.trim() }.filter { it.isNotBlank() }
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
        Column(horizontalAlignment = Alignment.End) {
            parts.forEach { part ->
                Text(
                    text = part,
                    style = MaterialTheme.typography.labelSmall,
                    fontFamily = FontFamily.Monospace,
                    color = MaterialTheme.colorScheme.onSurface,
                    textAlign = TextAlign.End,
                )
            }
        }
    }
}

/**
 * Tunnel-health traffic-light pill. Three states map to icon
 * dots + label: HEALTHY=green/"Tunnel OK", DEGRADED=orange/
 * "Tunnel checks failing", RECOVERING=red/"Recovery in
 * progress". Inactive state filters at the call site so this
 * composable always renders a visible pill.
 */
@Composable
private fun HealthPill(state: com.privycs.vpn.service.TunnelHealthMonitor.State) {
    val (color, label) = when (state) {
        com.privycs.vpn.service.TunnelHealthMonitor.State.HEALTHY ->
            Color(0xFF10B981) to stringResource(R.string.connect_health_ok)
        com.privycs.vpn.service.TunnelHealthMonitor.State.DEGRADED ->
            Color(0xFFF59E0B) to stringResource(R.string.connect_health_degraded)
        com.privycs.vpn.service.TunnelHealthMonitor.State.RECOVERING ->
            Color(0xFFDC2626) to stringResource(R.string.connect_health_recovering)
        else -> Color(0xFF6B7280) to ""
    }
    Row(
        modifier = Modifier
            .clip(RoundedCornerShape(12.dp))
            .background(color.copy(alpha = 0.12f))
            .padding(horizontal = 10.dp, vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(
            modifier = Modifier
                .size(8.dp)
                .clip(RoundedCornerShape(4.dp))
                .background(color),
        )
        Spacer(modifier = Modifier.width(6.dp))
        Text(
            text = label,
            style = MaterialTheme.typography.labelSmall,
            color = color,
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

/**
 * v0.9.14.76 defensive endpoint sanitiser. Mirrors the rule used by
 * ConnectionRepository.isCorruptServerAddress so the on-screen
 * server-endpoint row never renders parse-artefact glyphs even if a
 * corrupt value sneaks past the load-time heal.
 *
 * Returns "" for any of:
 *   - blank
 *   - single non-alphanumeric glyph ("{", "[", "<", "-" etc.)
 *   - starts with "{", "[", "<", or "-----" (= JSON / XML / PEM
 *     opening tokens — not valid hostnames or IPs)
 *
 * Real hostnames and IPs always start with an alphanumeric character
 * and have length > 1, so the bar is high enough that no legitimate
 * endpoint matches.
 */
private fun sanitizeEndpointForDisplay(s: String): String {
    val t = s.trim()
    if (t.isEmpty()) return ""
    if (t.length == 1 && !t[0].isLetterOrDigit()) return ""
    if (t.startsWith("{") || t.startsWith("[") ||
        t.startsWith("<") || t.startsWith("-----")
    ) return ""
    return t
}
