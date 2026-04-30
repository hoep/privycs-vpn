<template>
  <div class="p-4 overflow-y-auto max-h-[calc(100vh-4rem)]">
    <!-- Header with back + add -->
    <div class="flex items-center justify-between mb-4">
      <div class="flex items-center gap-2">
        <button @click="$router.back()" class="text-gray-500 dark:text-gray-400 hover:text-gray-900 dark:hover:text-white">
          <ArrowLeftIcon class="w-5 h-5" />
        </button>
        <h2 class="text-sm font-semibold text-gray-900 dark:text-white">Network Rules</h2>
      </div>
      <button
        @click="openCreate"
        class="flex items-center gap-1 text-xs text-primary-400 hover:text-primary-300"
      >
        <PlusIcon class="w-4 h-4" /> Add Rule
      </button>
    </div>

    <!-- Empty-state explainer / how rules interact with legacy COD -->
    <div v-if="rules.length === 0" class="card p-4 mb-4 bg-gray-50 dark:bg-gray-800/30">
      <h3 class="text-xs font-semibold text-gray-700 dark:text-gray-300 mb-1">No rules defined</h3>
      <p class="text-[11px] text-gray-500 leading-relaxed">
        When empty, the legacy <strong>Connect-on-Demand</strong> trigger + SSID list
        from Settings drives the lifecycle. Add a rule to take fine-grained control:
        per-SSID / per-BSSID / per-transport routing to a specific Pool, Connection,
        or No VPN.
      </p>
    </div>

    <!-- Rule list. List position = priority order, first match wins. -->
    <div v-else class="space-y-2">
      <div
        v-for="(rule, i) in rules"
        :key="rule.id"
        class="card p-3"
        :class="rule.enabled ? '' : 'opacity-50'"
      >
        <div class="flex items-start justify-between gap-2">
          <div class="flex-1 min-w-0">
            <div class="text-sm text-gray-800 dark:text-gray-200 truncate">
              {{ ruleSummary(rule) }}
            </div>
            <div v-if="rule.name" class="text-[10px] text-gray-500 mt-0.5">{{ rule.name }}</div>
          </div>
          <label class="flex items-center cursor-pointer">
            <input
              type="checkbox"
              :checked="rule.enabled"
              @change="toggleEnabled(rule)"
              class="sr-only peer"
            />
            <div class="w-8 h-4 bg-gray-300 dark:bg-gray-700 rounded-full peer-checked:bg-primary-500 transition-colors relative">
              <div class="absolute top-0.5 left-0.5 w-3 h-3 bg-white rounded-full transition-transform"
                   :class="rule.enabled ? 'translate-x-4' : ''"></div>
            </div>
          </label>
        </div>
        <div class="flex items-center gap-1 mt-2">
          <button @click="moveUp(i)" :disabled="i === 0"
                  class="p-1 text-gray-500 hover:text-primary-400 disabled:opacity-30">
            <ArrowUpIcon class="w-3.5 h-3.5" />
          </button>
          <button @click="moveDown(i)" :disabled="i === rules.length - 1"
                  class="p-1 text-gray-500 hover:text-primary-400 disabled:opacity-30">
            <ArrowDownIcon class="w-3.5 h-3.5" />
          </button>
          <span class="ml-2 text-[10px] text-gray-500">priority {{ i + 1 }}</span>
          <div class="flex-1"></div>
          <button @click="openEdit(rule)" class="p-1 text-gray-500 hover:text-primary-400">
            <PencilIcon class="w-3.5 h-3.5" />
          </button>
          <button @click="remove(rule)" class="p-1 text-gray-500 hover:text-red-400">
            <TrashIcon class="w-3.5 h-3.5" />
          </button>
        </div>
      </div>
    </div>

    <!-- Edit / Create modal -->
    <div
      v-if="editing"
      class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4"
      @click.self="editing = null"
    >
      <div class="card p-4 max-w-md w-full">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white mb-3">
          {{ editing.id ? 'Edit Rule' : 'New Rule' }}
        </h3>
        <div class="space-y-3">
          <div>
            <label class="text-[11px] text-gray-500 block mb-1">Name (optional)</label>
            <input v-model="draft.name" type="text" placeholder="Home / Office / Public"
                   class="input" />
          </div>
          <div>
            <label class="text-[11px] text-gray-500 block mb-1">Match by</label>
            <select v-model="draft.match_type" class="input">
              <option value="ssid_exact">Wi-Fi SSID (exact)</option>
              <option value="ssid_pattern">Wi-Fi SSID (pattern *, ?)</option>
              <option value="network_type">Network type</option>
              <option value="bssid">Wi-Fi BSSID (MAC)</option>
              <option value="any">Any network</option>
            </select>
          </div>
          <div v-if="draft.match_type !== 'any'">
            <label class="text-[11px] text-gray-500 block mb-1">{{ matchValueLabel }}</label>
            <input v-model="draft.match_value" type="text" :placeholder="matchValueHint"
                   class="input" />
          </div>
          <div>
            <label class="text-[11px] text-gray-500 block mb-1">Action</label>
            <select v-model="draft.action" @change="resetTarget" class="input">
              <option value="no_vpn">No VPN (trusted)</option>
              <option value="pool">Use Pool</option>
              <option value="connection">Use Connection</option>
            </select>
          </div>
          <div v-if="draft.action === 'pool'">
            <label class="text-[11px] text-gray-500 block mb-1">Pool</label>
            <select v-model="draft.target_id" class="input">
              <option value="">Pick a pool…</option>
              <option v-for="p in pools" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
          </div>
          <div v-else-if="draft.action === 'connection'">
            <label class="text-[11px] text-gray-500 block mb-1">Connection</label>
            <select v-model="draft.target_id" class="input">
              <option value="">Pick a connection…</option>
              <option v-for="c in connections" :key="c.id" :value="c.id">{{ c.name }}</option>
            </select>
          </div>
        </div>
        <div class="flex justify-end gap-2 mt-4">
          <button @click="editing = null" class="px-3 py-1.5 text-xs text-gray-500 hover:text-gray-700">
            Cancel
          </button>
          <button
            @click="save"
            :disabled="!canSave"
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
import { ref, computed, onMounted } from 'vue'
import {
  ListNetworkRules, AddNetworkRule, UpdateNetworkRule, DeleteNetworkRule,
  SetNetworkRulesOrder, ListPools, ListConnections,
} from '../../wailsjs/go/main/App'
import {
  ArrowLeftIcon, PlusIcon, PencilIcon, TrashIcon,
  ArrowUpIcon, ArrowDownIcon,
} from '@heroicons/vue/24/outline'

interface NetworkRule {
  id: string
  priority: number
  match_type: string
  match_value: string
  action: string
  target_id: string
  enabled: boolean
  name: string
}

const rules = ref<NetworkRule[]>([])
const pools = ref<any[]>([])
const connections = ref<any[]>([])
const editing = ref<NetworkRule | null>(null)
const draft = ref<NetworkRule>(emptyRule())

function emptyRule(): NetworkRule {
  return {
    id: '', priority: 0,
    match_type: 'ssid_exact', match_value: '',
    action: 'no_vpn', target_id: '',
    enabled: true, name: '',
  }
}

const matchValueLabel = computed(() => {
  switch (draft.value.match_type) {
    case 'ssid_exact': return 'SSID'
    case 'ssid_pattern': return 'SSID pattern (use * and ?)'
    case 'network_type': return 'wifi / mobile / ethernet / wifi_mobile / any'
    case 'bssid': return 'BSSID (e.g. aa:bb:cc:dd:ee:ff)'
    default: return ''
  }
})

const matchValueHint = computed(() => {
  switch (draft.value.match_type) {
    case 'ssid_exact': return 'HomeWifi'
    case 'ssid_pattern': return 'Cafe-*'
    case 'network_type': return 'wifi'
    case 'bssid': return 'aa:bb:cc:dd:ee:ff'
    default: return ''
  }
})

const canSave = computed(() => {
  if (draft.value.match_type !== 'any' && !draft.value.match_value.trim()) return false
  if (draft.value.action !== 'no_vpn' && !draft.value.target_id) return false
  return true
})

function ruleSummary(rule: NetworkRule): string {
  let m = ''
  switch (rule.match_type) {
    case 'ssid_exact': m = `SSID = "${rule.match_value}"`; break
    case 'ssid_pattern': m = `SSID matches "${rule.match_value}"`; break
    case 'network_type': m = `Network = ${rule.match_value}`; break
    case 'bssid': m = `BSSID = ${rule.match_value}`; break
    case 'any': m = 'Any network'; break
  }
  let t = ''
  switch (rule.action) {
    case 'no_vpn': t = '→ No VPN'; break
    case 'pool': {
      const p = pools.value.find((x: any) => x.id === rule.target_id)
      t = `→ Pool: ${p?.name ?? '(missing)'}`; break
    }
    case 'connection': {
      const c = connections.value.find((x: any) => x.id === rule.target_id)
      t = `→ Connection: ${c?.name ?? '(missing)'}`; break
    }
  }
  return `${m}  ${t}`
}

async function load() {
  rules.value = (await ListNetworkRules()) as NetworkRule[] || []
  pools.value = (await ListPools()) as any[] || []
  connections.value = (await ListConnections()) as any[] || []
}

function openCreate() {
  draft.value = emptyRule()
  editing.value = draft.value
}

function openEdit(rule: NetworkRule) {
  draft.value = { ...rule }
  editing.value = draft.value
}

function resetTarget() {
  draft.value.target_id = ''
}

async function save() {
  try {
    if (draft.value.id) {
      await UpdateNetworkRule(draft.value)
    } else {
      await AddNetworkRule(draft.value)
    }
    editing.value = null
    await load()
  } catch (e) {
    console.error('Save rule failed:', e)
  }
}

async function remove(rule: NetworkRule) {
  if (!confirm(`Delete rule "${ruleSummary(rule)}"?`)) return
  await DeleteNetworkRule(rule.id)
  await load()
}

async function toggleEnabled(rule: NetworkRule) {
  await UpdateNetworkRule({ ...rule, enabled: !rule.enabled })
  await load()
}

async function moveUp(i: number) {
  if (i <= 0) return
  const ids = rules.value.map((r) => r.id)
  ;[ids[i - 1], ids[i]] = [ids[i], ids[i - 1]]
  await SetNetworkRulesOrder(ids)
  await load()
}

async function moveDown(i: number) {
  if (i >= rules.value.length - 1) return
  const ids = rules.value.map((r) => r.id)
  ;[ids[i + 1], ids[i]] = [ids[i], ids[i + 1]]
  await SetNetworkRulesOrder(ids)
  await load()
}

onMounted(load)
</script>
