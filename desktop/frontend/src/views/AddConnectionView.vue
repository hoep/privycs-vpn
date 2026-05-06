<template>
  <div class="p-4 overflow-y-auto max-h-[calc(100vh-7rem)]">
    <div class="flex items-center justify-between mb-4">
      <div class="flex items-center gap-2">
        <button @click="$router.back()" class="text-gray-500 dark:text-gray-400 hover:text-gray-900 dark:hover:text-white">
          <ArrowLeftIcon class="w-5 h-5" />
        </button>
        <h2 class="text-sm font-semibold text-gray-600 dark:text-gray-300">
          {{ targetConnection ? 'Add Protocol to ' + targetConnection.name : 'Add Connection' }}
        </h2>
      </div>
      <div class="flex items-center gap-2">
        <!-- QR code scan entry point. Opens a modal with the webcam and
             a BarcodeDetector-backed scanning loop. Accepts WireGuard
             raw-config QRs and Privycs enrollment URLs identically to
             the Android side. -->
        <button
          @click="showQrScanner = true"
          class="text-xs text-primary-400 hover:text-primary-300 flex items-center gap-1"
          title="Scan QR code"
        >
          <QrCodeIcon class="w-4 h-4" />
          Scan QR
        </button>
        <button
          v-if="hasApiKey"
          @click="toggleGatewayPanel"
          class="text-xs text-primary-400 hover:text-primary-300 flex items-center gap-1"
        >
          <CloudArrowDownIcon class="w-4 h-4" />
          Gateway
        </button>
      </div>
    </div>

    <!-- Gateway configs panel -->
    <div v-if="showGateway" class="card p-3 mb-4 border border-primary-500/30">
      <div class="flex items-center justify-between mb-2">
        <span class="text-xs font-semibold text-gray-500 dark:text-gray-400">
          {{ targetConnection ? 'Missing Protocols from Gateway' : 'Gateway Configs' }}
        </span>
        <button @click="loadGatewayConfigs" :disabled="loadingGateway" class="text-[10px] text-primary-400 hover:text-primary-300">
          {{ loadingGateway ? 'Loading...' : 'Refresh' }}
        </button>
      </div>
      <p v-if="gatewayError" class="text-[10px] text-red-400 mb-2">{{ gatewayError }}</p>
      <div v-if="filteredGatewayConfigs.length === 0 && !loadingGateway" class="text-[10px] text-gray-500 text-center py-2">
        <template v-if="targetConnection && gatewayConfigs.length > 0">
          All user protocols already imported for this connection
        </template>
        <template v-else-if="gatewayConfigs.length === 0">
          No configs available. Click Refresh.
        </template>
        <template v-else>No matching configs</template>
      </div>
      <div v-else class="space-y-1.5 max-h-64 overflow-y-auto">
        <div
          v-for="rc in filteredGatewayConfigs"
          :key="rc.protocol + '-' + rc.id"
          class="flex items-center justify-between py-1.5 px-2 rounded hover:bg-gray-100 dark:hover:bg-gray-700/50"
        >
          <div class="flex items-center gap-2 min-w-0">
            <ProtocolIcon :protocol="rc.protocol" class="w-4 h-4 flex-shrink-0" />
            <div class="min-w-0">
              <span class="text-xs text-gray-700 dark:text-gray-300 truncate block">{{ rc.peer_name }}</span>
              <span class="text-[9px] text-gray-400">{{ rc.interface_name }} / {{ rc.vpn_ip }}</span>
            </div>
          </div>
          <button
            @click.stop="importFromGateway(rc)"
            :disabled="gatewayImportingId === rc.protocol + '-' + rc.id"
            class="text-[10px] px-2 py-1 rounded bg-primary-600 text-white hover:bg-primary-700 disabled:opacity-50 flex-shrink-0"
          >
            {{ gatewayImportingId === rc.protocol + '-' + rc.id ? '...' : 'Import' }}
          </button>
        </div>
      </div>
    </div>

    <!-- File Import -->
    <div class="space-y-4">
      <div class="text-center mb-2">
        <p class="text-xs text-gray-500 dark:text-gray-400">
          {{ targetConnection ? 'Add another protocol config' : 'Import a VPN configuration file' }}
        </p>
      </div>

      <!-- Hidden file input -->
      <input ref="fileInput" type="file" class="hidden"
        accept=".conf,.ovpn,.sswan,.p12"
        @change="handleFileSelect" />

      <!-- Drop zone -->
      <div
        class="border-2 border-dashed border-gray-300 dark:border-gray-600 rounded-xl p-8 text-center cursor-pointer hover:border-primary-500/50 hover:bg-primary-500/5 transition-all"
        @click="($refs.fileInput as HTMLInputElement)?.click()"
        @dragover.prevent="dragOver = true"
        @dragleave="dragOver = false"
        @drop.prevent="handleDrop"
        :class="{ 'border-primary-500 bg-primary-500/10': dragOver }"
      >
        <DocumentArrowUpIcon class="w-10 h-10 mx-auto text-gray-500 mb-3" />
        <p class="text-sm text-gray-600 dark:text-gray-300 font-medium">
          {{ dragOver ? 'Drop file here' : 'Click to select config file' }}
        </p>
        <p class="text-xs text-gray-500 mt-1">
          .conf (WireGuard) / .ovpn (OpenVPN) / .sswan (IPSec)
        </p>
      </div>

      <!-- Selected file preview + options -->
      <div v-if="selectedFile" class="space-y-3">
        <div class="card p-4">
          <div class="flex items-center gap-3 mb-3">
            <DocumentTextIcon class="w-8 h-8 text-primary-400 flex-shrink-0" />
            <div class="min-w-0">
              <p class="text-sm text-gray-900 dark:text-white font-medium truncate">{{ selectedFile.name }}</p>
              <p class="text-xs text-gray-500 dark:text-gray-400">
                <span class="font-medium" :class="protocolColor(detectedProtocol)">
                  {{ detectedProtocol ? protocolDisplayName(detectedProtocol) : 'Unknown' }}
                </span>
                / {{ formatFileSize(selectedFile.size) }}
              </p>
            </div>
          </div>

          <!-- Connection name (only for new connections) -->
          <div v-if="!targetConnection">
            <label class="block text-xs text-gray-500 dark:text-gray-400 mb-1">Connection Name</label>
            <input
              v-model="connectionName"
              type="text"
              placeholder="e.g. Office VPN"
              maxlength="64"
              class="input"
            />
            <p v-if="connectionName.length > 50" class="text-[10px] text-gray-400 mt-0.5">
              {{ connectionName.length }}/64
            </p>
          </div>

          <!-- Add to existing connection (only for new connections) -->
          <div v-if="!targetConnection && existingConnections.length > 0" class="mt-3">
            <label class="block text-xs text-gray-500 dark:text-gray-400 mb-1">Or add to existing connection</label>
            <AppSelect
              v-model="addToExisting"
              :options="existingConnectionOptions"
            />
          </div>
        </div>

        <button
          @click="importConfig"
          :disabled="importing"
          class="btn-primary w-full py-2.5 text-sm disabled:opacity-50"
        >
          {{ importing ? 'Importing...' : (addToExisting || targetConnection ? 'Add Protocol Config' : 'Import Config') }}
        </button>
      </div>

      <p v-if="error" class="text-xs text-red-400 text-center">{{ error }}</p>
      <p v-if="success" class="text-xs text-green-400 text-center">{{ success }}</p>
    </div>

    <!-- Supported Formats Info -->
    <div class="mt-6 card p-4">
      <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 mb-2">Supported Formats</h3>
      <div class="space-y-1.5">
        <div class="flex items-center gap-2">
          <span class="w-1.5 h-1.5 rounded-full bg-red-500"></span>
          <span class="text-xs text-gray-600 dark:text-gray-300">.conf — WireGuard</span>
        </div>
        <div class="flex items-center gap-2">
          <span class="w-1.5 h-1.5 rounded-full bg-orange-400"></span>
          <span class="text-xs text-gray-600 dark:text-gray-300">.ovpn — OpenVPN</span>
        </div>
        <div class="flex items-center gap-2">
          <span class="w-1.5 h-1.5 rounded-full bg-blue-400"></span>
          <span class="text-xs text-gray-600 dark:text-gray-300">.sswan — IPSec/IKEv2</span>
        </div>
      </div>
    </div>

    <QrScanModal
      v-if="showQrScanner"
      @scanned="handleQrScanned"
      @close="showQrScanner = false"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ImportConfig, ListConnections, FetchMyProfile, DownloadAndImportConfig, GetSettings, UpdateSettings } from '../../wailsjs/go/main/App'
import AppSelect from '@/components/AppSelect.vue'
import ProtocolIcon from '@/components/ProtocolIcon.vue'
import QrScanModal from '@/components/QrScanModal.vue'
import { parseQrPayload } from '@/util/qrPayload'
import {
  ArrowLeftIcon,
  CloudArrowDownIcon,
  DocumentArrowUpIcon,
  DocumentTextIcon,
  QrCodeIcon,
} from '@heroicons/vue/24/outline'

const route = useRoute()
const router = useRouter()

const fileInput = ref<HTMLInputElement | null>(null)
const selectedFile = ref<File | null>(null)
const fileContent = ref('')
const detectedProtocol = ref('')
const connectionName = ref('')
const addToExisting = ref('')
const dragOver = ref(false)
const importing = ref(false)
const error = ref('')
const success = ref('')
const existingConnections = ref<any[]>([])

// Gateway panel state
const hasApiKey = ref(!!localStorage.getItem('privycs-api-user'))
const showGateway = ref(false)
const gatewayConfigs = ref<any[]>([])
const loadingGateway = ref(false)
const gatewayError = ref('')
const gatewayImportingId = ref('')

// If connectionId is in query params, we're adding a protocol to existing connection
const targetConnection = computed(() => {
  const id = route.query.connectionId as string
  if (!id) return null
  return existingConnections.value.find(c => c.id === id) || null
})

const existingConnectionOptions = computed(() => {
  return [
    { value: '', label: 'Create new connection' },
    ...existingConnections.value.map(conn => ({
      value: conn.id,
      label: `${conn.name} (${conn.protocols?.map((p: any) => protocolShort(p.protocol)).join(', ')})`
    }))
  ]
})

async function loadConnections() {
  try {
    existingConnections.value = await ListConnections() || []
  } catch {
    existingConnections.value = []
  }
}

// Gateway-configs fetched from the gateway API. When in "Add Protocol to X"
// mode, these are filtered to only show protocols not yet in the target connection.
const filteredGatewayConfigs = computed(() => {
  if (!targetConnection.value) {
    return gatewayConfigs.value
  }
  const existingProtocols = new Set(
    (targetConnection.value.protocols || []).map((p: any) => p.protocol)
  )
  return gatewayConfigs.value.filter(rc => !existingProtocols.has(rc.protocol))
})

function toggleGatewayPanel() {
  showGateway.value = !showGateway.value
  if (showGateway.value && gatewayConfigs.value.length === 0) {
    loadGatewayConfigs()
  }
}

async function loadGatewayConfigs() {
  loadingGateway.value = true
  gatewayError.value = ''
  try {
    const profile = await FetchMyProfile()
    gatewayConfigs.value = profile.configs || []
    localStorage.setItem('privycs-api-user', profile.user)
    hasApiKey.value = true
  } catch (e: any) {
    gatewayError.value = e?.toString()?.replace('Error: ', '') || 'Failed to connect'
    gatewayConfigs.value = []
  } finally {
    loadingGateway.value = false
  }
}

async function importFromGateway(rc: any) {
  const key = rc.protocol + '-' + rc.id
  gatewayImportingId.value = key
  error.value = ''
  gatewayError.value = ''
  try {
    const connID = targetConnection.value?.id || ''
    await DownloadAndImportConfig(rc.protocol, rc.id, rc.peer_name, connID)
    success.value = connID
      ? `${protocolDisplayName(rc.protocol)} config added from gateway`
      : 'Connection imported from gateway'
    await new Promise(resolve => setTimeout(resolve, 500))
    router.push('/connection')
  } catch (e: any) {
    const msg = e?.toString()?.replace('Error: ', '') || 'Unknown error'
    // Show errors inline inside the gateway panel (where the user just
    // clicked) instead of in the file-import error slot far below — the
    // file-import error renders below the gateway list and was easy to
    // miss when the gateway list is long enough to scroll past it.
    gatewayError.value = 'Gateway import failed: ' + msg
  } finally {
    gatewayImportingId.value = ''
  }
}

function handleFileSelect(e: Event) {
  const input = e.target as HTMLInputElement
  if (!input.files?.length) return
  processFile(input.files[0])
}

function handleDrop(e: DragEvent) {
  dragOver.value = false
  if (!e.dataTransfer?.files?.length) return
  processFile(e.dataTransfer.files[0])
}

// QR scanner state and handler. Opens the webcam-backed modal and
// routes the decoded payload through the shared parser so WG configs
// land in the same fileContent pipeline as a drag-drop import, and
// Privycs enrollment URLs auto-fill the gateway URL / API key before
// opening the gateway panel.
const showQrScanner = ref(false)

async function handleQrScanned(raw: string) {
  showQrScanner.value = false
  const payload = parseQrPayload(raw)
  if (payload.kind === 'wireguard') {
    // Feed the WireGuard config through the same pipeline as a file
    // import so downstream name-deriving and protocol-detect logic
    // stays in one place.
    fileContent.value = payload.content
    detectedProtocol.value = 'wireguard'
    if (!connectionName.value) connectionName.value = 'scanned'
    selectedFile.value = null
    error.value = ''
    success.value = ''
  } else if (payload.kind === 'privycs') {
    // Store gateway credentials if supplied and pop open the gateway
    // panel so the user can pick which protocol to import. Does NOT
    // auto-import because we don't know which protocol(s) the user
    // wants and the list may be filtered by "add to existing" mode.
    if (payload.gatewayUrl && payload.apiKey) {
      try {
        const currentSettings = await GetSettings()
        currentSettings.gateway_url = payload.gatewayUrl
        currentSettings.api_key = payload.apiKey
        await UpdateSettings(currentSettings)
      } catch (e) {
        error.value = `Could not store gateway credentials: ${e}`
        return
      }
    }
    showGateway.value = true
    await loadGatewayConfigs()
  } else {
    error.value = 'QR code content not recognised as a VPN config or Privycs enrollment URL'
  }
}

function processFile(file: File) {
  selectedFile.value = file
  error.value = ''
  success.value = ''

  const name = file.name.toLowerCase()
  if (name.endsWith('.conf')) detectedProtocol.value = 'wireguard'
  else if (name.endsWith('.ovpn')) detectedProtocol.value = 'openvpn'
  else if (name.endsWith('.sswan')) detectedProtocol.value = 'ipsec'
  else detectedProtocol.value = ''

  connectionName.value = file.name.replace(/\.(conf|ovpn|sswan|p12)$/i, '')

  const reader = new FileReader()
  reader.onload = () => {
    fileContent.value = reader.result as string
    if (!detectedProtocol.value) {
      if (fileContent.value.includes('[Interface]')) detectedProtocol.value = 'wireguard'
      else if (fileContent.value.includes('remote ') || fileContent.value.includes('<ca>')) detectedProtocol.value = 'openvpn'
    }
  }
  reader.readAsText(file)
}

async function importConfig() {
  if (!fileContent.value || !detectedProtocol.value) {
    error.value = 'Cannot detect protocol from file'
    return
  }

  importing.value = true
  error.value = ''

  try {
    // Determine target: existing connection (from query or dropdown) or new
    const connID = targetConnection.value?.id || addToExisting.value || ''
    const name = connID ? '' : connectionName.value // don't rename existing connections

    await ImportConfig(
      detectedProtocol.value,
      fileContent.value,
      selectedFile.value?.name || '',
      name,
      connID
    )
    success.value = connID
      ? `${protocolDisplayName(detectedProtocol.value)} config added`
      : 'Connection imported successfully'
    // Wait for success message to be visible, then navigate
    await new Promise(resolve => setTimeout(resolve, 500))
    router.push('/connection')
  } catch (e: any) {
    const msg = e?.toString() || ''
    if (msg.includes('invalid config')) error.value = 'Invalid configuration file format'
    else if (msg.includes('unsupported')) error.value = 'Unsupported protocol or file format'
    else error.value = 'Import failed — please check your config file'
  } finally {
    importing.value = false
  }
}

function protocolDisplayName(proto: string): string {
  switch (proto) {
    case 'wireguard': return 'WireGuard'
    case 'openvpn': return 'OpenVPN'
    case 'ipsec': return 'IPSec/IKEv2'
    default: return proto
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

function protocolColor(proto: string): string {
  switch (proto) {
    case 'wireguard': return 'text-red-400'
    case 'openvpn': return 'text-orange-400'
    case 'ipsec': return 'text-blue-400'
    default: return 'text-gray-400'
  }
}

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / 1048576).toFixed(1) + ' MB'
}

onMounted(loadConnections)
</script>
