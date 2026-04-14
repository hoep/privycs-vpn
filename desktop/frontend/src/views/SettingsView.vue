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
          <!-- Connect on Demand -->
          <div>
            <div class="flex items-center justify-between">
              <div>
                <span class="text-sm text-gray-700 dark:text-gray-300">Connect on Demand</span>
                <p class="text-[10px] text-gray-400 mt-0.5">
                  Auto-connect based on network
                </p>
              </div>
              <button
                @click="toggleConnectOnDemand"
                class="toggle"
                :class="[
                  connectOnDemand.enabled && platform.auto_connect_supported ? 'toggle-enabled' : 'toggle-disabled',
                  !platform.auto_connect_supported ? 'opacity-40 cursor-not-allowed' : ''
                ]"
                :disabled="!platform.auto_connect_supported"
              >
                <span class="toggle-knob" :class="connectOnDemand.enabled && platform.auto_connect_supported ? 'translate-x-5' : 'translate-x-0'" />
              </button>
            </div>

            <!-- On-demand options (visible when enabled) -->
            <div v-if="connectOnDemand.enabled && platform.auto_connect_supported" class="mt-3 ml-1 space-y-2.5 border-l-2 border-gray-200 dark:border-gray-600 pl-3">
              <div class="flex items-center justify-between">
                <span class="text-xs text-gray-600 dark:text-gray-400">When connected to</span>
                <AppSelect
                  :model-value="connectOnDemand.trigger || 'wifi_mobile'"
                  @update:model-value="connectOnDemand.trigger = $event; saveOnDemandSettings()"
                  :options="[
                    { value: 'wifi', label: 'WiFi' },
                    { value: 'mobile', label: 'Mobile' },
                    { value: 'wifi_mobile', label: 'WiFi & Mobile' },
                  ]"
                />
              </div>
              <div class="flex items-center justify-between">
                <span class="text-xs text-gray-600 dark:text-gray-400">WiFi networks</span>
                <AppSelect
                  :model-value="connectOnDemand.ssid_mode || 'all'"
                  @update:model-value="connectOnDemand.ssid_mode = $event; saveOnDemandSettings()"
                  :options="[
                    { value: 'all', label: 'All SSIDs' },
                    { value: 'only', label: 'Only these SSIDs' },
                    { value: 'except', label: 'Except these SSIDs' },
                  ]"
                />
              </div>
              <div v-if="connectOnDemand.ssid_mode === 'only' || connectOnDemand.ssid_mode === 'except'">
                <label class="text-xs text-gray-600 dark:text-gray-400 block mb-1">
                  {{ connectOnDemand.ssid_mode === 'only' ? 'Connect only on' : 'Do not connect on' }}
                </label>
                <input
                  v-model="ssidListText"
                  @blur="parseAndSaveSSIDs"
                  type="text"
                  placeholder="HomeWiFi, OfficeNet, CafeSpot"
                  class="input text-xs"
                />
                <p class="text-[10px] text-gray-400 mt-0.5">Comma-separated SSID names</p>
              </div>
              <!-- Live status indicator -->
              <div v-if="codStatus" class="flex items-center gap-1.5 pt-1">
                <span class="inline-block w-1.5 h-1.5 rounded-full"
                  :class="codStatus.vpn_connected ? 'bg-green-400' : (codStatus.rule_match ? 'bg-yellow-400' : 'bg-gray-400')"
                />
                <span class="text-[10px] text-gray-500 dark:text-gray-400">
                  <template v-if="codStatus.ssid">{{ codStatus.ssid }} ({{ codStatus.network_type }})</template>
                  <template v-else-if="codStatus.network_type !== 'none'">{{ codStatus.network_type }}</template>
                  <template v-else>No network</template>
                  <template v-if="codStatus.vpn_connected"> -- VPN: Active</template>
                  <template v-else-if="codStatus.rule_match"> -- Connecting...</template>
                </span>
              </div>
            </div>
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

      <!-- Network -->
      <div class="card p-4">
        <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">Network</h3>
        <div class="space-y-3">
          <div>
            <label class="text-sm text-gray-700 dark:text-gray-300 block mb-1">DNS Override</label>
            <input
              v-model="settings.dns_override"
              @blur="validateAndSave"
              type="text"
              placeholder="e.g. 1.1.1.1 or leave empty"
              pattern="^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})?$"
              maxlength="45"
              class="input"
            />
            <p v-if="dnsError" class="text-[10px] text-red-400 mt-0.5">{{ dnsError }}</p>
          </div>
          <div class="flex items-center justify-between">
            <span class="text-sm text-gray-700 dark:text-gray-300">Routing Mode</span>
            <AppSelect
              :model-value="settings.routing_mode"
              @update:model-value="settings.routing_mode = $event; saveSettings()"
              :options="[
                { value: 'full', label: 'Full Tunnel' },
                { value: 'split', label: 'Split Tunnel' },
              ]"
            />
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
import { ref, onMounted, onUnmounted } from 'vue'
import { useVpnStore } from '@/stores/vpn'
import { GetSettings, UpdateSettings, GetPlatformFeatures, FetchMyProfile, GetConnectOnDemandStatus } from '../../wailsjs/go/main/App'
import AppSelect from '@/components/AppSelect.vue'

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

const settings = ref<any>({
  kill_switch_enabled: false,
  auto_connect_on_start: false,
  autostart_enabled: false,
  minimize_to_tray: true,
  dns_override: '',
  routing_mode: 'full',
  theme: 'system',
  gateway_url: '',
  api_key: '',
  connect_on_demand: {
    enabled: false,
    trigger: 'wifi_mobile',
    ssid_mode: 'all',
    ssid_list: [],
  },
})

// Connect on Demand state
const connectOnDemand = ref<any>({
  enabled: false,
  trigger: 'wifi_mobile',
  ssid_mode: 'all',
  ssid_list: [],
})
const ssidListText = ref('')
const codStatus = ref<any>(null)
let codStatusInterval: ReturnType<typeof setInterval> | null = null

function toggleConnectOnDemand() {
  connectOnDemand.value.enabled = !connectOnDemand.value.enabled
  saveOnDemandSettings()
  if (connectOnDemand.value.enabled) {
    startCodStatusPolling()
  } else {
    stopCodStatusPolling()
    codStatus.value = null
  }
}

function saveOnDemandSettings() {
  settings.value.connect_on_demand = { ...connectOnDemand.value }
  // Keep legacy field in sync
  settings.value.auto_connect_on_start = connectOnDemand.value.enabled
  saveSettings()
}

function parseAndSaveSSIDs() {
  const text = ssidListText.value.trim()
  if (text) {
    connectOnDemand.value.ssid_list = text.split(',').map((s: string) => s.trim()).filter((s: string) => s.length > 0)
  } else {
    connectOnDemand.value.ssid_list = []
  }
  saveOnDemandSettings()
}

async function pollCodStatus() {
  try {
    codStatus.value = await GetConnectOnDemandStatus()
  } catch (e) {
    // Silently ignore polling errors
  }
}

function startCodStatusPolling() {
  if (codStatusInterval) return
  pollCodStatus()
  codStatusInterval = setInterval(pollCodStatus, 5000)
}

function stopCodStatusPolling() {
  if (codStatusInterval) {
    clearInterval(codStatusInterval)
    codStatusInterval = null
  }
}

// Platform features — controls which settings toggles are available
const platform = ref<any>({
  kill_switch_supported: false,
  auto_connect_supported: false,
  autostart_supported: false,
  tray_supported: true,
  platform: '',
})

async function loadSettings() {
  try {
    settings.value = await GetSettings()
    // Default to 'system' if not set
    if (!settings.value.theme) {
      settings.value.theme = 'system'
    }
    // Sync connect-on-demand state from loaded settings
    if (settings.value.connect_on_demand) {
      connectOnDemand.value = { ...settings.value.connect_on_demand }
      if (connectOnDemand.value.ssid_list && connectOnDemand.value.ssid_list.length > 0) {
        ssidListText.value = connectOnDemand.value.ssid_list.join(', ')
      }
      if (connectOnDemand.value.enabled) {
        startCodStatusPolling()
      }
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

function validateAndSave() {
  const dns = settings.value.dns_override?.trim() || ''
  if (dns && !/^(\d{1,3}\.){3}\d{1,3}$/.test(dns) && !dns.includes(':')) {
    dnsError.value = 'Enter a valid IP address (e.g. 1.1.1.1)'
    return
  }
  dnsError.value = ''
  saveSettings()
}

function toggleSetting(key: string) {
  (settings.value as any)[key] = !(settings.value as any)[key]
  saveSettings()
}

onMounted(async () => {
  loadSettings()
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
  stopCodStatusPolling()
})
</script>
