<template>
  <div class="p-4 overflow-y-auto max-h-[calc(100vh-7rem)]">
    <h2 class="text-sm font-semibold text-gray-600 dark:text-gray-300 mb-4">Settings</h2>

    <div class="space-y-3">
      <!-- Connection Settings -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Connection</h3>
        <div class="space-y-3">
          <div class="flex items-center justify-between">
            <div>
              <span class="text-sm text-gray-700 dark:text-gray-300">Kill Switch</span>
              <p class="text-[10px] text-gray-400 mt-0.5">
                Block traffic if VPN disconnects
              </p>
            </div>
            <button
              @click="toggleSetting('kill_switch_enabled')"
              class="toggle"
              :class="[
                settings.kill_switch_enabled && platform.kill_switch_supported ? 'toggle-enabled' : 'toggle-disabled',
                !platform.kill_switch_supported ? 'opacity-40 cursor-not-allowed' : ''
              ]"
              :disabled="!platform.kill_switch_supported"
            >
              <span class="toggle-knob" :class="settings.kill_switch_enabled && platform.kill_switch_supported ? 'translate-x-5' : 'translate-x-0'" />
            </button>
          </div>
          <div class="flex items-center justify-between">
            <span class="text-sm text-gray-700 dark:text-gray-300">Minimize to tray</span>
            <button
              @click="toggleSetting('minimize_to_tray')"
              class="toggle"
              :class="settings.minimize_to_tray ? 'toggle-enabled' : 'toggle-disabled'"
            >
              <span class="toggle-knob" :class="settings.minimize_to_tray ? 'translate-x-5' : 'translate-x-0'" />
            </button>
          </div>
          <div class="flex items-center justify-between">
            <div>
              <span class="text-sm text-gray-700 dark:text-gray-300">Start at login</span>
              <p class="text-[10px] text-gray-400 mt-0.5">Launch app when you log in</p>
            </div>
            <button
              @click="toggleSetting('autostart_enabled')"
              class="toggle"
              :class="[
                settings.autostart_enabled && platform.autostart_supported ? 'toggle-enabled' : 'toggle-disabled',
                !platform.autostart_supported ? 'opacity-40 cursor-not-allowed' : ''
              ]"
              :disabled="!platform.autostart_supported"
            >
              <span class="toggle-knob" :class="settings.autostart_enabled && platform.autostart_supported ? 'translate-x-5' : 'translate-x-0'" />
            </button>
          </div>
        </div>
      </div>

      <!-- Privycs Gateway -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Privycs Gateway</h3>
        <div class="space-y-3">
          <div>
            <label class="text-sm text-gray-700 dark:text-gray-300 block mb-1">Gateway URL</label>
            <input
              v-model="settings.gateway_url"
              @blur="saveSettings"
              type="url"
              placeholder="https://app.privycs.com"
              maxlength="255"
              class="input"
            />
          </div>
          <div>
            <label class="text-sm text-gray-700 dark:text-gray-300 block mb-1">API Key</label>
            <input
              v-model="settings.api_key"
              @blur="saveSettings"
              type="text"
              placeholder="pvcs_..."
              maxlength="100"
              autocomplete="off"
              autocorrect="off"
              autocapitalize="off"
              spellcheck="false"
              data-form-type="other"
              class="input font-mono [-webkit-text-security:disc]"
            />
          </div>
          <div class="flex items-center gap-2">
            <button
              @click="verifyApiKey"
              :disabled="verifying || !settings.gateway_url || !settings.api_key"
              class="btn-primary px-4 py-1.5 text-xs disabled:opacity-50"
            >
              {{ verifying ? 'Verifying...' : 'Verify & Sync' }}
            </button>
            <span v-if="apiStatus" class="text-[10px]" :class="apiStatus.ok ? 'text-green-400' : 'text-red-400'">
              {{ apiStatus.message }}
            </span>
          </div>
        </div>
      </div>

      <!-- Privileged Helper -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Privileged Helper</h3>
        <div class="space-y-3">
          <div class="flex items-center justify-between">
            <div>
              <span class="text-sm text-gray-700 dark:text-gray-300">Status</span>
              <p class="text-[10px] text-gray-400 mt-0.5">
                Eliminates password prompts when connecting VPN
              </p>
            </div>
            <div class="flex items-center gap-1.5">
              <span class="inline-block w-1.5 h-1.5 rounded-full"
                :class="helperStatus.running ? 'bg-green-400' : (helperStatus.installed ? 'bg-yellow-400' : 'bg-gray-400')"
              />
              <span class="text-xs text-gray-500 dark:text-gray-400">
                {{ helperStatus.running ? 'Running' : (helperStatus.installed ? 'Installed (not running)' : 'Not installed') }}
              </span>
            </div>
          </div>
          <div class="flex items-center gap-2">
            <button
              v-if="!helperStatus.running"
              @click="installHelper"
              :disabled="helperInstalling"
              class="btn-primary px-4 py-1.5 text-xs disabled:opacity-50"
            >
              {{ helperInstalling ? 'Installing...' : 'Install Helper' }}
            </button>
            <button
              v-if="helperStatus.installed"
              @click="uninstallHelper"
              :disabled="helperInstalling"
              class="btn-secondary px-4 py-1.5 text-xs disabled:opacity-50"
            >
              Uninstall
            </button>
          </div>
          <p v-if="helperError" class="text-[10px] text-red-400 bg-red-500/10 rounded-lg py-1.5 px-2">{{ helperError }}</p>
          <p class="text-[10px] text-gray-500 dark:text-gray-400">
            The helper runs as a system service and handles VPN operations without repeated admin prompts. Requires a one-time authorization to install.
          </p>
          <!-- macOS manual cleanup hint. The osascript-based install/uninstall
               UI is fragile on unsigned builds because Sequoia's TCC can
               terminate AppleEvents mid-flight (visible as "signal:
               terminated" errors). When the in-app buttons fail, these
               Terminal commands always work because sudo bypasses the TCC
               path entirely. Hidden by default to keep the UI tidy; click
               to expand. Linux/Windows users don't need this. -->
          <details v-if="isMacOS" class="text-[10px]">
            <summary class="text-gray-500 dark:text-gray-400 cursor-pointer hover:text-primary-400">
              Helper button stuck or failing? Show manual cleanup commands
            </summary>
            <pre class="mt-2 p-2 rounded bg-gray-900/50 text-gray-300 overflow-x-auto whitespace-pre">sudo launchctl bootout system /Library/LaunchDaemons/com.privycs.vpn-helper.plist 2&gt;/dev/null
sudo rm -f /Library/LaunchDaemons/com.privycs.vpn-helper.plist
sudo pkill -9 -f "privycs.*--helper" 2&gt;/dev/null</pre>
            <p class="mt-1 text-gray-500 dark:text-gray-400">
              Run these in Terminal, then reopen this window — Helper status will refresh and "Install Helper" becomes clickable again.
            </p>
          </details>
        </div>
      </div>

      <!-- Network -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Network</h3>
        <div class="space-y-3">
          <div>
            <label class="text-sm text-gray-700 dark:text-gray-300 block mb-1">DNS Override</label>
            <DnsOverrideField
              v-model="settings.dns_override"
              placeholder="e.g. 1.1.1.1, 2606:4700:4700::1111 — comma-separated, IPv4 + IPv6"
              @update:model-value="onDnsInput"
              @blur="validateAndSave"
            />
            <div class="flex items-center justify-between mt-1">
              <p class="text-[10px] flex-1" :class="dnsError ? 'text-red-400' : 'text-gray-500'">
                <template v-if="dnsError">{{ dnsError }}</template>
                <template v-else-if="dnsProviderHint">{{ dnsProviderHint }}</template>
                <template v-else>Multiple servers separated by comma. Empty = use server-pushed DNS.</template>
              </p>
              <button
                @click="testDns"
                :disabled="dnsTesting"
                class="text-[10px] text-primary-400 hover:text-primary-300 ml-2 disabled:opacity-50"
              >
                {{ dnsTesting ? 'Testing…' : 'Test DNS' }}
              </button>
            </div>
            <p v-if="dnsTestResult" class="text-[10px] text-gray-500 mt-0.5">
              <span v-if="dnsTestResult.error" class="text-red-400">Error: {{ dnsTestResult.error }}</span>
              <span v-else>
                Resolved {{ dnsTestResult.host }} → {{ dnsTestResult.addresses.join(', ') }} ({{ dnsTestResult.duration_ms }}ms)
                <span v-if="dnsTestResult.resolver_hint" class="text-primary-400 ml-1">via {{ dnsTestResult.resolver_hint }}</span>
              </span>
            </p>
          </div>
        </div>
      </div>

      <!-- Tunnel Health (Phase 1 visible UX) -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Tunnel Health</h3>
        <p class="text-[10px] text-gray-400 mb-3">
          Periodic ICMP ping to verify the tunnel is actually carrying traffic.
          Three failures fire recovery: pool member rotation or single-connection disconnect/reconnect.
        </p>
        <div class="space-y-2 mb-3">
          <label v-for="opt in [
            { value: 'auto', label: 'Auto (recovery for pool & single)' },
            { value: 'always', label: 'Always on' },
            { value: 'off', label: 'Off' },
          ]" :key="opt.value" class="flex items-center gap-2 text-sm cursor-pointer">
            <input
              type="radio"
              :value="opt.value"
              v-model="settings.tunnel_health_mode"
              @change="saveSettings()"
              class="accent-primary-600 h-4 w-4"
            />
            <span class="text-gray-700 dark:text-gray-300">{{ opt.label }}</span>
          </label>
        </div>
        <label class="text-[11px] text-gray-500 block mb-1">Ping target (optional)</label>
        <input
          v-model="settings.tunnel_health_target"
          @blur="saveSettings()"
          type="text"
          placeholder="default: 1.1.1.1"
          class="input"
        />
        <p class="text-[10px] text-gray-500 mt-1">
          Empty = use default 1.1.1.1. Useful if 1.1.1.1 is blocked or you prefer pinging an internal target.
        </p>

        <!-- v0.9.15.30: probe cadence overrides -->
        <div class="grid grid-cols-2 gap-3 mt-4">
          <div>
            <label class="text-[11px] text-gray-500 block mb-1">Ping interval (seconds)</label>
            <input
              v-model.number="settings.tunnel_health_ping_interval_sec"
              @blur="saveSettings()"
              type="number"
              min="1"
              max="120"
              placeholder="5"
              class="input"
            />
          </div>
          <div>
            <label class="text-[11px] text-gray-500 block mb-1">Fails before recovery</label>
            <input
              v-model.number="settings.tunnel_health_dead_threshold"
              @blur="saveSettings()"
              type="number"
              min="1"
              max="10"
              placeholder="2"
              class="input"
            />
          </div>
        </div>
        <p class="text-[10px] text-gray-500 mt-1">
          Max detection time = interval × threshold. Default 5 s × 2 = max 10 s. Larger values lower battery / data cost; smaller values catch dead tunnels faster but may false-trigger on flaky networks.
        </p>
      </div>

      <!-- Sleep / Wake Recovery (macOS) -->
      <div v-if="isMacOS" class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Sleep / Wake Recovery</h3>
        <p class="text-[10px] text-gray-400 mb-3">
          When macOS wakes from sleep, the upstream NAT mapping for the VPN has typically expired and the tunnel is silently dead — packets black-hole through stuck routes until manually disconnected. These options auto-recover.
        </p>
        <div class="flex items-center justify-between mb-3">
          <div class="flex-1 pr-3">
            <span class="text-sm text-gray-700 dark:text-gray-300">Reconnect on system wake</span>
            <p class="text-[10px] text-gray-500">Force-reconnect within ~1 s after wake instead of waiting for the tunnel-health probe (~60 s).</p>
          </div>
          <Switch
            :model-value="settings.reconnect_on_system_wake !== false"
            @update:model-value="(v: boolean) => { settings.reconnect_on_system_wake = v; saveSettings() }"
            :class="settings.reconnect_on_system_wake !== false ? 'toggle-enabled' : 'toggle-disabled'"
            class="toggle"
          >
            <span class="toggle-knob" :class="settings.reconnect_on_system_wake !== false ? 'translate-x-5' : 'translate-x-0'" />
          </Switch>
        </div>
        <div class="flex items-center justify-between">
          <div class="flex-1 pr-3">
            <span class="text-sm text-gray-700 dark:text-gray-300">Prevent display sleep while tunnel is up</span>
            <p class="text-[10px] text-gray-500">Uses macOS <code>caffeinate -di</code>. Trades battery for zero sleep-related VPN drops. Default off.</p>
          </div>
          <Switch
            :model-value="!!settings.prevent_display_sleep"
            @update:model-value="(v: boolean) => { settings.prevent_display_sleep = v; saveSettings() }"
            :class="settings.prevent_display_sleep ? 'toggle-enabled' : 'toggle-disabled'"
            class="toggle"
          >
            <span class="toggle-knob" :class="settings.prevent_display_sleep ? 'translate-x-5' : 'translate-x-0'" />
          </Switch>
        </div>
      </div>

      <!-- On-Demand & Network Rules (unified — v0.9.15.73) -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">On-Demand &amp; Network Rules</h3>
        <p class="text-[10px] text-gray-400 mb-3">
          Automatically connect — or stay off — based on the network you're on. Network
          Rules are checked first, top to bottom; the first rule that matches the current
          network wins. Any network no rule matches falls back to the Connect-on-Demand
          default. The engine toggle and the default behaviour both live there.
        </p>
        <router-link to="/network-rules"
          class="btn-secondary block w-full py-2 text-center text-xs">
          Open On-Demand &amp; Network Rules
        </router-link>
      </div>

      <!-- Protocol Failover Order (v0.9.15.70) -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Protocol Failover Order</h3>
        <p class="text-[10px] text-gray-400 mb-3">
          When a connection holds multiple protocols, failover walks them in this order. Move a protocol up to try it first.
        </p>
        <div class="space-y-1.5">
          <div v-for="(p, idx) in failoverOrder" :key="p" class="flex items-center gap-2 px-2 py-1.5 rounded border border-gray-200 dark:border-gray-700">
            <span class="text-[10px] text-gray-400 w-4">{{ idx + 1 }}.</span>
            <ProtocolIcon :protocol="p" size="lg" />
            <span class="flex-1 text-xs font-medium text-gray-700 dark:text-gray-200">{{ protocolLabel(p) }}</span>
            <button
              class="p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 disabled:opacity-30 disabled:cursor-not-allowed"
              :disabled="idx === 0"
              @click="moveFailover(idx, idx - 1)"
              title="Move up"
            >
              <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 15l7-7 7 7"/></svg>
            </button>
            <button
              class="p-1 rounded hover:bg-gray-100 dark:hover:bg-gray-700 disabled:opacity-30 disabled:cursor-not-allowed"
              :disabled="idx === failoverOrder.length - 1"
              @click="moveFailover(idx, idx + 1)"
              title="Move down"
            >
              <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"/></svg>
            </button>
          </div>
        </div>
      </div>

      <!-- Logs -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Diagnostics</h3>
        <router-link to="/logs"
          class="btn-secondary block w-full py-2 text-center text-xs">
          View Logs
        </router-link>
      </div>

      <!-- Backup & Restore -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Backup &amp; Restore</h3>
        <p class="text-[10px] text-gray-400 mb-3">
          Export all connections and settings as an encrypted file. Use the same passphrase to restore on another device.
        </p>
        <div class="grid grid-cols-2 gap-2">
          <button
            @click="showExport = true"
            class="btn-secondary py-2 text-xs"
          >
            Export...
          </button>
          <button
            @click="showImport = true"
            class="btn-secondary py-2 text-xs"
          >
            Import...
          </button>
        </div>
        <p v-if="backupMessage" class="text-xs mt-2" :class="backupError ? 'text-red-400' : 'text-green-400'">
          {{ backupMessage }}
        </p>
      </div>

      <!-- Export modal -->
      <div v-if="showExport" class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4" @click.self="showExport = false">
        <div class="card p-4 w-full max-w-sm">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white mb-3">Export backup</h3>
          <label class="text-xs text-gray-600 dark:text-gray-300 block mb-1">Passphrase (min 8 chars)</label>
          <input v-model="exportPassphrase" type="password" autocomplete="new-password"
            class="input-sm w-full mb-2" placeholder="Passphrase" />
          <label class="text-xs text-gray-600 dark:text-gray-300 block mb-1">Confirm passphrase</label>
          <input v-model="exportPassphraseConfirm" type="password" autocomplete="new-password"
            class="input-sm w-full mb-3" placeholder="Repeat" />
          <p v-if="exportError" class="text-xs text-red-400 mb-2">{{ exportError }}</p>
          <div class="flex justify-end gap-2">
            <button @click="showExport = false" class="btn-secondary px-3 py-1.5 text-xs">Cancel</button>
            <button
              @click="doExport"
              :disabled="!exportReady || exporting"
              class="btn-primary px-3 py-1.5 text-xs disabled:opacity-40"
            >
              {{ exporting ? 'Exporting...' : 'Export' }}
            </button>
          </div>
        </div>
      </div>

      <!-- Import modal -->
      <div v-if="showImport" class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4" @click.self="showImport = false">
        <div class="card p-4 w-full max-w-sm">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white mb-3">Import backup</h3>
          <p class="text-xs text-gray-500 dark:text-gray-400 mb-3">
            Importing replaces your current connections and settings.
          </p>
          <label class="text-xs text-gray-600 dark:text-gray-300 block mb-1">Passphrase</label>
          <input v-model="importPassphrase" type="password" autocomplete="current-password"
            class="input-sm w-full mb-3" placeholder="Passphrase" />
          <p v-if="importError" class="text-xs text-red-400 mb-2">{{ importError }}</p>
          <div class="flex justify-end gap-2">
            <button @click="showImport = false" class="btn-secondary px-3 py-1.5 text-xs">Cancel</button>
            <button
              @click="doImport"
              :disabled="importPassphrase.length === 0 || importing"
              class="btn-primary px-3 py-1.5 text-xs disabled:opacity-40"
            >
              {{ importing ? 'Importing...' : 'Import' }}
            </button>
          </div>
        </div>
      </div>

      <!-- Appearance -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Appearance</h3>
        <div class="flex items-center justify-between">
          <div>
            <span class="text-sm text-gray-700 dark:text-gray-300">Theme</span>
            <p class="text-[10px] text-gray-500 mt-0.5">
              {{ settings.theme === 'system' ? (systemIsDark ? 'System: Dark' : 'System: Light') : '' }}
            </p>
          </div>
          <AppSelect
            :model-value="settings.theme"
            @update:model-value="settings.theme = $event; applyTheme(); saveSettings()"
            :options="[
              { value: 'system', label: 'System Default' },
              { value: 'dark', label: 'Dark' },
              { value: 'light', label: 'Light' },
            ]"
          />
        </div>
      </div>

      <!-- About -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">About</h3>
        <div class="space-y-1">
          <div class="flex justify-between">
            <span class="text-xs text-gray-500">App</span>
            <span class="text-xs text-gray-600 dark:text-gray-300">Privycs VPN Client</span>
          </div>
          <div class="flex justify-between">
            <span class="text-xs text-gray-500">Version</span>
            <span class="text-xs text-gray-600 dark:text-gray-300">{{ vpn.version || '...' }}</span>
          </div>
          <div class="flex justify-between">
            <span class="text-xs text-gray-500">Protocol</span>
            <span class="text-xs text-gray-600 dark:text-gray-300">{{ vpn.status?.active_protocol || '-' }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { useVpnStore } from '@/stores/vpn'
import { GetSettings, UpdateSettings, GetPlatformFeatures, FetchMyProfile, GetHelperStatus, InstallPrivilegedHelper, UninstallPrivilegedHelper, ExportBackup, ImportBackup, PickBackupSavePath, PickBackupOpenPath, ValidateDnsOverride, TestDnsResolution, GetDnsProviders } from '../../wailsjs/go/main/App'
import AppSelect from '@/components/AppSelect.vue'
import DnsOverrideField from '@/components/DnsOverrideField.vue'
import ProtocolIcon from '@/components/ProtocolIcon.vue'
import { Switch } from '@headlessui/vue'

// v0.9.15.70 — Protocol failover order. Default mirrors the
// pre-v0.9.15.70 hard-coded enum order (AmneziaWG first → safer on
// censored / DPI-restricted networks). Persisted as
// settings.protocol_failover_order; reads/writes drive
// SavedConnection.OrderedConfigsFor in tryFailoverProtocol.
const PROTOCOL_DEFAULT_ORDER: string[] = ['amneziawg', 'wireguard', 'openvpn', 'ipsec']
const PROTOCOL_LABELS: Record<string, string> = {
  amneziawg: 'AmneziaWG',
  wireguard: 'WireGuard',
  openvpn: 'OpenVPN',
  ipsec: 'IPSec / IKEv2',
}
const failoverOrder = computed<string[]>(() => {
  const o = (settings.value?.protocol_failover_order ?? []) as string[]
  if (!o || o.length === 0) return [...PROTOCOL_DEFAULT_ORDER]
  // Defensive: append any default protocol missing from the saved
  // list so the UI always shows all four.
  const seen = new Set(o)
  return [...o, ...PROTOCOL_DEFAULT_ORDER.filter((p) => !seen.has(p))]
})
function protocolLabel(p: string): string {
  return PROTOCOL_LABELS[p] ?? p
}
function moveFailover(from: number, to: number) {
  if (to < 0 || to >= failoverOrder.value.length || from === to) return
  const next = [...failoverOrder.value]
  const [item] = next.splice(from, 1)
  next.splice(to, 0, item)
  settings.value.protocol_failover_order = next
  saveSettings()
}

const vpn = useVpnStore()

// Detect OS dark mode preference
const systemIsDark = ref(window.matchMedia('(prefers-color-scheme: dark)').matches)
let mediaQuery: MediaQueryList | null = null

function applyTheme() {
  const theme = settings.value.theme || 'system'
  let dark: boolean

  if (theme === 'system') {
    dark = systemIsDark.value
  } else {
    dark = theme === 'dark'
  }

  if (dark) {
    document.documentElement.classList.add('dark')
  } else {
    document.documentElement.classList.remove('dark')
  }
}

function onSystemThemeChange(e: MediaQueryListEvent) {
  systemIsDark.value = e.matches
  if (settings.value.theme === 'system') {
    applyTheme()
  }
}

const verifying = ref(false)
const apiStatus = ref<{ ok: boolean; message: string } | null>(null)

async function verifyApiKey() {
  verifying.value = true
  apiStatus.value = null
  try {
    await saveSettingsImmediate()
    const profile = await FetchMyProfile()
    apiStatus.value = { ok: true, message: `${profile.user} (${profile.count} configs)` }
    // Store username for display
    localStorage.setItem('privycs-api-user', profile.user)
  } catch (e: any) {
    const msg = e?.toString() || 'Connection failed'
    apiStatus.value = { ok: false, message: msg.replace('Error: ', '') }
  } finally {
    verifying.value = false
  }
}

async function saveSettingsImmediate() {
  try {
    await UpdateSettings(settings.value)
  } catch (e) {
    console.error('Failed to save settings:', e)
  }
}

// --- Backup / Restore state ---
// Mirrors the Android SettingsScreen export/import flow: passphrase-gated
// JSON envelope containing connections + settings, AES-256-GCM encrypted.
const showExport = ref(false)
const showImport = ref(false)
const exportPassphrase = ref('')
const exportPassphraseConfirm = ref('')
const importPassphrase = ref('')
const exportError = ref('')
const importError = ref('')
const exporting = ref(false)
const importing = ref(false)
const backupMessage = ref('')
const backupError = ref(false)

const exportReady = computed(() =>
  exportPassphrase.value.length >= 8 &&
  exportPassphrase.value === exportPassphraseConfirm.value
)

async function doExport() {
  exportError.value = ''
  if (exportPassphrase.value.length < 8) {
    exportError.value = 'Passphrase must be at least 8 characters'
    return
  }
  if (exportPassphrase.value !== exportPassphraseConfirm.value) {
    exportError.value = 'Passphrases do not match'
    return
  }
  exporting.value = true
  try {
    const path = await PickBackupSavePath()
    if (!path) {
      // User cancelled the OS dialog. Not an error — just leave the
      // modal open so they can try again or explicitly cancel.
      exporting.value = false
      return
    }
    await ExportBackup(path, exportPassphrase.value)
    backupMessage.value = `Backup saved to ${path}`
    backupError.value = false
    showExport.value = false
    // Wipe passphrases from memory so a later screenshot/log doesn't leak
    exportPassphrase.value = ''
    exportPassphraseConfirm.value = ''
  } catch (e: any) {
    exportError.value = e?.toString() || 'Export failed'
  } finally {
    exporting.value = false
  }
}

// --- IPv6 LAN bypass textarea binding ---
async function doImport() {
  importError.value = ''
  importing.value = true
  try {
    const path = await PickBackupOpenPath()
    if (!path) {
      importing.value = false
      return
    }
    await ImportBackup(path, importPassphrase.value)
    backupMessage.value = 'Backup restored. Please reopen the app to apply changes.'
    backupError.value = false
    showImport.value = false
    importPassphrase.value = ''
    // Reload settings in-place so at least the currently-visible screen reflects the change
    await loadSettings()
  } catch (e: any) {
    importError.value = e?.toString().replace('Error: ', '') || 'Import failed'
    backupError.value = true
  } finally {
    importing.value = false
  }
}

const settings = ref<any>({
  kill_switch_enabled: false,
  auto_connect_on_start: false,
  autostart_enabled: false,
  minimize_to_tray: true,
  dns_override: '',
  theme: 'system',
  gateway_url: '',
  api_key: '',
  connect_on_demand: {
    enabled: false,
    trigger: 'any',
    ssid_mode: 'all',
    ssid_list: [],
  },
  tunnel_health_mode: 'auto',
  tunnel_health_target: '',
  // v0.9.15.30: probe cadence overrides. Defaults map to the Go
  // const fallbacks in tunnel_health_monitor.go (5 s × 2 = max 10 s).
  tunnel_health_ping_interval_sec: 5,
  tunnel_health_dead_threshold: 2,
  // null = backend default (ON for ReconnectOnSystemWake — see
  // settings.go *bool fallback). Toggle UI explicitly sets true/false.
  reconnect_on_system_wake: null,
  prevent_display_sleep: false,
})

// Platform features — controls which settings toggles are available
const platform = ref<any>({
  kill_switch_supported: false,
  auto_connect_supported: false,
  autostart_supported: false,
  tray_supported: true,
  platform: '',
})

// macOS gets the manual-cleanup hint under the Helper section because
// Sequoia's TCC frequently terminates the osascript Install/Uninstall
// AppleEvent for unsigned apps. Linux/Windows have stable pkexec/SCM
// paths and don't need the fallback hint.
const isMacOS = computed(() => platform.value.platform === 'darwin')

async function loadSettings() {
  try {
    settings.value = await GetSettings()
    // Default to 'system' if not set
    if (!settings.value.theme) {
      settings.value.theme = 'system'
    }
    applyTheme()
  } catch (e) {
    console.error('Failed to load settings:', e)
  }
}

const dnsError = ref('')
let saveTimeout: ReturnType<typeof setTimeout> | null = null

async function saveSettings() {
  // Debounce: cancel any pending save and schedule a new one.
  // Prevents rapid-fire save calls when user tabs through fields.
  if (saveTimeout) clearTimeout(saveTimeout)
  saveTimeout = setTimeout(async () => {
    try {
      await UpdateSettings(settings.value)
    } catch (e) {
      console.error('Failed to save settings:', e)
    }
  }, 300)
}

// DNS provider preset table loaded from backend GetDnsProviders.
// Same canonical list used by the Android picker so cross-platform
// users get identical preset options.
const dnsProviders = ref<any[]>([])
const dnsProviderHint = ref('')
const dnsTestResult = ref<any>(null)
const dnsTesting = ref(false)

const dnsPresetOptions = computed(() => {
  return dnsProviders.value.map(p => ({ value: p.id, label: p.label }))
})

async function loadDnsProviders() {
  try {
    dnsProviders.value = await GetDnsProviders() as any[]
  } catch (e) {
    console.error('Failed to load DNS providers:', e)
  }
}

function applyDnsPreset(id: string) {
  if (!id) return
  const preset = dnsProviders.value.find(p => p.id === id)
  if (!preset) return
  settings.value.dns_override = preset.servers.join(', ')
  dnsError.value = ''
  validateAndSave()
}

async function onDnsInput() {
  // Live validation while typing so the user gets feedback before
  // they tab away.
  await refreshDnsValidationAndHint()
  // v0.9.14.13: also trigger debounced save here. Pre-fix only
  // @blur called validateAndSave — but preset-picks (clicking a
  // provider in the AppSelect dropdown) don't change focus, so
  // @blur never fires and the picked preset was lost on next
  // restart. Routing through saveSettings (debounced 300ms) means
  // typing also coalesces to a single save instead of one per
  // keystroke. User reported "nach ändern und neustart sind
  // settings weg" — exactly this case.
  saveSettings()
}

async function refreshDnsValidationAndHint() {
  const raw = (settings.value.dns_override || '').trim()
  if (!raw) {
    dnsError.value = ''
    dnsProviderHint.value = ''
    return
  }
  try {
    const bad = (await ValidateDnsOverride(raw)) as string[]
    if (bad && bad.length) {
      dnsError.value = `Invalid: ${bad.join(', ')}`
      dnsProviderHint.value = ''
      return
    }
    dnsError.value = ''
    // Match against known providers for an inline hint.
    const lower = raw.toLowerCase()
    const match = dnsProviders.value.find(p => {
      return p.servers.every((s: string) => lower.includes(s.toLowerCase()))
        && p.servers.length <= raw.split(/[,\s]+/).filter(Boolean).length
    })
    if (match) {
      dnsProviderHint.value = `Detected: ${match.label}` + (match.dot_host ? ` · DoT host: ${match.dot_host}` : '')
    } else {
      dnsProviderHint.value = ''
    }
  } catch (e) {
    console.error('DNS validate failed:', e)
  }
}

async function validateAndSave() {
  await refreshDnsValidationAndHint()
  if (dnsError.value) return
  saveSettings()
}

async function testDns() {
  dnsTesting.value = true
  dnsTestResult.value = null
  try {
    dnsTestResult.value = await TestDnsResolution('cloudflare.com')
  } catch (e) {
    dnsTestResult.value = { error: String(e) }
  } finally {
    dnsTesting.value = false
  }
}

function toggleSetting(key: string) {
  (settings.value as any)[key] = !(settings.value as any)[key]
  saveSettings()
}

// Privileged helper state
const helperStatus = ref<any>({ installed: false, running: false, platform: '' })
const helperInstalling = ref(false)
const helperError = ref('')

async function loadHelperStatus() {
  try {
    helperStatus.value = await GetHelperStatus()
  } catch (e) {
    console.error('Failed to load helper status:', e)
  }
}

async function installHelper() {
  helperInstalling.value = true
  helperError.value = ''
  try {
    await InstallPrivilegedHelper()
    await loadHelperStatus()
  } catch (e: any) {
    const msg = e?.toString()?.replace('Error: ', '') || 'Unknown error'
    helperError.value = 'Install failed: ' + msg
    console.error('Failed to install helper:', e)
  } finally {
    helperInstalling.value = false
  }
}

async function uninstallHelper() {
  helperInstalling.value = true
  helperError.value = ''
  try {
    await UninstallPrivilegedHelper()
    await loadHelperStatus()
  } catch (e: any) {
    // Pre-fix the catch only logged to console which is invisible in
    // production builds. v0.9.14.26 user reported "nach uninstall
    // wird das frontend nicht aktualisiert" — likely the osascript
    // admin-prompt was dismissed, uninstall threw, status stayed
    // unchanged, no UI feedback. Now surfaces as inline error.
    const msg = e?.toString()?.replace('Error: ', '') || 'Unknown error'
    helperError.value = 'Uninstall failed: ' + msg
    console.error('Failed to uninstall helper:', e)
  } finally {
    helperInstalling.value = false
  }
}

onMounted(async () => {
  loadSettings()
  loadHelperStatus()
  loadDnsProviders()
  // Load platform feature flags to disable unsupported toggles
  try {
    platform.value = await GetPlatformFeatures()
  } catch (e) {
    console.error('Failed to load platform features:', e)
  }
  // Listen for OS theme changes (for "System Default" mode)
  mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
  mediaQuery.addEventListener('change', onSystemThemeChange)
})

onUnmounted(() => {
  if (mediaQuery) {
    mediaQuery.removeEventListener('change', onSystemThemeChange)
  }
})
</script>
