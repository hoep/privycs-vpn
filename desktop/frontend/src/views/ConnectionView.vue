<template>
  <div class="p-5 flex flex-col items-center min-h-[calc(100vh-7rem)]">
    <!-- No connections yet -->
    <div v-if="showWelcome" class="w-full max-w-sm mt-8 text-center">
      <div class="w-20 h-20 mx-auto mb-4 rounded-2xl bg-primary-600/20 flex items-center justify-center">
        <ShieldCheckIcon class="w-10 h-10 text-primary-400" />
      </div>
      <h2 class="text-lg font-bold text-gray-900 dark:text-white mb-2">Welcome to Privycs VPN</h2>
      <p class="text-sm text-gray-500 dark:text-gray-400 mb-6">Import a VPN config to get started</p>
      <router-link to="/add" class="btn-primary px-6 py-2.5 rounded-md text-sm">
        Add Connection
      </router-link>
    </div>

    <!-- Connection UI -->
    <template v-else>
      <!-- Transient notice toast. Auto-clears after 4s. Used for
           non-fatal user-feedback like "Kill Switch active. This will
           block your reconnect!" without blocking the UI. -->
      <transition name="fade">
        <div v-if="notice" class="w-full max-w-sm mb-3">
          <div class="card px-3 py-2 text-xs bg-yellow-500/10 border border-yellow-500/30 text-yellow-700 dark:text-yellow-300">
            {{ notice }}
          </div>
        </div>
      </transition>

      <!-- Pause banner: visible while a user-initiated VPN pause is
           active. Mirrors Android long-press pause - shows countdown
           plus a Resume-now link. -->
      <div v-if="isPaused" class="w-full max-w-sm mb-3">
        <div class="card px-3 py-2 flex items-center justify-between gap-2 text-xs bg-blue-500/10 border border-blue-500/30">
          <div class="flex items-center gap-2 text-blue-700 dark:text-blue-300">
            <PauseIcon class="w-4 h-4" />
            <span>Paused — {{ pauseLabel }} remaining</span>
          </div>
          <button @click="resumeNow" class="text-blue-700 dark:text-blue-300 hover:underline font-medium">
            Resume now
          </button>
        </div>
      </div>

      <!-- Pool indicator, always visible when a Pool is active. The
           policy is shown explicitly so the user understands why a
           specific server was chosen (Round-Robin rotates, Geo-Nearest
           matches country, Random is random). Round-Robin gets a big
           countdown to next rotation in its own visually-prominent
           row so it can NEVER be missed. -->
      <div v-if="poolStore.activePoolId" class="w-full max-w-sm mb-3">
        <div class="card px-3 py-2.5">
          <div class="flex items-center justify-between mb-1">
            <div class="flex items-center gap-1.5 min-w-0">
              <RectangleStackIcon class="w-3.5 h-3.5 text-primary-400 flex-shrink-0" />
              <span class="text-[11px] font-semibold text-gray-700 dark:text-gray-200 truncate">
                {{ activePoolName }}
              </span>
              <span class="text-[9px] uppercase tracking-wide text-gray-500 dark:text-gray-400 flex-shrink-0">
                · {{ activePoolPolicyShort }}
              </span>
            </div>
            <router-link
              :to="`/pool/${poolStore.activePoolId}`"
              class="text-[10px] text-primary-400 hover:text-primary-300 flex-shrink-0"
            >Edit</router-link>
          </div>

          <div v-if="activeMemberDisplay" class="text-[10px] text-gray-600 dark:text-gray-300 truncate">
            <span class="text-gray-400">Currently:</span> {{ activeMemberDisplay }}
          </div>

          <!-- Round-Robin countdown: prominent dedicated row.
               When idle_blocked the row tints amber and the label
               reads "Force-rotate in" (because rotation got deferred
               by traffic) - the configured interval line below makes
               it explicit so a 30-min countdown does not get
               misread as the user's interval setting. -->
          <div
            v-if="rotatorActive"
            class="mt-1.5 flex items-center justify-between gap-2 px-2 py-1.5 rounded-md ring-1"
            :class="rotatorIdleBlocked
              ? 'bg-amber-500/10 ring-amber-500/30'
              : 'bg-primary-500/10 ring-primary-500/20'"
          >
            <div class="flex items-center gap-1.5 min-w-0">
              <ArrowPathIcon class="w-3 h-3 flex-shrink-0"
                :class="rotatorIdleBlocked ? 'text-amber-500 animate-spin' : 'text-primary-400'" />
              <span class="text-[10px] text-gray-700 dark:text-gray-300 truncate">
                {{ rotatorPrimaryLabel }}
              </span>
            </div>
            <span
              class="text-xs font-mono font-semibold tabular-nums flex-shrink-0"
              :class="rotatorIdleBlocked ? 'text-amber-500 dark:text-amber-400' : 'text-primary-500 dark:text-primary-300'"
            >
              {{ rotatorCountdown }}
            </span>
          </div>
          <div v-if="rotatorActive" class="flex items-center justify-between text-[9px] text-gray-500 mt-0.5">
            <span v-if="rotatorIntervalLabel">{{ rotatorIntervalLabel }}</span>
            <span v-if="rotatorAtLine">{{ rotatorAtLine }}</span>
          </div>
        </div>
      </div>

      <!-- Connect-on-Demand banner, visible whenever the feature is
           enabled so the user knows the tunnel state may change
           automatically based on network conditions. Matches Android
           ConnectScreen on-demand banner. -->
      <div v-if="codStatus?.enabled" class="w-full max-w-sm mb-3">
        <div class="card flex items-center gap-2 px-3 py-2">
          <span class="inline-block w-2 h-2 rounded-full flex-shrink-0"
            :class="codStatus.vpn_connected
              ? 'bg-green-400'
              : codStatus.rule_match
                ? 'bg-yellow-400 animate-pulse'
                : 'bg-gray-400'"
          />
          <div class="flex-1 min-w-0">
            <div class="text-[11px] font-semibold text-gray-700 dark:text-gray-200">On-demand</div>
            <div class="text-[10px] text-gray-500 dark:text-gray-400 truncate">
              {{ codDescription }}
            </div>
          </div>
          <router-link to="/settings" class="text-[10px] text-primary-400 hover:text-primary-300 flex-shrink-0">
            Edit
          </router-link>
        </div>
      </div>

      <!-- Connect Button — animations match Android ConnectScreen:
           * Disconnected: neutral background, outlined button, no motion.
           * Connecting: subtle scale pulse on the button, large ring
             progress spinner replaces the icon.
           * Connected: outer pulse-ring + soft glow, gradient-filled
             button, icon fades in over 300ms. -->
      <div class="mt-4 mb-5 relative">
        <div class="w-40 h-40 rounded-full flex items-center justify-center relative"
          :class="isSinkhole ? 'bg-red-500/5' : (isConnected ? 'bg-primary-500/5' : 'bg-gray-100 dark:bg-gray-800/50')">
          <!-- Glow ring, visible when the tunnel is up OR sinkhole is
               engaged (red halo signals "blocked"). -->
          <div v-if="isConnected || isSinkhole" class="absolute inset-[-12px] rounded-full pointer-events-none"
               :class="isSinkhole ? 'sinkhole-glow' : 'connect-glow'"></div>
          <!-- Outer pulse, subtle. -->
          <div v-if="isConnected || isSinkhole" class="absolute inset-0 rounded-full border-2 pulse-ring pointer-events-none"
               :class="isSinkhole ? 'border-red-500/40' : 'border-primary-500/30'"></div>
          <button @click="toggleConnection" :disabled="vpn.loading && !isSinkhole"
            class="w-32 h-32 rounded-full flex flex-col items-center justify-center transition-all duration-300 focus:outline-none relative"
            :class="[
              isSinkhole
                ? 'bg-gradient-to-br from-red-500 to-red-700 shadow-lg shadow-red-600/30 hover:shadow-red-600/50'
                : (isConnected
                  ? 'bg-gradient-to-br from-primary-500 to-primary-600 shadow-lg shadow-primary-500/25 hover:shadow-primary-500/40'
                  : 'bg-white dark:bg-gray-800 border-2 border-gray-300 dark:border-gray-600 hover:border-gray-400 dark:hover:border-gray-500'),
              vpn.loading && !isSinkhole ? 'connect-pulse' : ''
            ]">
            <!-- Sinkhole: red shield-with-X to signal "blocked, manual
                 intervention required". Wins over every other state. -->
            <ShieldExclamationIcon v-if="isSinkhole" class="w-12 h-12 text-white" />
            <!-- Connecting: large SVG progress ring. -->
            <svg v-else-if="vpn.loading" class="w-10 h-10 animate-spin" viewBox="0 0 48 48" fill="none" stroke-width="4">
              <circle cx="24" cy="24" r="20" stroke="rgba(255,255,255,0.15)" />
              <path d="M 24 4 a 20 20 0 0 1 20 20" stroke="white" stroke-linecap="round" />
            </svg>
            <!-- Protocol logo (WireGuard/OpenVPN/IPSec brand icon) when a
                 protocol is active. When connected over a solid colored
                 background, tint the icon white so the brand-color doesn't
                 clash with the button's gradient. -->
            <ProtocolIcon
              v-else-if="vpn.status?.active_protocol"
              :protocol="vpn.status.active_protocol"
              size="3xl"
              :tint="isConnected ? 'white' : undefined"
              class="transition-all duration-300"
            />
            <ShieldCheckIcon v-else class="w-12 h-12 transition-all duration-300"
              :class="isConnected ? 'text-white' : 'text-gray-500'" />
            <span class="text-[11px] font-semibold mt-1.5 transition-colors duration-300"
                  :class="isSinkhole ? 'text-white' : (isConnected ? 'text-white/90' : 'text-gray-500 dark:text-gray-400')">
              {{ connectionLabel }}
            </span>
          </button>
        </div>
      </div>

      <!-- Uptime + Pause control -->
      <div v-if="isConnected && vpn.status?.uptime" class="mb-3 flex items-center gap-2">
        <span class="text-lg font-mono text-gray-900 dark:text-white">{{ vpn.status.uptime }}</span>
        <!-- Pause dropdown - explicit, prominent button so users can
             find it. Equivalent of Android ConnectScreen long-press,
             but desktop wants the affordance visible rather than
             hidden behind a long-press gesture. Hidden while a pause
             is active (the blue Resume-now banner above replaces it). -->
        <div class="relative" v-if="!isPaused">
          <button @click.stop="showPauseMenu = !showPauseMenu"
            class="flex items-center gap-1 px-2.5 py-1 rounded-full text-[11px] font-medium bg-blue-500/15 text-blue-600 dark:text-blue-300 ring-1 ring-blue-500/30 hover:bg-blue-500/25 hover:ring-blue-500/50 transition-colors"
            :class="showPauseMenu ? 'bg-blue-500/25 ring-blue-500/50' : ''"
            title="Pause VPN for a fixed duration">
            <PauseIcon class="w-3.5 h-3.5" />
            <span>Pause</span>
          </button>
          <div v-if="showPauseMenu"
               @click.stop
               class="absolute right-0 top-full mt-1 w-36 card p-1 shadow-lg z-10 text-xs">
            <button @click="applyPause(60)" class="w-full text-left px-2 py-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700/50">1 minute</button>
            <button @click="applyPause(3*60)" class="w-full text-left px-2 py-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700/50">3 minutes</button>
            <button @click="applyPause(5*60)" class="w-full text-left px-2 py-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700/50">5 minutes</button>
            <button @click="applyPause(15*60)" class="w-full text-left px-2 py-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700/50">15 minutes</button>
            <button @click="applyPause(60*60)" class="w-full text-left px-2 py-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700/50">1 hour</button>
            <button @click="applyPause(4*60*60)" class="w-full text-left px-2 py-1.5 rounded hover:bg-gray-200 dark:hover:bg-gray-700/50">4 hours</button>
          </div>
        </div>
      </div>

      <!-- Connection / Pool Name — click to switch between any saved
           connection or pool. The picker is unified: pools render
           with the stack-icon at the top, singles below. Picking a
           pool calls ActivatePool (clears single's activeID); picking
           a single calls SwitchActiveConnection (clears active pool). -->
      <div v-if="activeName" class="mb-2 relative">
        <button
          @click.stop="showConnectionPicker = !showConnectionPicker"
          class="flex items-center gap-1 text-sm font-medium text-gray-600 dark:text-gray-300 hover:text-primary-400 transition-colors"
        >
          {{ activeName }}
          <ChevronDownIcon class="w-3.5 h-3.5" :class="showConnectionPicker ? 'rotate-180' : ''" />
        </button>
        <!-- Unified dropdown -->
        <div
          v-if="showConnectionPicker && pickerEntries.length > 1"
          @click.stop
          class="absolute left-1/2 -translate-x-1/2 mt-1 w-64 card p-1 shadow-lg z-10"
        >
          <button
            v-for="entry in pickerEntries"
            :key="entry.type + ':' + entry.id"
            @click="pickEntry(entry)"
            class="w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left text-xs transition-colors"
            :class="entry.isActive
              ? 'bg-primary-500/10 text-primary-300'
              : 'text-gray-500 dark:text-gray-400 hover:bg-gray-200 dark:hover:bg-gray-700/50 hover:text-gray-900 dark:hover:text-white'"
          >
            <div class="w-1.5 h-1.5 rounded-full flex-shrink-0"
              :class="entry.isActive ? 'bg-primary-400' : 'bg-gray-600'">
            </div>
            <RectangleStackIcon v-if="entry.type === 'pool'" class="w-3.5 h-3.5 text-primary-400 flex-shrink-0" />
            <span class="truncate flex-1">{{ entry.name }}</span>
            <span class="ml-auto text-[9px] text-gray-600">
              {{ entry.subtitle }}
            </span>
          </button>
        </div>
      </div>

      <!-- Protocol Switcher with proper protocol icons and colors -->
      <div class="flex items-center gap-1.5 mb-4">
        <button
          v-for="proto in connectionProtocols"
          :key="proto"
          @click="switchProtocol(proto)"
          :disabled="vpn.loading"
          class="flex items-center gap-1 px-2.5 py-1 rounded-full text-[11px] font-medium transition-all"
          :class="vpn.status?.active_protocol === proto
            ? protocolBadgeActive(proto)
            : 'bg-gray-200 dark:bg-gray-700/50 text-gray-500 hover:text-gray-700 dark:hover:text-gray-300'"
        >
          <ProtocolIcon :protocol="proto" size="xs" />
          {{ protocolLabel(proto) }}
        </button>
        <router-link
          :to="{ path: '/add', query: { connectionId: vpn.status?.connection_id } }"
          class="px-2 py-1 rounded-full text-[11px] text-gray-600 hover:text-gray-500 dark:hover:text-gray-400 bg-gray-100 dark:bg-gray-800/30 hover:bg-gray-200 dark:hover:bg-gray-700/50 transition-all"
          title="Add another protocol config"
        >
          +
        </router-link>

        <!-- Switch member: visible only when an active Pool is driving
             the connection. Opens a picker that overrides the policy's
             current pick - useful when the user wants a specific
             country / region right now without changing the policy. -->
        <button
          v-if="poolStore.activePoolId && isConnected"
          @click="showMemberPicker = true"
          class="ml-auto px-2 py-1 rounded-full text-[11px] text-primary-400 hover:text-primary-300 bg-primary-500/10 hover:bg-primary-500/20 transition-all flex items-center gap-1"
          title="Switch to a different pool member"
        >
          <ArrowPathIcon class="w-3 h-3" />
          Switch member
        </button>
      </div>

      <!-- Server address with country flag (left) and city + country
           label (right). The flag is a flag-icons CSS rectangle keyed
           by ISO 3166-1 alpha-2 (vpn.status.server_country_code).
           City comes from the Mullvad-style hostname pattern; both
           degrade silently to no-flag / no-city for non-standard
           configs. -->
      <div v-if="vpn.status?.server_address" class="mb-4 flex items-center justify-center gap-2 flex-wrap px-3">
        <span
          v-if="vpn.status.server_country_code"
          :class="`fi fi-${vpn.status.server_country_code.toLowerCase()}`"
          class="rounded-sm shadow-sm flex-shrink-0"
          style="width: 1.25rem; height: 0.875rem;"
        />
        <span class="text-xs text-gray-500 truncate font-mono">
          {{ vpn.status.server_address }}
        </span>
        <span v-if="serverLocation" class="text-[10px] text-gray-500 dark:text-gray-400 truncate">
          · {{ serverLocation }}
        </span>
      </div>

      <!-- Transfer Stats with live sparkline. The sparkline sits
           directly inside each mini-card behind the byte totals and
           the current-speed label. No axes, no tooltips — rudimentary
           by design. Colour matches the arrow icon (green for
           download, blue for upload) so the two channels stay visually
           distinct. -->
      <div v-if="isConnected" class="w-full max-w-sm grid grid-cols-2 gap-3 mb-4">
        <div class="card p-3 text-center">
          <div class="flex items-center justify-center gap-1 mb-1">
            <ArrowDownTrayIcon class="w-3 h-3 text-green-400" />
            <span class="text-[10px] text-gray-500">Download</span>
          </div>
          <span class="text-base font-semibold text-gray-900 dark:text-white">{{ formatBytes(vpn.status?.bytes_rx) }}</span>
          <div class="text-[10px] text-gray-500 dark:text-gray-400 mb-1">{{ formatSpeed(latestRxSpeed) }}</div>
          <SpeedSparkline :data="vpn.rxSpeedHistory" color="#4ade80" />
        </div>
        <div class="card p-3 text-center">
          <div class="flex items-center justify-center gap-1 mb-1">
            <ArrowUpTrayIcon class="w-3 h-3 text-blue-400" />
            <span class="text-[10px] text-gray-500">Upload</span>
          </div>
          <span class="text-base font-semibold text-gray-900 dark:text-white">{{ formatBytes(vpn.status?.bytes_tx) }}</span>
          <div class="text-[10px] text-gray-500 dark:text-gray-400 mb-1">{{ formatSpeed(latestTxSpeed) }}</div>
          <SpeedSparkline :data="vpn.txSpeedHistory" color="#60a5fa" />
        </div>
      </div>

      <!-- Connection Details -->
      <div class="w-full max-w-sm space-y-1.5">
        <div v-if="vpn.status?.local_address" class="flex justify-between items-center py-1.5 px-3 bg-white dark:bg-gray-800 rounded-lg">
          <span class="text-[11px] text-gray-500">VPN IP</span>
          <span class="text-[11px] text-gray-600 dark:text-gray-300 font-mono">{{ vpn.status.local_address }}</span>
        </div>
        <div v-if="vpn.status?.server_address" class="flex justify-between items-center py-1.5 px-3 bg-white dark:bg-gray-800 rounded-lg">
          <span class="text-[11px] text-gray-500">Endpoint</span>
          <span class="text-[11px] text-gray-600 dark:text-gray-300 font-mono truncate max-w-[180px]">{{ vpn.status.server_address }}</span>
        </div>
        <div v-if="vpn.status?.last_handshake" class="flex justify-between items-center py-1.5 px-3 bg-white dark:bg-gray-800 rounded-lg">
          <span class="text-[11px] text-gray-500">Handshake</span>
          <span class="text-[11px] text-gray-600 dark:text-gray-300">{{ vpn.status.last_handshake }}</span>
        </div>
        <div v-if="vpn.status?.kill_switch_enabled" class="flex justify-between items-center py-1.5 px-3 bg-white dark:bg-gray-800 rounded-lg">
          <span class="text-[11px] text-gray-500">Kill Switch</span>
          <span class="text-[11px] text-green-400">Active</span>
        </div>
      </div>

      <!-- Edit Config Button -->
      <div class="w-full max-w-sm mt-3">
        <button
          @click="openConfigEditor"
          :disabled="!vpn.status?.connection_name"
          class="w-full flex items-center justify-center gap-1.5 py-2 px-3 rounded-lg text-[11px] font-medium text-gray-500 dark:text-gray-400 bg-white dark:bg-gray-800 hover:text-primary-400 hover:bg-gray-50 dark:hover:bg-gray-700/50 transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
        >
          <PencilSquareIcon class="w-3.5 h-3.5" />
          Edit Config
        </button>
      </div>

      <!-- Error -->
      <p v-if="vpn.error" class="mt-3 text-xs text-red-400 text-center max-w-sm">{{ vpn.error }}</p>
    </template>

    <!-- COD-Mismatch Confirm Modal -->
    <!-- Shown when user clicks Connect while COD is enabled but the
         current network does not match any rule. COD's NetworkMonitor
         will tear the tunnel down within ~1s of it coming up, so we
         pre-empt with a clear info + Cancel/Connect-anyway choice. -->
    <div v-if="showCodMismatchModal" class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4" @click.self="cancelCodMismatchConnect">
      <div class="bg-white dark:bg-gray-900 rounded-xl shadow-2xl w-full max-w-md">
        <div class="flex items-start gap-3 px-5 pt-5 pb-3">
          <div class="flex-shrink-0 mt-0.5">
            <ShieldExclamationIcon class="w-6 h-6 text-amber-500" />
          </div>
          <div class="flex-1">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
              Connect on Demand will disconnect
            </h3>
            <p class="mt-1.5 text-xs text-gray-600 dark:text-gray-400 leading-relaxed">
              Connect on Demand is enabled but no rule matches the current network ({{ codCurrentNetworkLabel }}). If you connect now, Connect on Demand will tear the tunnel down again within seconds.
            </p>
            <p class="mt-2 text-xs text-gray-500 dark:text-gray-500 leading-relaxed">
              To keep this connection, either disable Connect on Demand in Settings or add a rule for this network.
            </p>
          </div>
        </div>
        <div class="flex items-center justify-end gap-2 px-5 py-3 border-t border-gray-200 dark:border-gray-700">
          <button
            @click="cancelCodMismatchConnect"
            class="px-3 py-1.5 text-[11px] font-medium text-gray-700 dark:text-gray-300 hover:bg-gray-100 dark:hover:bg-gray-800 rounded-md"
          >
            Cancel
          </button>
          <button
            @click="confirmCodMismatchConnect"
            class="px-3 py-1.5 text-[11px] font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md"
          >
            Connect anyway
          </button>
        </div>
      </div>
    </div>

    <!-- Pool Member Picker Modal -->
    <div
      v-if="showMemberPicker"
      class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4"
      @click.self="showMemberPicker = false"
    >
      <div class="bg-white dark:bg-gray-900 rounded-xl shadow-2xl w-full max-w-md max-h-[70vh] flex flex-col">
        <div class="flex items-center justify-between px-4 py-3 border-b border-gray-200 dark:border-gray-700">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            Switch pool member
          </h3>
          <button @click="showMemberPicker = false" class="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300">
            <XMarkIcon class="w-4 h-4" />
          </button>
        </div>
        <div class="px-4 py-2 border-b border-gray-200 dark:border-gray-700">
          <input
            v-model="memberPickerFilter"
            type="text"
            placeholder="Search by name, country, or region..."
            class="w-full bg-gray-50 dark:bg-gray-800 px-3 py-1.5 rounded text-xs border border-gray-200 dark:border-gray-700 focus:outline-none focus:ring-1 focus:ring-primary-500"
          />
        </div>
        <div class="flex-1 overflow-y-auto px-2 py-1">
          <button
            v-for="m in filteredPickerMembers"
            :key="m.id"
            @click="pickMember(m)"
            :disabled="memberSwitchInFlight"
            class="w-full flex items-center justify-between px-3 py-2 rounded hover:bg-gray-100 dark:hover:bg-gray-700/50 disabled:opacity-50 text-left"
          >
            <div class="min-w-0">
              <div class="text-xs text-gray-700 dark:text-gray-300 truncate">{{ m.name }}</div>
              <div class="text-[10px] text-gray-500">
                {{ m.country || 'unknown' }} · {{ m.region || 'Other' }}
                <span v-if="m.unreachable" class="text-amber-400 ml-1">• unreachable</span>
              </div>
            </div>
            <span v-if="m.id === activePickerMemberId" class="text-[10px] text-primary-400 flex-shrink-0">Current</span>
          </button>
          <div v-if="filteredPickerMembers.length === 0" class="text-center text-[10px] text-gray-500 italic py-4">
            No matches.
          </div>
        </div>
        <div v-if="memberSwitchError" class="px-4 py-2 text-[10px] text-red-400 border-t border-gray-200 dark:border-gray-700">
          {{ memberSwitchError }}
        </div>
      </div>
    </div>

    <!-- Config Editor Modal -->
    <div v-if="showConfigEditor" class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4" @click.self="showConfigEditor = false">
      <div class="bg-white dark:bg-gray-900 rounded-xl shadow-2xl w-full max-w-lg max-h-[80vh] flex flex-col">
        <div class="flex items-center justify-between px-4 py-3 border-b border-gray-200 dark:border-gray-700">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            Edit {{ protocolLabel(vpn.status?.active_protocol || '') }} Config
          </h3>
          <button @click="showConfigEditor = false" class="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300">
            <XMarkIcon class="w-4 h-4" />
          </button>
        </div>
        <div class="flex-1 overflow-hidden p-3">
          <textarea
            v-model="configEditorContent"
            spellcheck="false"
            class="w-full h-full min-h-[300px] bg-gray-50 dark:bg-gray-800 text-gray-800 dark:text-gray-200 text-xs font-mono p-3 rounded-lg border border-gray-200 dark:border-gray-700 focus:outline-none focus:ring-2 focus:ring-primary-500 resize-none"
          />
        </div>
        <div class="flex items-center justify-between px-4 py-3 border-t border-gray-200 dark:border-gray-700">
          <span v-if="configSaveStatus" class="text-[10px]" :class="configSaveStatus === 'saved' ? 'text-green-400' : 'text-red-400'">
            {{ configSaveStatus === 'saved' ? 'Saved and applied' : configSaveStatus }}
          </span>
          <span v-else class="text-[10px] text-gray-500">{{ configEditorContent.length }} bytes</span>
          <div class="flex gap-2">
            <button @click="showConfigEditor = false" class="px-3 py-1.5 text-[11px] text-gray-500 hover:text-gray-700 dark:hover:text-gray-300">Cancel</button>
            <button @click="saveConfig" :disabled="configSaving" class="px-3 py-1.5 text-[11px] font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md disabled:opacity-50">
              {{ configSaving ? 'Saving...' : 'Save' }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useVpnStore, formatSpeed } from '@/stores/vpn'
import { usePoolStore, formatDuration } from '@/stores/pool'
import { SelectProtocol, ListConnections, SwitchActiveConnection, GetActiveConfigContent, SaveActiveConfigContent, GetConnectOnDemandStatus, PauseFor, CancelPause } from '../../wailsjs/go/main/App'
import ProtocolIcon from '@/components/ProtocolIcon.vue'
import SpeedSparkline from '@/components/SpeedSparkline.vue'
import {
  ShieldCheckIcon,
  ShieldExclamationIcon,
  ArrowPathIcon,
  ArrowDownTrayIcon,
  ArrowUpTrayIcon,
  ChevronDownIcon,
  PencilSquareIcon,
  PauseIcon,
  XMarkIcon,
  RectangleStackIcon,
} from '@heroicons/vue/24/outline'

const vpn = useVpnStore()
const poolStore = usePoolStore()

// Pool indicator computed: only render once the store has the active
// pool's name. The list refresh is fast enough that we render
// "Pool · Loading..." for at most one frame.
const activePoolName = computed(() => {
  const p = poolStore.pools.find(x => x.id === poolStore.activePoolId)
  return p?.name || ''
})

const activeMemberDisplay = computed(() => {
  const p = poolStore.pools.find(x => x.id === poolStore.activePoolId)
  if (!p?.active_member_name) return ''
  if (p.active_member_cc) return `${p.active_member_name} (${p.active_member_cc})`
  return p.active_member_name
})

// Pool indicator data. activePoolPolicyShort surfaces "Round-Robin",
// "Geo-Nearest" or "Random" right next to the pool name so the user
// always knows what's driving server selection. The countdown row
// only appears for Round-Robin (the only policy with a rotation
// timer). All other policies pick once per Connect.
function formatClockTime(ns: number): string {
  if (!ns || ns <= 0) return ''
  const future = new Date(Date.now() + ns / 1_000_000)
  const hh = future.getHours().toString().padStart(2, '0')
  const mm = future.getMinutes().toString().padStart(2, '0')
  return `${hh}:${mm}`
}

const activePoolPolicyShort = computed(() => {
  const p = poolStore.pools.find((x) => x.id === poolStore.activePoolId)
  switch (p?.policy) {
    case 'geo-nearest':         return 'Geo-Nearest'
    case 'random':              return 'Random'
    case 'round-robin-region':  return 'Round-Robin'
    default:                    return ''
  }
})

const rotatorActive = computed(() => {
  const r = poolStore.rotatorStatus
  return !!(r && r.active)
})

const rotatorIdleBlocked = computed(() => {
  const r = poolStore.rotatorStatus
  return !!(r && r.active && r.idle_blocked)
})

const rotatorCountdown = computed(() => {
  const r = poolStore.rotatorStatus
  if (!r || !r.active) return ''
  if (r.idle_blocked) return formatDuration(r.force_rotate_in || 0)
  return formatDuration(r.next_rotation_in)
})

const rotatorPrimaryLabel = computed(() => {
  const r = poolStore.rotatorStatus
  if (!r || !r.active) return ''
  if (r.idle_blocked) return 'Force-rotate in'
  return 'Next server in'
})

const rotatorAtLine = computed(() => {
  const r = poolStore.rotatorStatus
  if (!r || !r.active) return ''
  if (r.idle_blocked) return `at ${formatClockTime(r.force_rotate_in || 0)}`
  return `at ${formatClockTime(r.next_rotation_in)}`
})

const rotatorIntervalLabel = computed(() => {
  const r = poolStore.rotatorStatus
  if (!r || !r.active) return ''
  return `${r.interval_min} min cycle`
})

// Server location: "Milan, Italy" / "Frankfurt, Germany" / "Italy"
// (when only country is known) / "" (unknown - flag and label both
// hidden). Country codes from MMDB lookup (backend resolveServerCountry);
// city from Mullvad-style hostname pattern (backend resolveServerCity).
const serverLocation = computed(() => {
  const cc = vpn.status?.server_country_code || ''
  const city = vpn.status?.server_city || ''
  const countryName = cc ? COUNTRY_NAMES[cc.toUpperCase()] || cc : ''
  if (city && countryName) return `${city}, ${countryName}`
  if (countryName) return countryName
  if (city) return city
  return ''
})

// ISO 3166-1 alpha-2 → English country name. Inline because it's a
// small lookup table; loading a separate locale file would be
// disproportionate. Covers every country a major commercial VPN
// provider has servers in plus the long tail.
const COUNTRY_NAMES: Record<string, string> = {
  AT: 'Austria',     BE: 'Belgium',     BG: 'Bulgaria',    CH: 'Switzerland',
  CY: 'Cyprus',      CZ: 'Czechia',     DE: 'Germany',     DK: 'Denmark',
  EE: 'Estonia',     ES: 'Spain',       FI: 'Finland',     FR: 'France',
  GB: 'UK',          GR: 'Greece',      HR: 'Croatia',     HU: 'Hungary',
  IE: 'Ireland',     IS: 'Iceland',     IT: 'Italy',       LI: 'Liechtenstein',
  LT: 'Lithuania',   LU: 'Luxembourg',  LV: 'Latvia',      MD: 'Moldova',
  MT: 'Malta',       NL: 'Netherlands', NO: 'Norway',      PL: 'Poland',
  PT: 'Portugal',    RO: 'Romania',     SE: 'Sweden',      SI: 'Slovenia',
  SK: 'Slovakia',    UA: 'Ukraine',     RS: 'Serbia',      BA: 'Bosnia',
  AL: 'Albania',     MK: 'N. Macedonia',ME: 'Montenegro',  XK: 'Kosovo',
  BY: 'Belarus',     RU: 'Russia',
  US: 'USA',         CA: 'Canada',      MX: 'Mexico',
  BR: 'Brazil',      AR: 'Argentina',   CL: 'Chile',       CO: 'Colombia',
  PE: 'Peru',        VE: 'Venezuela',   EC: 'Ecuador',     UY: 'Uruguay',
  JP: 'Japan',       KR: 'South Korea', CN: 'China',       TW: 'Taiwan',
  HK: 'Hong Kong',   SG: 'Singapore',   MY: 'Malaysia',    TH: 'Thailand',
  VN: 'Vietnam',     PH: 'Philippines', ID: 'Indonesia',   IN: 'India',
  PK: 'Pakistan',    BD: 'Bangladesh',  LK: 'Sri Lanka',   NP: 'Nepal',
  KH: 'Cambodia',    KZ: 'Kazakhstan',  UZ: 'Uzbekistan',  GE: 'Georgia',
  AM: 'Armenia',     AZ: 'Azerbaijan',
  AU: 'Australia',   NZ: 'New Zealand', FJ: 'Fiji',
  ZA: 'South Africa',NG: 'Nigeria',     EG: 'Egypt',       KE: 'Kenya',
  MA: 'Morocco',     DZ: 'Algeria',     TN: 'Tunisia',     GH: 'Ghana',
  ET: 'Ethiopia',    TZ: 'Tanzania',    UG: 'Uganda',
  AE: 'UAE',         SA: 'Saudi Arabia',IL: 'Israel',      TR: 'Turkey',
  IR: 'Iran',        IQ: 'Iraq',        JO: 'Jordan',      LB: 'Lebanon',
  QA: 'Qatar',       KW: 'Kuwait',      OM: 'Oman',        BH: 'Bahrain',
}
const showConnectionPicker = ref(false)
const allConnections = ref<any[]>([])

// Hardcore Kill Switch: when state is SINKHOLE the OS firewall is
// actively blocking everything outside the (failed) tunnel. Connect
// button must visually mirror that and refuse user-initiated connect
// attempts - the only way out is the user toggling KS off in
// Settings, mirroring the Android v0.9.10.6 hardcore lock.
const isSinkhole = computed(() => vpn.status?.kill_switch_state === 'SINKHOLE')

// Pause UI: backend exposes pause_remaining_sec on the live status,
// counts down naturally because the backend updates it on every
// status push (~2s).
const pauseRemaining = computed(() => Number(vpn.status?.pause_remaining_sec || 0))
const isPaused = computed(() => pauseRemaining.value > 0)
const pauseLabel = computed(() => {
  const total = pauseRemaining.value
  if (total <= 0) return ''
  const m = Math.floor(total / 60)
  const s = total % 60
  return `${m}:${String(s).padStart(2, '0')}`
})

// Transient inline notice (toast). 4s auto-clear is enough for the
// user to read without lingering. Set via showNotice('text').
const notice = ref('')
let noticeTimer: ReturnType<typeof setTimeout> | null = null
function showNotice(msg: string) {
  notice.value = msg
  if (noticeTimer) clearTimeout(noticeTimer)
  noticeTimer = setTimeout(() => {
    notice.value = ''
    noticeTimer = null
  }, 4000)
}

// Pause-duration menu state. Click on Pause button opens the small
// dropdown with quick-pick durations.
const showPauseMenu = ref(false)
async function applyPause(seconds: number) {
  showPauseMenu.value = false
  try {
    await PauseFor(seconds)
    await vpn.fetchStatus()
  } catch (e: any) {
    showNotice('Pause failed: ' + (e?.message || e))
  }
}
async function resumeNow() {
  try {
    await CancelPause()
    await vpn.fetchStatus()
  } catch (e: any) {
    showNotice('Resume failed: ' + (e?.message || e))
  }
}

// Config editor state
const showConfigEditor = ref(false)
const configEditorContent = ref('')
const configSaving = ref(false)
const configSaveStatus = ref('')

async function openConfigEditor() {
  try {
    configEditorContent.value = await GetActiveConfigContent()
    configSaveStatus.value = ''
    showConfigEditor.value = true
  } catch (e: any) {
    vpn.error = 'Failed to load config: ' + (e?.message || e)
  }
}

async function saveConfig() {
  configSaving.value = true
  configSaveStatus.value = ''
  try {
    await SaveActiveConfigContent(configEditorContent.value)
    configSaveStatus.value = 'saved'
    setTimeout(() => { showConfigEditor.value = false }, 1000)
    // Refresh status after reconnect
    setTimeout(() => { vpn.fetchStatus() }, 3000)
  } catch (e: any) {
    configSaveStatus.value = e?.message || 'Save failed'
  } finally {
    configSaving.value = false
  }
}

async function loadConnections() {
  try {
    allConnections.value = await ListConnections() || []
  } catch {
    allConnections.value = []
  }
}

async function pickConnection(conn: any) {
  showConnectionPicker.value = false
  if (conn.id === vpn.status?.connection_id && !poolStore.activePoolId) return
  try {
    const proto = conn.active_protocol || conn.protocols?.[0]?.protocol || ''
    // SwitchActiveConnection returns true iff a reconnect will be
    // attempted (because tunnel was up, or COD says it should be).
    // When KS is also armed in that case the disconnect engages
    // forceSinkhole and the upcoming reconnect is refused by the
    // hardcore-lock guard - surface a toast so the user knows.
    const willReconnect = await SwitchActiveConnection(conn.id, proto)
    const ksState = vpn.status?.kill_switch_state || 'IDLE'
    if (willReconnect && (ksState === 'ARMED' || ksState === 'SINKHOLE')) {
      showNotice('Kill Switch active. This will block your reconnect!')
    }
    // Parallelise the three post-switch refresh calls - sequential
    // form added ~150 ms perceived lag when switching pool ↔ single.
    await Promise.all([vpn.fetchStatus(), loadConnections(), poolStore.refresh()])
  } catch (e: any) {
    vpn.error = 'Failed to switch connection'
  }
}

// Unified picker entry shape - merges singles and pools so the
// dropdown can render them in one list. The data flow:
//   - singles come from allConnections (loaded via ListConnections)
//   - pools come from poolStore.pools (loaded via ListPools)
// Active state is derived locally rather than trusting either list's
// is_active flag exclusively, since the two layers refresh at
// different cadences.
interface PickerEntry {
  type: 'single' | 'pool'
  id: string
  name: string
  subtitle: string
  isActive: boolean
}

const pickerEntries = computed<PickerEntry[]>(() => {
  const activePoolId = poolStore.activePoolId
  const activeSingleId = vpn.status?.connection_id || ''

  const pools: PickerEntry[] = poolStore.pools.map((p) => ({
    type: 'pool',
    id: p.id,
    name: p.name,
    subtitle: `${p.member_count}`,
    isActive: !!activePoolId && p.id === activePoolId,
  }))
  const singles: PickerEntry[] = allConnections.value.map((c) => ({
    type: 'single',
    id: c.id,
    name: c.name,
    subtitle: c.protocols?.map((p: any) => protocolShort(p.protocol)).join('/') || '',
    // Singles are only "active" if no pool is active (mutual exclusion).
    isActive: !activePoolId && c.id === activeSingleId,
  }))
  // Pools above singles - matches the ConnectionsView layout so the
  // user's mental map is consistent across screens.
  return [...pools, ...singles]
})

const activeName = computed(() => {
  if (poolStore.activePoolId) {
    const p = poolStore.pools.find((x) => x.id === poolStore.activePoolId)
    return p?.name || ''
  }
  return vpn.status?.connection_name || ''
})

async function pickEntry(entry: PickerEntry) {
  showConnectionPicker.value = false
  if (entry.isActive) return
  try {
    if (entry.type === 'pool') {
      // poolStore.activate already calls ActivatePool then refresh().
      // The follow-up vpn.fetchStatus + loadConnections + the
      // refresh inside activate run in parallel rather than serially.
      await poolStore.activate(entry.id)
      await Promise.all([vpn.fetchStatus(), loadConnections()])
    } else {
      // Reuse pickConnection's path so KS-warning + reconnect logic
      // is centralised. Find the conn object for the protocol hint.
      const conn = allConnections.value.find((c) => c.id === entry.id)
      if (conn) await pickConnection(conn)
    }
  } catch (e: any) {
    vpn.error = e?.toString() || 'failed to switch'
  }
}

function protocolShort(proto: string): string {
  switch (proto) {
    case 'wireguard': return 'WG'
    case 'openvpn': return 'OVPN'
    case 'ipsec': return 'IPSec'
    default: return proto
  }
}

// Close picker when clicking outside
function onClickOutside(e: Event) {
  if (showConnectionPicker.value) {
    showConnectionPicker.value = false
  }
}

// Poll on-demand status so the banner reflects network changes without
// requiring the user to reopen Settings. 5s matches the Settings-view
// poll interval — same backend evaluator, so shorter cadence would just
// burn CPU for no new information.
const codStatus = ref<any>(null)
let codInterval: ReturnType<typeof setInterval> | null = null

async function pollCod() {
  try {
    codStatus.value = await GetConnectOnDemandStatus()
  } catch {
    codStatus.value = null
  }
}

// Compact human-readable description of the current on-demand state.
// Mirrors the Android ConnectScreen banner text so cross-device users
// see consistent wording.
const codDescription = computed(() => {
  const c = codStatus.value
  if (!c || !c.enabled) return ''
  if (c.vpn_connected) {
    return `VPN active for ${c.ssid || c.network_type || 'current network'}`
  }
  if (c.rule_match) {
    return `Rule matched — connecting to ${c.ssid || c.network_type || 'network'}...`
  }
  if (c.ssid) return `Watching ${c.ssid} (${c.network_type})`
  if (c.network_type && c.network_type !== 'none') return `Watching ${c.network_type}`
  return 'No network — idle'
})

// Pool rotator polls every 5s while a pool is active, mirroring the
// COD poll interval. The countdown UI updates by 1s tick on a separate
// timer so the user sees seconds advance even though we only refresh
// the underlying NextRotationIn value every 5s.
let rotatorInterval: ReturnType<typeof setInterval> | null = null
let rotatorCountdownInterval: ReturnType<typeof setInterval> | null = null

onMounted(() => {
  loadConnections()
  pollCod()
  codInterval = setInterval(pollCod, 5000)
  poolStore.refresh()
  rotatorInterval = setInterval(() => poolStore.pollRotator(), 5000)
  rotatorCountdownInterval = setInterval(() => {
    // Decrement the cached value by 1s so the UI counts smoothly between
    // polls. The next poll resets it to the authoritative value.
    const r = poolStore.rotatorStatus
    if (r && r.active && r.next_rotation_in > 1_000_000_000) {
      r.next_rotation_in -= 1_000_000_000
    }
    if (r && r.active && r.idle_blocked && (r.force_rotate_in || 0) > 1_000_000_000) {
      r.force_rotate_in = (r.force_rotate_in || 0) - 1_000_000_000
    }
  }, 1000)
  document.addEventListener('click', onClickOutside)
})
onUnmounted(() => {
  if (codInterval) {
    clearInterval(codInterval)
    codInterval = null
  }
  if (rotatorInterval) {
    clearInterval(rotatorInterval)
    rotatorInterval = null
  }
  if (rotatorCountdownInterval) {
    clearInterval(rotatorCountdownInterval)
    rotatorCountdownInterval = null
  }
  document.removeEventListener('click', onClickOutside)
})

// Robust connected state — use status from Go backend
const isConnected = computed(() => {
  return vpn.status?.connected === true
})

// Latest speed = last entry in the rolling buffer. Displayed above the
// sparkline so users see a concrete number plus the visual trend.
const latestRxSpeed = computed(() => {
  const h = vpn.rxSpeedHistory
  return h.length > 0 ? h[h.length - 1] : 0
})
const latestTxSpeed = computed(() => {
  const h = vpn.txSpeedHistory
  return h.length > 0 ? h[h.length - 1] : 0
})

const showWelcome = computed(() => {
  // Wait for the vpn status fetch to land - earlier we returned true
  // here, which flashed Welcome for ~500ms during initial fetch even
  // when the user has saved connections and pools. That window was
  // long enough to notice on first start.
  if (!vpn.status) return false
  if (isConnected.value) return false
  // An active pool is a virtual connection - the Connect button needs
  // to render so the user can fire PickAndConnectActivePool, even
  // though the singles' connection_name is empty (mutual exclusion).
  if (poolStore.activePoolId) return false
  if (vpn.status.connection_name) return false
  if (vpn.status.connection_protocols?.length > 0) return false
  // Welcome ONLY when we know there are no saved connections AND no
  // pools - otherwise the Connect-screen layout is the right thing
  // even if neither is "active" yet (auto-select-MRU on the backend
  // makes that case rare anyway).
  if (allConnections.value.length > 0) return false
  if (poolStore.pools.length > 0) return false
  return true
})

const connectionLabel = computed(() => {
  if (isSinkhole.value) return 'Kill Switch Active'
  if (vpn.loading) {
    return isConnected.value ? 'Disconnecting...' : 'Connecting...'
  }
  return isConnected.value ? 'Connected' : 'Connect'
})

const connectionProtocols = computed(() => {
  return vpn.status?.connection_protocols || []
})

// COD-mismatch confirm modal: shown when user clicks Connect while
// COD is enabled but the current network does not match any rule.
// COD would otherwise tear the tunnel down ~1s after handshake, leaving
// the user with a flicker and no explanation. The dialog explains the
// situation and offers a real out (Cancel) plus an explicit override
// (Connect anyway) for users who understand the consequence.
const showCodMismatchModal = ref(false)

const codCurrentNetworkLabel = computed(() => {
  const c = codStatus.value
  if (!c) return 'unknown'
  if (c.ssid) return c.ssid
  if (c.network_type && c.network_type !== 'none') return c.network_type
  return 'no network'
})

function shouldWarnCodMismatch(): boolean {
  const c = codStatus.value
  if (!c || !c.enabled) return false
  if (c.vpn_connected) return false
  // rule_match true = COD will sustain the connection; only warn when
  // it would actively tear it down.
  return !c.rule_match
}

function cancelCodMismatchConnect() {
  showCodMismatchModal.value = false
}

async function confirmCodMismatchConnect() {
  showCodMismatchModal.value = false
  await dispatchConnect()
}

// dispatchConnect picks the right Connect entry point based on whether
// a Pool is active. Pool-driven connects run PickAndConnectActivePool
// which resolves a member via the policy and calls Connect("") with
// the member's config staged. Single-connection users still go through
// the existing vpn.connect() path.
// Switch-Member picker state. Loads the full member list from
// PoolDetail on first open (the pools list endpoint only carries the
// active member name) and caches in a ref so re-opens are instant.
const showMemberPicker = ref(false)
const memberPickerFilter = ref('')
const memberPickerMembers = ref<any[]>([])
const memberSwitchInFlight = ref(false)
const memberSwitchError = ref('')

const activePickerMemberId = computed(() => {
  const p = poolStore.pools.find(x => x.id === poolStore.activePoolId)
  return p?.active_member_id || ''
})

const filteredPickerMembers = computed(() => {
  const q = memberPickerFilter.value.trim().toLowerCase()
  if (!q) return memberPickerMembers.value
  return memberPickerMembers.value.filter((m: any) =>
    m.name.toLowerCase().includes(q) ||
    (m.country || '').toLowerCase().includes(q) ||
    (m.region || '').toLowerCase().includes(q)
  )
})

watch(showMemberPicker, async (open: boolean) => {
  if (!open) return
  if (!poolStore.activePoolId) return
  memberPickerFilter.value = ''
  memberSwitchError.value = ''
  try {
    const detail: any = await poolStore.detail(poolStore.activePoolId)
    memberPickerMembers.value = detail?.pool?.members || []
  } catch (e: any) {
    memberSwitchError.value = e?.toString() || 'failed to load members'
  }
})

async function pickMember(m: any) {
  memberSwitchInFlight.value = true
  memberSwitchError.value = ''
  try {
    await poolStore.switchMember(m.id)
    showMemberPicker.value = false
  } catch (e: any) {
    memberSwitchError.value = e?.toString() || 'switch failed'
  } finally {
    memberSwitchInFlight.value = false
  }
}

async function dispatchConnect() {
  if (poolStore.activePoolId) {
    try {
      await poolStore.pickAndConnect()
    } catch (e: any) {
      vpn.error = e?.toString() || 'pool connect failed'
    }
  } else {
    await vpn.connect()
  }
}

async function toggleConnection() {
  // Hardcore Kill Switch: when sinkhole is engaged the connect-button
  // is rendered as a red shield indicating the lock state. Tapping it
  // does NOT fire a connect intent - the only release is the user
  // toggling KS off in Settings. We surface a toast so the affordance
  // is not just non-responsive.
  if (isSinkhole.value) {
    showNotice('Kill Switch is active. Toggle Kill Switch off in Settings to release.')
    return
  }
  if (isConnected.value) {
    await vpn.disconnect()
    return
  }
  if (shouldWarnCodMismatch()) {
    showCodMismatchModal.value = true
    return
  }
  await dispatchConnect()
}

async function switchProtocol(proto: string) {
  if (proto === vpn.status?.active_protocol) return
  try {
    await SelectProtocol(proto)
    if (vpn.status) {
      vpn.status.active_protocol = proto
    }
  } catch (e: any) {
    vpn.error = 'Failed to switch protocol'
  }
}

function protocolLabel(proto: string): string {
  switch (proto) {
    case 'wireguard': return 'WireGuard'
    case 'openvpn': return 'OpenVPN'
    case 'ipsec': return 'IPSec'
    default: return proto
  }
}

// Protocol badge colors — official brand colors
function protocolBadgeActive(proto: string): string {
  switch (proto) {
    case 'wireguard': return 'bg-red-900/20 text-red-300 ring-1 ring-red-500/30'       // WireGuard red #88171A
    case 'openvpn': return 'bg-orange-500/20 text-orange-300 ring-1 ring-orange-500/30' // OpenVPN orange #EA7E20
    case 'ipsec': return 'bg-blue-500/20 text-blue-300 ring-1 ring-blue-500/30'         // IPSec blue #2563eb
    default: return 'bg-gray-500/20 text-gray-600 dark:text-gray-300'
  }
}

function formatBytes(bytes: number | undefined): string {
  if (!bytes || bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return (bytes / Math.pow(1024, i)).toFixed(1) + ' ' + units[i]
}
</script>
