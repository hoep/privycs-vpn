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
      <!-- Connect Button -->
      <div class="mt-4 mb-5 relative">
        <div class="w-40 h-40 rounded-full flex items-center justify-center"
          :class="isConnected ? 'bg-primary-500/5' : 'bg-gray-100 dark:bg-gray-800/50'">
          <div v-if="isConnected" class="absolute inset-0 rounded-full border-2 border-primary-500/30 pulse-ring pointer-events-none"></div>
          <button @click="toggleConnection" :disabled="vpn.loading"
            class="w-32 h-32 rounded-full flex flex-col items-center justify-center transition-all duration-300 focus:outline-none"
            :class="isConnected
              ? 'bg-gradient-to-br from-primary-500 to-primary-600 shadow-lg shadow-primary-500/25 hover:shadow-primary-500/40'
              : 'bg-white dark:bg-gray-800 border-2 border-gray-300 dark:border-gray-600 hover:border-gray-400 dark:hover:border-gray-500'">
            <ArrowPathIcon v-if="vpn.loading" class="w-8 h-8 text-white animate-spin" />
            <ShieldCheckIcon v-else class="w-12 h-12 transition-all"
              :class="isConnected ? 'text-white' : 'text-gray-500'" />
            <span class="text-[11px] font-semibold mt-1.5" :class="isConnected ? 'text-white/90' : 'text-gray-500 dark:text-gray-400'">
              {{ connectionLabel }}
            </span>
          </button>
        </div>
      </div>

      <!-- Uptime -->
      <div v-if="isConnected && vpn.status?.uptime" class="mb-3">
        <span class="text-lg font-mono text-gray-900 dark:text-white">{{ vpn.status.uptime }}</span>
      </div>

      <!-- Connection Name — click to switch between connections -->
      <div v-if="vpn.status?.connection_name" class="mb-2 relative">
        <button
          @click.stop="showConnectionPicker = !showConnectionPicker"
          class="flex items-center gap-1 text-sm font-medium text-gray-600 dark:text-gray-300 hover:text-primary-400 transition-colors"
        >
          {{ vpn.status.connection_name }}
          <ChevronDownIcon class="w-3.5 h-3.5" :class="showConnectionPicker ? 'rotate-180' : ''" />
        </button>
        <!-- Dropdown -->
        <div
          v-if="showConnectionPicker && allConnections.length > 1"
          @click.stop
          class="absolute left-1/2 -translate-x-1/2 mt-1 w-56 card p-1 shadow-lg z-10"
        >
          <button
            v-for="conn in allConnections"
            :key="conn.id"
            @click="pickConnection(conn)"
            class="w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left text-xs transition-colors"
            :class="conn.id === vpn.status?.connection_id
              ? 'bg-primary-500/10 text-primary-300'
              : 'text-gray-500 dark:text-gray-400 hover:bg-gray-200 dark:hover:bg-gray-700/50 hover:text-gray-900 dark:hover:text-white'"
          >
            <div class="w-1.5 h-1.5 rounded-full flex-shrink-0"
              :class="conn.id === vpn.status?.connection_id ? 'bg-primary-400' : 'bg-gray-600'">
            </div>
            <span class="truncate">{{ conn.name }}</span>
            <span class="ml-auto text-[9px] text-gray-600">
              {{ conn.protocols?.map((p: any) => protocolShort(p.protocol)).join('/') }}
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
      </div>

      <!-- Server Address -->
      <div v-if="vpn.status?.server_address" class="mb-4">
        <span class="text-xs text-gray-500 truncate max-w-[250px] block text-center">
          {{ vpn.status.server_address }}
        </span>
      </div>

      <!-- Transfer Stats -->
      <div v-if="isConnected" class="w-full max-w-sm grid grid-cols-2 gap-3 mb-4">
        <div class="card p-3 text-center">
          <div class="flex items-center justify-center gap-1 mb-1">
            <ArrowDownTrayIcon class="w-3 h-3 text-green-400" />
            <span class="text-[10px] text-gray-500">Download</span>
          </div>
          <span class="text-base font-semibold text-gray-900 dark:text-white">{{ formatBytes(vpn.status?.bytes_rx) }}</span>
        </div>
        <div class="card p-3 text-center">
          <div class="flex items-center justify-center gap-1 mb-1">
            <ArrowUpTrayIcon class="w-3 h-3 text-blue-400" />
            <span class="text-[10px] text-gray-500">Upload</span>
          </div>
          <span class="text-base font-semibold text-gray-900 dark:text-white">{{ formatBytes(vpn.status?.bytes_tx) }}</span>
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
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useVpnStore } from '@/stores/vpn'
import { SelectProtocol, ListConnections, ActivateConnection, GetActiveConfigContent, SaveActiveConfigContent } from '../../wailsjs/go/main/App'
import ProtocolIcon from '@/components/ProtocolIcon.vue'
import {
  ShieldCheckIcon,
  ArrowPathIcon,
  ArrowDownTrayIcon,
  ArrowUpTrayIcon,
  ChevronDownIcon,
  PencilSquareIcon,
  XMarkIcon,
} from '@heroicons/vue/24/outline'

const vpn = useVpnStore()
const showConnectionPicker = ref(false)
const allConnections = ref<any[]>([])

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
  if (conn.id === vpn.status?.connection_id) return
  try {
    const proto = conn.active_protocol || conn.protocols?.[0]?.protocol || ''
    await ActivateConnection(conn.id, proto)
    await vpn.fetchStatus()
    await loadConnections()
  } catch (e: any) {
    vpn.error = 'Failed to switch connection'
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

onMounted(() => {
  loadConnections()
  document.addEventListener('click', onClickOutside)
})
onUnmounted(() => {
  document.removeEventListener('click', onClickOutside)
})

// Robust connected state — use status from Go backend
const isConnected = computed(() => {
  return vpn.status?.connected === true
})

const showWelcome = computed(() => {
  if (!vpn.status) return true
  if (isConnected.value) return false
  if (vpn.status.connection_name) return false
  if (vpn.status.connection_protocols?.length > 0) return false
  return true
})

const connectionLabel = computed(() => {
  if (vpn.loading) {
    return isConnected.value ? 'Disconnecting...' : 'Connecting...'
  }
  return isConnected.value ? 'Connected' : 'Connect'
})

const connectionProtocols = computed(() => {
  return vpn.status?.connection_protocols || []
})

async function toggleConnection() {
  if (isConnected.value) {
    await vpn.disconnect()
  } else {
    await vpn.connect()
  }
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
