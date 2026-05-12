<template>
  <div class="p-4 overflow-y-auto max-h-[calc(100vh-7rem)]">
    <div class="flex items-center justify-between mb-4">
      <h2 class="text-sm font-semibold text-gray-600 dark:text-gray-300">Connections</h2>
      <div class="flex items-center gap-3">
        <!-- QR code scan — sits next to the cloud/gateway icon to
             group the two "import from outside" actions visually.
             Mirrors the Android ConnectionsScreen placement. -->
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
          @click="toggleRemoteConfigs"
          class="text-xs text-primary-400 hover:text-primary-300 flex items-center gap-1"
        >
          <CloudArrowDownIcon class="w-4 h-4" />
          Gateway
        </button>
        <router-link to="/add-pool" class="text-xs text-primary-400 hover:text-primary-300 flex items-center gap-1" title="Connection Pool from a ZIP or many config files">
          <RectangleStackIcon class="w-4 h-4" />
          Add Pool
        </router-link>
        <router-link to="/add" class="text-xs text-primary-400 hover:text-primary-300 flex items-center gap-1">
          <PlusIcon class="w-4 h-4" />
          Add
        </router-link>
      </div>
    </div>

    <!-- Pools section: rendered above Singles when any exist. Pool is
         a virtual connection - selection works the same as singles:
         clicking the row activates the pool (mutual exclusion with
         the singles' activeId is enforced backend-side). Re-clicking
         an already-active pool navigates to the Connect screen, same
         pattern as selectConnection() for singles. The pencil icon
         opens the detail/edit view. -->
    <div v-if="poolStore.pools.length > 0" class="mb-4 space-y-2">
      <h3 class="text-[11px] font-semibold text-gray-500 dark:text-gray-400 mb-1">Connection Pools</h3>
      <div
        v-for="p in poolStore.pools"
        :key="p.id"
        class="card p-3 border-2 cursor-pointer transition-all group"
        :class="p.is_active ? 'border-primary-500 bg-primary-500/5' : 'border-transparent hover:border-gray-300 dark:hover:border-gray-600'"
        @click="selectPool(p)"
      >
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2 min-w-0">
            <div class="w-2 h-2 rounded-full flex-shrink-0"
              :class="p.is_active ? 'bg-primary-400' : 'bg-gray-600'">
            </div>
            <RectangleStackIcon class="w-5 h-5 text-primary-400 flex-shrink-0" />
            <div class="min-w-0">
              <p class="text-sm truncate"
                :class="p.is_active ? 'text-primary-300' : 'text-gray-900 dark:text-white'">
                {{ p.name }}
              </p>
              <p class="text-[10px] text-gray-500">
                Pool · {{ p.member_count }} server<span v-if="p.member_count !== 1">s</span> · {{ policyShort(p.policy) }}
                <span v-if="p.is_active && p.active_member_name" class="ml-1 text-primary-400">→ {{ p.active_member_name }}</span>
              </p>
            </div>
          </div>
          <div class="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
            <button
              @click.stop="$router.push(`/pool/${p.id}`)"
              class="p-1.5 text-gray-400 hover:text-primary-400"
              title="Edit pool"
            >
              <PencilIcon class="w-4 h-4" />
            </button>
            <button
              @click.stop="confirmDeletePool(p)"
              class="p-1.5 text-gray-400 hover:text-red-400"
              title="Delete pool"
            >
              <TrashIcon class="w-4 h-4" />
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Remote configs panel -->
    <div v-if="showRemoteConfigs" class="card p-3 mb-4 border border-primary-500/30">
      <div class="flex items-center justify-between mb-2">
        <span class="text-xs font-semibold text-gray-500 dark:text-gray-400">Gateway Configs</span>
        <button @click="loadRemoteConfigs" :disabled="loadingRemote" class="text-[10px] text-primary-400 hover:text-primary-300">
          {{ loadingRemote ? 'Loading...' : 'Refresh' }}
        </button>
      </div>
      <p v-if="remoteError" class="text-[10px] text-red-400 mb-2">{{ remoteError }}</p>
      <div v-if="remoteConfigs.length === 0 && !loadingRemote" class="text-[10px] text-gray-500 text-center py-2">
        No configs available. Click Refresh.
      </div>
      <div v-else class="space-y-1.5 max-h-48 overflow-y-auto">
        <div
          v-for="rc in remoteConfigs"
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
            @click.stop="downloadRemoteConfig(rc)"
            :disabled="downloadingId === rc.protocol + '-' + rc.id"
            class="text-[10px] px-2 py-1 rounded bg-primary-600 text-white hover:bg-primary-700 disabled:opacity-50 flex-shrink-0"
          >
            {{ downloadingId === rc.protocol + '-' + rc.id ? '...' : 'Import' }}
          </button>
        </div>
      </div>
    </div>

    <!-- Error banner — independent v-if so a sticky actionError does NOT
         hide the connection cards below. Pre-fix this `<p>` was the root
         of a v-if/v-else-if/v-else chain that included the empty state
         AND the connection cards; any non-empty actionError therefore
         removed all connection cards from the DOM, leaving the user with
         a layout that LOOKED like the listbox but had no click targets
         — every click hit empty space, no @click handler, nothing fired,
         no log line. Dead-locked: actionError stayed sticky because the
         user could not click anything to trigger a successful action
         that would clear it. This bug was the root cause of the v0.9.14.34
         user report: "kann aber nicht zwischen den beiden umschalten ...
         auch wenn ich in der listbox die connection wähle wird wieder
         die vpn pool aktiviert". -->
    <p v-if="actionError" class="text-xs text-red-400 text-center mb-3 bg-red-500/10 rounded-lg py-2 px-3">{{ actionError }}</p>

    <!-- Loading / empty / cards — separate v-if chain, NOT chained off
         actionError, so the cards always render when connections exist. -->
    <div v-if="loadingConnections" class="text-center mt-12">
      <div class="w-6 h-6 mx-auto border-2 border-primary-400 border-t-transparent rounded-full animate-spin mb-3"></div>
      <p class="text-xs text-gray-500">Loading connections...</p>
    </div>

    <div v-else-if="connections.length === 0" class="text-center mt-12">
      <DocumentTextIcon class="w-12 h-12 mx-auto text-gray-600 mb-3" />
      <p class="text-sm text-gray-500 dark:text-gray-400 mb-4">No saved connections</p>
      <router-link to="/add" class="btn-primary px-4 py-2 text-xs">
        Import Config
      </router-link>
    </div>

    <div v-else class="space-y-3">
      <!-- Connection Card — click anywhere to SELECT (not connect) -->
      <div
        v-for="conn in connections"
        :key="conn.id"
        @click="selectConnection(conn.id)"
        class="card p-4 transition-all border-2 cursor-pointer"
        :class="isSelected(conn.id)
          ? 'border-primary-500 bg-primary-500/5'
          : 'border-transparent hover:border-gray-300 dark:hover:border-gray-600'"
      >
        <!-- Header: status dot + name + actions -->
        <div class="flex items-center justify-between mb-2">
          <div class="flex items-center gap-2 min-w-0">
            <!-- Status dot -->
            <div class="w-2 h-2 rounded-full flex-shrink-0"
              :class="isConnected(conn.id) ? 'bg-green-400 animate-pulse' : isSelected(conn.id) ? 'bg-primary-400' : 'bg-gray-600'">
            </div>
            <!-- Name (editable) -->
            <h3
              v-if="editingId !== conn.id"
              @dblclick.stop="startRename(conn)"
              class="text-sm font-medium truncate"
              :class="isSelected(conn.id) ? 'text-primary-300' : 'text-gray-900 dark:text-white'"
              title="Double-click to rename"
            >{{ conn.name }}</h3>
            <input
              v-else
              v-model="editName"
              @click.stop
              @blur="saveRename(conn.id)"
              @keyup.enter="saveRename(conn.id)"
              @keyup.escape="editingId = ''"
              maxlength="64"
              class="text-sm font-medium text-gray-900 dark:text-white bg-gray-100 dark:bg-gray-700 border border-primary-500 rounded px-1 py-0 w-full focus:outline-none"
              ref="renameInput"
            />
          </div>
          <div class="flex items-center gap-1.5 flex-shrink-0" @click.stop>
            <button
              @click="startRename(conn)"
              class="p-1 text-gray-500 hover:text-primary-400 transition-colors"
              title="Rename"
            >
              <PencilIcon class="w-3.5 h-3.5" />
            </button>
            <button
              @click="openDnsEdit(conn)"
              class="p-1 transition-colors"
              :class="conn.dns_override ? 'text-primary-400 hover:text-primary-300' : 'text-gray-500 hover:text-primary-400'"
              :title="conn.dns_override ? 'DNS Override: ' + conn.dns_override + ' (click to edit)' : 'Set per-connection DNS Override'"
            >
              <GlobeAltIcon class="w-3.5 h-3.5" />
            </button>
            <router-link
              :to="{ path: '/add', query: { connectionId: conn.id } }"
              class="p-1 text-gray-500 hover:text-primary-400 transition-colors"
              title="Add protocol"
            >
              <PlusIcon class="w-3.5 h-3.5" />
            </router-link>
            <button
              @click="remove(conn.id)"
              class="p-1 text-gray-500 hover:text-red-400 transition-colors"
              title="Delete"
            >
              <TrashIcon class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        <!-- Protocol badges — click to SELECT protocol (not connect).
             Hover shows an × to remove the protocol from this group,
             but only when the group has more than one protocol (removing
             the last one would delete the whole connection). -->
        <div class="flex flex-wrap gap-1.5 mb-2" @click.stop>
          <div
            v-for="pc in conn.protocols"
            :key="pc.protocol"
            class="group relative inline-flex items-center"
          >
            <button
              @click="selectProtocol(conn.id, pc.protocol)"
              class="inline-flex items-center gap-1 px-2.5 py-1 rounded-full text-[10px] font-medium transition-all"
              :class="isSelectedProtocol(conn.id, pc.protocol)
                ? protocolBadgeActive(pc.protocol)
                : 'bg-gray-200 dark:bg-gray-700/50 text-gray-500 hover:text-gray-700 dark:hover:text-gray-300 hover:bg-gray-200 dark:hover:bg-gray-700'"
            >
              <ProtocolIcon :protocol="pc.protocol" size="xs" />
              {{ protocolLabel(pc.protocol) }}
            </button>
            <button
              v-if="conn.protocols && conn.protocols.length > 1"
              @click="removeProtocol(conn.id, pc.protocol)"
              class="ml-0.5 opacity-0 group-hover:opacity-100 transition-opacity w-4 h-4 rounded-full bg-gray-300 dark:bg-gray-600 text-gray-700 dark:text-gray-200 hover:bg-red-500 hover:text-white flex items-center justify-center text-[9px] leading-none"
              :title="'Remove ' + protocolLabel(pc.protocol) + ' from this connection'"
            >
              ×
            </button>
          </div>
        </div>

        <!-- Server info -->
        <div v-if="activeConfig(conn)" class="text-[11px] text-gray-500">
          {{ activeConfig(conn)?.server_address || activeConfig(conn)?.filename }}
        </div>

        <!-- VPN IP — last-known inner address per protocol. WireGuard:
             parsed from the .conf at import (always current). OpenVPN /
             IPSec: server-pushed, captured after first connect, may be
             stale across server-side IP rotations. Hidden when none
             stored (fresh import that never connected, or static-IP-
             less .ovpn). -->
        <div v-if="activeConfig(conn)?.local_address" class="text-[11px] text-gray-500 font-mono">
          <span class="text-gray-400">VPN IP:</span> {{ activeConfig(conn)?.local_address }}
        </div>

        <!-- Connected indicator with protocol brand color -->
        <div v-if="isConnected(conn.id)" class="mt-2 flex items-center gap-1.5">
          <div class="w-1.5 h-1.5 rounded-full animate-pulse" :class="protocolDotColor(vpn.status?.active_protocol)"></div>
          <span class="text-[10px] font-medium" :class="protocolTextColor(vpn.status?.active_protocol)">
            Connected via {{ protocolLabel(vpn.status?.active_protocol) }}
          </span>
        </div>
      </div>
    </div>

    <QrScanModal
      v-if="showQrScanner"
      @scanned="handleQrScanned"
      @close="showQrScanner = false"
    />

    <!-- Per-connection DNS override modal. Single textfield with
         live invalid-entry validation. Empty = inherit Settings
         global. Resolution priority chain at connect time:
         active-pool > active-connection > global Settings. -->
    <div
      v-if="dnsEditTarget"
      class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4"
      @click.self="dnsEditTarget = null"
    >
      <div class="card p-4 max-w-md w-full">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white mb-1">
          DNS Override
        </h3>
        <p class="text-[11px] text-gray-500 mb-3">
          Per-connection DNS for "{{ dnsEditTarget.name }}". Empty inherits Settings global.
        </p>
        <DnsOverrideField
          v-model="dnsEditDraft"
          placeholder="e.g. 1.1.1.1, 2606:4700:4700::1111"
          @update:model-value="validateDnsEdit"
        />
        <p
          v-if="dnsEditError"
          class="text-[10px] text-red-400 mb-2"
        >{{ dnsEditError }}</p>
        <p
          v-else
          class="text-[10px] text-gray-500 mb-2"
        >
          Comma-separated IPv4/IPv6. Overrides Settings DNS for this connection only.
        </p>
        <div class="flex justify-end gap-2">
          <button
            @click="dnsEditTarget = null"
            class="px-3 py-1.5 text-xs text-gray-600 dark:text-gray-300 hover:text-gray-900 dark:hover:text-white"
          >
            Cancel
          </button>
          <button
            @click="saveDnsEdit"
            :disabled="!!dnsEditError"
            class="px-3 py-1.5 text-xs font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md disabled:opacity-50"
          >
            Save
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, nextTick, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useVpnStore } from '@/stores/vpn'
import { usePoolStore } from '@/stores/pool'
import { ListConnections, ActivateConnection, DeleteConnection, RenameConnection, FetchMyProfile, DownloadAndImportConfig, RemoveProtocolFromConnection, ImportConfig, GetSettings, UpdateSettings, SetConnectionDnsOverride, ValidateDnsOverride } from '../../wailsjs/go/main/App'
import { LogPrint } from '../../wailsjs/runtime/runtime'
import ProtocolIcon from '@/components/ProtocolIcon.vue'
import QrScanModal from '@/components/QrScanModal.vue'
import DnsOverrideField from '@/components/DnsOverrideField.vue'
import { parseQrPayload } from '@/util/qrPayload'
import {
  PlusIcon,
  PencilIcon,
  DocumentTextIcon,
  TrashIcon,
  CloudArrowDownIcon,
  QrCodeIcon,
  RectangleStackIcon,
  GlobeAltIcon,
} from '@heroicons/vue/24/outline'

const router = useRouter()
const vpn = useVpnStore()
const poolStore = usePoolStore()

function policyShort(p: string): string {
  switch (p) {
    case 'geo-nearest': return 'Geo-Nearest'
    case 'random':      return 'Random'
    case 'round-robin-region': return 'Round-Robin'
  }
  return p
}

async function usePool(id: string) {
  await poolStore.activate(id)
  router.push('/connection')
}

async function selectPool(p: any) {
  // Re-clicking an already-active pool jumps to the Connect screen,
  // mirroring selectConnection's "already selected → go to Connect"
  // behaviour for singles.
  if (p.is_active) {
    router.push('/connection')
    return
  }
  try {
    await poolStore.activate(p.id)
    await loadConnections()
  } catch (e: any) {
    actionError.value = e?.toString() || 'Failed to activate pool'
  }
}

async function confirmDeletePool(p: any) {
  if (!confirm(`Delete pool "${p.name}"? This cannot be undone.`)) return
  try {
    await poolStore.remove(p.id)
    await loadConnections()
  } catch (e: any) {
    actionError.value = e?.toString() || 'Failed to delete pool'
  }
}
const connections = ref<any[]>([])
const showQrScanner = ref(false)

// Route a scanned QR payload into the right import path. Raw WG
// configs go straight through ImportConfig so the user doesn't need
// another click; Privycs enrollment URLs store the gateway
// credentials and open the gateway panel so the user can pick which
// protocol to import.
async function handleQrScanned(raw: string) {
  showQrScanner.value = false
  const payload = parseQrPayload(raw)
  try {
    if (payload.kind === 'wireguard') {
      await ImportConfig(payload.content, 'scanned', 'scanned.conf')
      await loadConnections()
    } else if (payload.kind === 'privycs') {
      if (payload.gatewayUrl && payload.apiKey) {
        const s = await GetSettings()
        s.gateway_url = payload.gatewayUrl
        s.api_key = payload.apiKey
        await UpdateSettings(s)
      }
      showRemoteConfigs.value = true
      await loadRemoteConfigs()
    } else {
      actionError.value = 'QR content not recognised as a VPN config or Privycs enrollment URL'
    }
  } catch (e: any) {
    actionError.value = `QR import failed: ${e}`
  }
}
const editingId = ref('')
const editName = ref('')
const renameInput = ref<HTMLInputElement | null>(null)
const loadingConnections = ref(false)
const actionError = ref('')

// Remote config state
const hasApiKey = ref(!!localStorage.getItem('privycs-api-user'))
const showRemoteConfigs = ref(false)
const remoteConfigs = ref<any[]>([])
const loadingRemote = ref(false)
const remoteError = ref('')
const downloadingId = ref('')

function toggleRemoteConfigs() {
  showRemoteConfigs.value = !showRemoteConfigs.value
  if (showRemoteConfigs.value && remoteConfigs.value.length === 0) {
    loadRemoteConfigs()
  }
}

async function loadRemoteConfigs() {
  loadingRemote.value = true
  remoteError.value = ''
  try {
    const profile = await FetchMyProfile()
    remoteConfigs.value = profile.configs || []
    localStorage.setItem('privycs-api-user', profile.user)
    hasApiKey.value = true
  } catch (e: any) {
    remoteError.value = e?.toString()?.replace('Error: ', '') || 'Failed to connect'
    remoteConfigs.value = []
  } finally {
    loadingRemote.value = false
  }
}

async function downloadRemoteConfig(rc: any) {
  const key = rc.protocol + '-' + rc.id
  downloadingId.value = key
  try {
    await DownloadAndImportConfig(rc.protocol, rc.id, rc.peer_name, '')
    await loadConnections()
    // Remove from remote list after successful import
    remoteConfigs.value = remoteConfigs.value.filter(c => (c.protocol + '-' + c.id) !== key)
  } catch (e: any) {
    actionError.value = 'Import failed: ' + (e?.toString()?.replace('Error: ', '') || 'Unknown error')
  } finally {
    downloadingId.value = ''
  }
}

async function loadConnections() {
  loadingConnections.value = true
  try {
    connections.value = await ListConnections() || []
  } catch {
    connections.value = []
  } finally {
    loadingConnections.value = false
  }
}

// Selected = this connection is chosen (shown on Connect screen)
function isSelected(id: string): boolean {
  return vpn.status?.connection_id === id
}

// Connected = this connection is selected AND tunnel is running
function isConnected(id: string): boolean {
  return isSelected(id) && vpn.status?.connected === true
}

// Selected protocol within a connection
function isSelectedProtocol(connId: string, protocol: string): boolean {
  return isSelected(connId) && vpn.status?.active_protocol === protocol
}

function activeConfig(conn: any): any {
  if (!conn.protocols) return null
  return conn.protocols.find((p: any) => p.protocol === conn.active_protocol) || conn.protocols[0]
}

// SELECT a connection (activate it, but don't connect)
async function selectConnection(connId: string) {
  try {
    LogPrint(`selectConnection: connId=${connId} isSelected=${isSelected(connId)} vpn.status.connection_id=${vpn.status?.connection_id} poolStore.activePoolId=${poolStore.activePoolId}`)
  } catch {}
  // The early-return must ALSO check !poolStore.activePoolId. Pre-fix
  // it was just `isSelected(connId)` which returned true whenever
  // `vpn.status.connection_id === connId` — but a pool can be active
  // alongside a non-empty connection_id (corrupt mutual-exclusion
  // state from earlier switch failures). In that case the user
  // clicks the single to switch AWAY from the pool, but isSelected
  // returns true → we router.push and do nothing → backend never
  // gets the switch → pool keeps running. Mirrors the dropdown
  // pickConnection guard which has had the !poolStore.activePoolId
  // half forever.
  if (isSelected(connId) && !poolStore.activePoolId) {
    try { LogPrint(`selectConnection: returning early — already selected and no pool active, navigating to /connection`) } catch {}
    router.push('/connection')
    return
  }
  actionError.value = ''
  try {
    // Get the connection's active protocol
    const conn = connections.value.find(c => c.id === connId)
    const proto = conn?.active_protocol || conn?.protocols?.[0]?.protocol || ''
    try { LogPrint(`selectConnection: dispatching ActivateConnection(${connId}, ${proto})`) } catch {}
    await ActivateConnection(connId, proto)
    // Refresh pool store too. Backend ActivateConnection clears
    // activePoolID + persists pools.SetActiveID("") but the frontend
    // pool store cached the previous "is_active=true" rendering until
    // we explicitly re-fetch. Without this, the user picked a single
    // connection in the listbox and the previously-active pool stayed
    // visually highlighted with its primary-border + "active member"
    // badge, looking like "the switch did not happen". Mirror the
    // ConnectionView dropdown's three-call refresh pattern.
    await Promise.all([vpn.fetchStatus(), loadConnections(), poolStore.refresh()])
  } catch (e: any) {
    actionError.value = 'Failed to select connection'
  }
}

// SELECT a protocol within a connection (don't connect)
async function selectProtocol(connId: string, protocol: string) {
  actionError.value = ''
  try {
    await ActivateConnection(connId, protocol)
    if (vpn.status) {
      vpn.status.active_protocol = protocol
      vpn.status.connection_id = connId
    }
    // Same poolStore.refresh() reason as selectConnection above —
    // ActivateConnection clears the active pool backend-side and
    // the listbox needs to re-render without the stale pool highlight.
    await Promise.all([loadConnections(), poolStore.refresh()])
  } catch (e: any) {
    actionError.value = 'Failed to switch protocol'
  }
}

async function remove(id: string) {
  actionError.value = ''
  try {
    await DeleteConnection(id)
    await loadConnections()
    await vpn.fetchStatus()
  } catch (e: any) {
    actionError.value = 'Failed to delete connection'
  }
}

async function removeProtocol(connId: string, protocol: string) {
  actionError.value = ''
  try {
    await RemoveProtocolFromConnection(connId, protocol)
    await loadConnections()
    await vpn.fetchStatus()
  } catch (e: any) {
    actionError.value = 'Failed to remove protocol'
  }
}

function protocolLabel(proto: string): string {
  switch (proto) {
    case 'amneziawg': return 'AmneziaWG'
    case 'wireguard': return 'WireGuard'
    case 'openvpn': return 'OpenVPN'
    case 'ipsec': return 'IPSec'
    default: return proto || ''
  }
}

async function startRename(conn: any) {
  editingId.value = conn.id
  editName.value = conn.name
  await nextTick()
  if (renameInput.value) {
    renameInput.value.focus()
    renameInput.value.select()
  }
}

// Per-connection DNS override modal state. Live validation
// against the backend ValidateDnsOverride so a typo gets
// flagged before the user clicks Save - mirrors Settings
// view's DNS-Override validation.
const dnsEditTarget = ref<any>(null)
const dnsEditDraft = ref('')
const dnsEditError = ref('')

function openDnsEdit(conn: any) {
  dnsEditTarget.value = conn
  dnsEditDraft.value = conn.dns_override || ''
  dnsEditError.value = ''
}

async function validateDnsEdit() {
  const raw = dnsEditDraft.value.trim()
  if (!raw) { dnsEditError.value = ''; return }
  try {
    const bad = (await ValidateDnsOverride(raw)) as string[]
    dnsEditError.value = bad && bad.length ? `Invalid: ${bad.join(', ')}` : ''
  } catch (e) {
    console.error('DNS validate failed:', e)
  }
}

async function saveDnsEdit() {
  if (dnsEditError.value || !dnsEditTarget.value) return
  try {
    await SetConnectionDnsOverride(dnsEditTarget.value.id, dnsEditDraft.value.trim())
    await loadConnections()
    dnsEditTarget.value = null
  } catch (e: any) {
    actionError.value = 'Failed to save DNS override'
  }
}

async function saveRename(id: string) {
  if (editingId.value !== id) return
  const newName = editName.value.trim()
  if (newName && newName !== '') {
    try {
      await RenameConnection(id, newName)
      await loadConnections()
      await vpn.fetchStatus()
    } catch (e: any) {
      actionError.value = 'Failed to rename connection'
    }
  }
  editingId.value = ''
}

function protocolTextColor(proto: string): string {
  switch (proto) {
    case 'amneziawg': return 'text-indigo-400'
    case 'wireguard': return 'text-red-400'
    case 'openvpn': return 'text-orange-400'
    case 'ipsec': return 'text-blue-400'
    default: return 'text-primary-400'
  }
}

function protocolDotColor(proto: string): string {
  switch (proto) {
    case 'amneziawg': return 'bg-indigo-400'
    case 'wireguard': return 'bg-red-400'
    case 'openvpn': return 'bg-orange-400'
    case 'ipsec': return 'bg-blue-400'
    default: return 'bg-primary-400'
  }
}

function protocolBadgeActive(proto: string): string {
  switch (proto) {
    case 'amneziawg': return 'bg-indigo-500/20 text-indigo-300 ring-1 ring-indigo-500/30'
    case 'wireguard': return 'bg-red-900/20 text-red-300 ring-1 ring-red-500/30'
    case 'openvpn': return 'bg-orange-500/20 text-orange-300 ring-1 ring-orange-500/30'
    case 'ipsec': return 'bg-blue-500/20 text-blue-300 ring-1 ring-blue-500/30'
    default: return 'bg-gray-500/20 text-gray-600 dark:text-gray-300'
  }
}

onMounted(() => {
  loadConnections()
  poolStore.refresh()
})
</script>
