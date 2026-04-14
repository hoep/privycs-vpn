import { defineStore } from 'pinia'
import { ref } from 'vue'
import { Status, Connect, Disconnect, SetProtocol, GetAvailableProtocols, GetVersion } from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'

// Map raw backend errors to user-friendly messages.
// Backend errors often contain Go stack details that confuse end users.
function friendlyError(e: any, fallback: string): string {
  const msg = e?.toString() || ''
  if (msg.includes('not found')) return 'VPN software not installed on this system'
  if (msg.includes('no active protocol')) return 'No protocol configured — import a config first'
  if (msg.includes('not available')) return 'This protocol is not available on your system'
  if (msg.includes('connection failed')) return 'Connection failed — check your config and network'
  if (msg.includes('timeout') || msg.includes('Timeout')) return 'Operation timed out — please try again'
  if (msg.includes('permission') || msg.includes('denied')) return 'Permission denied — admin rights required'
  if (msg.includes('already')) return 'Tunnel is already running'
  // If none matched, use a short fallback instead of raw error
  return fallback
}

export const useVpnStore = defineStore('vpn', () => {
  const status = ref<any>(null)
  const protocols = ref<any[]>([])
  const version = ref('')
  const loading = ref(false)
  const error = ref('')

  async function fetchStatus() {
    try {
      status.value = await Status()
      error.value = ''
    } catch (e: any) {
      error.value = friendlyError(e, 'Failed to get status')
    }
  }

  async function fetchProtocols() {
    try {
      protocols.value = await GetAvailableProtocols()
    } catch (e: any) {
      console.error('Failed to get protocols:', e)
    }
  }

  async function connect(protocol?: string) {
    loading.value = true
    error.value = ''
    try {
      await Connect(protocol || '')
      // Fetch full status after connect to ensure all fields
      // (connection_name, protocols, etc.) are populated
      await fetchStatus()
    } catch (e: any) {
      error.value = friendlyError(e, 'Connection failed')
    } finally {
      loading.value = false
    }
  }

  async function disconnect() {
    loading.value = true
    error.value = ''
    try {
      await Disconnect()
      await fetchStatus()
    } catch (e: any) {
      error.value = friendlyError(e, 'Disconnect failed')
    } finally {
      loading.value = false
    }
  }

  async function switchProtocol(protocol: string) {
    loading.value = true
    error.value = ''
    try {
      await SetProtocol(protocol)
      await fetchStatus()
    } catch (e: any) {
      error.value = friendlyError(e, 'Protocol switch failed')
    } finally {
      loading.value = false
    }
  }

  // Listen for real-time status updates from Go backend
  let unsubscribe: (() => void) | null = null

  function startListening() {
    // Clean up existing listener to prevent memory leaks
    // (e.g. if init() is called multiple times)
    if (unsubscribe) {
      unsubscribe()
    }
    unsubscribe = EventsOn('vpn:status', (data: any) => {
      status.value = data
    })
  }

  function stopListening() {
    if (unsubscribe) {
      unsubscribe()
      unsubscribe = null
    }
  }

  // Initialize
  async function init() {
    await fetchStatus()
    await fetchProtocols()
    version.value = await GetVersion()
    startListening()
  }

  return {
    status,
    protocols,
    version,
    loading,
    error,
    fetchStatus,
    fetchProtocols,
    connect,
    disconnect,
    switchProtocol,
    init,
    startListening,
    stopListening,
  }
})
