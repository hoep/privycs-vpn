import { defineStore } from 'pinia'
import { ref } from 'vue'
import { Status, Connect, Disconnect, SetProtocol, GetAvailableProtocols, GetVersion } from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'

// Map raw backend errors to user-friendly messages.
// Backend errors often contain Go stack details that confuse end users.
// Matches the Android error-mapping fidelity (PrivycsLogger + VpnStatus.error
// in VpnServiceManager) so cross-device users see consistent wording.
function friendlyError(e: any, fallback: string): string {
  const msg = (e?.toString() || '').toLowerCase()

  // Auth / certificate failures — most common real-world failure mode.
  // OpenVPN emits "AUTH_FAILED" or "TLS Error", WireGuard "unknown peer",
  // strongSwan "authentication failed".
  if (msg.includes('auth_failed') || msg.includes('auth failed') || msg.includes('authentication')) {
    return 'Authentication failed — your credentials or certificate may be invalid'
  }
  if (msg.includes('tls error') || msg.includes('tls handshake') || msg.includes('certificate')) {
    return 'TLS / certificate error — server identity could not be verified'
  }
  if (msg.includes('unknown peer') || msg.includes('key mismatch')) {
    return 'WireGuard key mismatch — re-import your config from the gateway'
  }

  // DNS / connectivity. Seen with proto udp6 when the client's resolver
  // strips AAAA records, or when the gateway hostname is wrong.
  if (msg.includes('resolve') || msg.includes('no such host') || msg.includes('name resolution')) {
    return 'Could not resolve the server hostname — check your DNS and internet connection'
  }
  if (msg.includes('connection refused') || msg.includes('econnrefused')) {
    return 'Server refused the connection — the VPN service may be down or the port is blocked'
  }
  if (msg.includes('no route to host') || msg.includes('network unreachable')) {
    return 'Server is unreachable — check your network or firewall'
  }

  // Tunnel-setup failures.
  if (msg.includes('did not come up') || msg.includes('timed out') || msg.includes('timeout')) {
    return 'Tunnel did not come up in time — check the Logs view for details'
  }
  if (msg.includes('tun')) {
    if (msg.includes('open') || msg.includes('create')) {
      return 'Could not open the TUN device — another VPN may already be active'
    }
  }

  // Permission / privilege failures.
  if (msg.includes('permission') || msg.includes('denied') || msg.includes('not permitted')) {
    return 'Permission denied — admin/root privileges are required for VPN setup'
  }
  if (msg.includes('helper') && (msg.includes('install') || msg.includes('reachable'))) {
    return 'Privileged helper is not installed — see Settings to install it'
  }

  // Config / import.
  if (msg.includes('parse') || msg.includes('invalid config') || msg.includes('malformed')) {
    return 'Configuration file could not be parsed — it may be corrupt or incomplete'
  }

  // Environmental.
  if (msg.includes('not found')) return 'VPN software is not installed on this system'
  if (msg.includes('no active protocol')) return 'No protocol configured — import a config first'
  if (msg.includes('not available')) return 'This protocol is not available on your system'
  if (msg.includes('already')) return 'A tunnel is already running — disconnect first'

  // Catch-all — use fallback instead of leaking raw Go error.
  return fallback
}

// SPEED_HISTORY_LEN is the number of bytes/sec samples retained for the
// sparkline chart. 30 samples at 2s poll interval = 60s of history,
// enough to show a meaningful trend without being visually noisy.
const SPEED_HISTORY_LEN = 30

export const useVpnStore = defineStore('vpn', () => {
  const status = ref<any>(null)
  const protocols = ref<any[]>([])
  const version = ref('')
  const loading = ref(false)
  const error = ref('')

  // Rolling speed history in bytes/second, computed from successive
  // bytes_rx / bytes_tx deltas. Each array is a fixed-length ring
  // buffer of the most-recent SPEED_HISTORY_LEN samples. Oldest first,
  // newest last — echarts renders them in index order.
  const rxSpeedHistory = ref<number[]>(Array(SPEED_HISTORY_LEN).fill(0))
  const txSpeedHistory = ref<number[]>(Array(SPEED_HISTORY_LEN).fill(0))

  let lastBytesRx = 0
  let lastBytesTx = 0
  let lastSampleAt = 0

  // updateSpeedSamples pushes one new pair into the ring buffers.
  // Deltas are divided by the elapsed wall-clock time (not the nominal
  // poll interval) to cope with missed polls, backgrounded apps, etc.
  // A negative delta (counter wrap or disconnect reset) is clamped to
  // zero so the chart does not dip into negatives.
  function updateSpeedSamples(newStatus: any) {
    if (!newStatus) return
    const connected = !!newStatus.connected
    const now = Date.now()
    const rxBytes = Number(newStatus.bytes_rx || 0)
    const txBytes = Number(newStatus.bytes_tx || 0)

    if (!connected) {
      // Reset on disconnect so the sparkline goes flat immediately
      // instead of holding a stale spike when the user reconnects.
      rxSpeedHistory.value = Array(SPEED_HISTORY_LEN).fill(0)
      txSpeedHistory.value = Array(SPEED_HISTORY_LEN).fill(0)
      lastBytesRx = 0
      lastBytesTx = 0
      lastSampleAt = 0
      return
    }

    if (lastSampleAt === 0) {
      // First sample of this connected session: establish baseline.
      lastBytesRx = rxBytes
      lastBytesTx = txBytes
      lastSampleAt = now
      return
    }

    // Defensive guard against the "0 B/s glitch" the user reported.
    // The backend's protocol Status() readers can briefly report
    // bytes_rx=0 / bytes_tx=0 when:
    //   - swanctl --list-sas runs during a CHILD-SA rekey (no in/out
    //     lines in the output, parseSwanctlBytes sums to 0)
    //   - OpenVPN's ByteCountListener desync window after reconnect
    //   - WireGuard tunnel briefly down between handshakes
    // The previous code clamped the resulting negative delta to 0
    // via Math.max(0,…) BUT still wrote rxBytes (= 0) into
    // lastBytesRx, corrupting the baseline. Next real sample then
    // computed (real_now - 0) → spike → ring buffer cycled
    // 0 / spike / 0 / spike, which the user perceives as "Traffic
    // 0 B/s" alternating since the ring-buffer mean is what the
    // sparkline area maps to.
    //
    // Treat any non-monotonic counter regression as "transient
    // backend hiccup, ignore this sample" — keep lastBytesRx +
    // lastSampleAt unchanged so the NEXT real reading produces a
    // delta against the last good baseline. Push 0 to the ring
    // buffer for that interval (no traffic-data-known is the same
    // visual as no-traffic-flowing — a flat second on the chart),
    // but DON'T mutate the baseline.
    if (rxBytes < lastBytesRx || txBytes < lastBytesTx) {
      rxSpeedHistory.value = [...rxSpeedHistory.value.slice(1), 0]
      txSpeedHistory.value = [...txSpeedHistory.value.slice(1), 0]
      return
    }

    const elapsedSec = Math.max(0.001, (now - lastSampleAt) / 1000)
    const rxSpeed = Math.max(0, (rxBytes - lastBytesRx) / elapsedSec)
    const txSpeed = Math.max(0, (txBytes - lastBytesTx) / elapsedSec)

    rxSpeedHistory.value = [...rxSpeedHistory.value.slice(1), rxSpeed]
    txSpeedHistory.value = [...txSpeedHistory.value.slice(1), txSpeed]

    lastBytesRx = rxBytes
    lastBytesTx = txBytes
    lastSampleAt = now
  }

  async function fetchStatus() {
    try {
      const newStatus = await Status()
      status.value = newStatus
      updateSpeedSamples(newStatus)
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
      updateSpeedSamples(data)
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
    rxSpeedHistory,
    txSpeedHistory,
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

// formatSpeed renders bytes/second as a short, readable string: "B/s",
// "KB/s", "MB/s". Mirrors the ByteCount formatter used elsewhere in
// the app. Exported so SpeedSparkline and ConnectionView can share.
export function formatSpeed(bps: number): string {
  if (!bps || bps < 1) return '0 B/s'
  if (bps < 1024) return `${Math.round(bps)} B/s`
  if (bps < 1024 * 1024) return `${(bps / 1024).toFixed(1)} KB/s`
  if (bps < 1024 * 1024 * 1024) return `${(bps / 1024 / 1024).toFixed(1)} MB/s`
  return `${(bps / 1024 / 1024 / 1024).toFixed(1)} GB/s`
}
