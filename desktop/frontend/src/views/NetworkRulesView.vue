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

    <!-- Empty-state explainer -->
    <div v-if="rules.length === 0" class="card p-4 mb-4 bg-gray-50 dark:bg-gray-800/30">
      <h3 class="text-xs font-semibold text-gray-700 dark:text-gray-300 mb-1">No rules defined</h3>
      <p class="text-[11px] text-gray-500 leading-relaxed">
        When empty, the legacy <strong>Connect-on-Demand</strong> trigger + SSID list
        from Settings drives the lifecycle. Add a rule to take fine-grained control:
        per-SSID / per-BSSID / per-transport routing to a specific Pool, Connection,
        or No VPN.
      </p>
    </div>

    <!-- Rule list -->
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
          <SwitchGroup>
            <Switch
              :model-value="rule.enabled"
              @update:model-value="(v: boolean) => toggleEnabled(rule, v)"
              :class="rule.enabled ? 'bg-primary-600' : 'bg-gray-300 dark:bg-gray-700'"
              class="relative inline-flex h-4 w-8 items-center rounded-full transition-colors flex-shrink-0"
            >
              <span
                :class="rule.enabled ? 'translate-x-4' : 'translate-x-0.5'"
                class="inline-block h-3 w-3 transform rounded-full bg-white transition-transform"
              />
            </Switch>
          </SwitchGroup>
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
          <button @click="confirmDelete(rule)" class="p-1 text-gray-500 hover:text-red-400">
            <TrashIcon class="w-3.5 h-3.5" />
          </button>
        </div>
      </div>
    </div>

    <!-- Edit / Create modal: HeadlessUI Dialog with smooth in/out -->
    <TransitionRoot :show="!!editing" as="template">
      <Dialog as="div" class="relative z-50" @close="cancel">
        <TransitionChild
          as="template"
          enter="duration-200 ease-out"
          enter-from="opacity-0"
          enter-to="opacity-100"
          leave="duration-150 ease-in"
          leave-from="opacity-100"
          leave-to="opacity-0"
        >
          <!-- Backdrop with pointer-events-none on leave so the
               click-through bug fixed in v0.9.11.54 doesn't return. -->
          <div class="fixed inset-0 bg-black/60 transition-opacity" :class="editing ? '' : 'pointer-events-none'" />
        </TransitionChild>
        <div class="fixed inset-0 overflow-y-auto">
          <div class="flex min-h-full items-center justify-center p-4">
            <TransitionChild
              as="template"
              enter="duration-200 ease-out"
              enter-from="opacity-0 scale-95"
              enter-to="opacity-100 scale-100"
              leave="duration-150 ease-in"
              leave-from="opacity-100 scale-100"
              leave-to="opacity-0 scale-95"
            >
              <DialogPanel class="w-full max-w-md transform overflow-hidden rounded-xl bg-white dark:bg-gray-900 shadow-2xl ring-1 ring-black/5 dark:ring-white/10">
                <div class="px-4 py-3 border-b border-gray-200 dark:border-gray-800">
                  <DialogTitle class="text-sm font-semibold text-gray-900 dark:text-white">
                    {{ editing?.id ? 'Edit Rule' : 'New Rule' }}
                  </DialogTitle>
                </div>
                <div class="p-4 space-y-3">
                  <div>
                    <label class="text-[11px] text-gray-500 block mb-1">Name (optional)</label>
                    <input v-model="draft.name" type="text" placeholder="Home / Office / Public"
                           class="input" />
                  </div>

                  <!-- HeadlessUI Listbox for match-type instead of native select -->
                  <div>
                    <label class="text-[11px] text-gray-500 block mb-1">Match by</label>
                    <Listbox v-model="draft.match_type">
                      <div class="relative">
                        <ListboxButton class="input flex justify-between items-center cursor-pointer text-left">
                          <span>{{ matchTypeLabel(draft.match_type) }}</span>
                          <ChevronUpDownIcon class="w-4 h-4 text-gray-400" />
                        </ListboxButton>
                        <ListboxOptions class="absolute z-10 mt-1 w-full overflow-auto rounded-lg bg-white dark:bg-gray-800 py-1 text-xs shadow-lg ring-1 ring-black/10 max-h-60">
                          <ListboxOption
                            v-for="opt in matchTypeOptions"
                            :key="opt.value"
                            :value="opt.value"
                            v-slot="{ active, selected }"
                            as="template"
                          >
                            <li :class="[active ? 'bg-primary-500/10' : '', 'px-3 py-1.5 cursor-pointer']">
                              <span :class="selected ? 'font-medium text-primary-500' : ''">{{ opt.label }}</span>
                            </li>
                          </ListboxOption>
                        </ListboxOptions>
                      </div>
                    </Listbox>
                  </div>

                  <div v-if="draft.match_type !== 'any'">
                    <label class="text-[11px] text-gray-500 block mb-1">{{ matchValueLabel }}</label>
                    <input v-model="draft.match_value" type="text" :placeholder="matchValueHint"
                           class="input" />
                  </div>

                  <div>
                    <label class="text-[11px] text-gray-500 block mb-1">Action</label>
                    <Listbox :model-value="draft.action" @update:model-value="onActionChange">
                      <div class="relative">
                        <ListboxButton class="input flex justify-between items-center cursor-pointer text-left">
                          <span>{{ actionLabel(draft.action) }}</span>
                          <ChevronUpDownIcon class="w-4 h-4 text-gray-400" />
                        </ListboxButton>
                        <ListboxOptions class="absolute z-10 mt-1 w-full overflow-auto rounded-lg bg-white dark:bg-gray-800 py-1 text-xs shadow-lg ring-1 ring-black/10 max-h-60">
                          <ListboxOption
                            v-for="opt in actionOptions"
                            :key="opt.value"
                            :value="opt.value"
                            v-slot="{ active, selected }"
                            as="template"
                          >
                            <li :class="[active ? 'bg-primary-500/10' : '', 'px-3 py-1.5 cursor-pointer']">
                              <span :class="selected ? 'font-medium text-primary-500' : ''">{{ opt.label }}</span>
                            </li>
                          </ListboxOption>
                        </ListboxOptions>
                      </div>
                    </Listbox>
                  </div>

                  <div v-if="draft.action === 'pool'">
                    <label class="text-[11px] text-gray-500 block mb-1">Pool</label>
                    <Listbox v-model="draft.target_id">
                      <div class="relative">
                        <ListboxButton class="input flex justify-between items-center cursor-pointer text-left">
                          <span>{{ targetLabel('pool', draft.target_id) }}</span>
                          <ChevronUpDownIcon class="w-4 h-4 text-gray-400" />
                        </ListboxButton>
                        <ListboxOptions class="absolute z-10 mt-1 w-full overflow-auto rounded-lg bg-white dark:bg-gray-800 py-1 text-xs shadow-lg ring-1 ring-black/10 max-h-60">
                          <ListboxOption
                            v-for="p in pools"
                            :key="p.id"
                            :value="p.id"
                            v-slot="{ active, selected }"
                            as="template"
                          >
                            <li :class="[active ? 'bg-primary-500/10' : '', 'px-3 py-1.5 cursor-pointer']">
                              <span :class="selected ? 'font-medium text-primary-500' : ''">{{ p.name }}</span>
                            </li>
                          </ListboxOption>
                        </ListboxOptions>
                      </div>
                    </Listbox>
                  </div>

                  <div v-else-if="draft.action === 'connection'">
                    <label class="text-[11px] text-gray-500 block mb-1">Connection</label>
                    <Listbox v-model="draft.target_id">
                      <div class="relative">
                        <ListboxButton class="input flex justify-between items-center cursor-pointer text-left">
                          <span>{{ targetLabel('connection', draft.target_id) }}</span>
                          <ChevronUpDownIcon class="w-4 h-4 text-gray-400" />
                        </ListboxButton>
                        <ListboxOptions class="absolute z-10 mt-1 w-full overflow-auto rounded-lg bg-white dark:bg-gray-800 py-1 text-xs shadow-lg ring-1 ring-black/10 max-h-60">
                          <ListboxOption
                            v-for="c in connections"
                            :key="c.id"
                            :value="c.id"
                            v-slot="{ active, selected }"
                            as="template"
                          >
                            <li :class="[active ? 'bg-primary-500/10' : '', 'px-3 py-1.5 cursor-pointer']">
                              <span :class="selected ? 'font-medium text-primary-500' : ''">{{ c.name }}</span>
                            </li>
                          </ListboxOption>
                        </ListboxOptions>
                      </div>
                    </Listbox>
                  </div>
                </div>
                <div class="flex justify-end gap-2 px-4 py-3 border-t border-gray-200 dark:border-gray-800 bg-gray-50 dark:bg-gray-800/30">
                  <button @click="cancel" class="px-3 py-1.5 text-xs text-gray-600 dark:text-gray-300 hover:text-gray-900 dark:hover:text-white">
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
              </DialogPanel>
            </TransitionChild>
          </div>
        </div>
      </Dialog>
    </TransitionRoot>

    <!-- Confirm-delete dialog (HeadlessUI). The native confirm()
         was the cause of the "windows aufgehen und verschwinden"
         flicker - some Wails / WebView2 builds render confirm()
         in a way that can re-enter the Vue render loop. -->
    <TransitionRoot :show="!!deleting" as="template">
      <Dialog as="div" class="relative z-50" @close="deleting = null">
        <div class="fixed inset-0 bg-black/60" />
        <div class="fixed inset-0 flex items-center justify-center p-4">
          <DialogPanel class="card p-4 max-w-sm w-full">
            <DialogTitle class="text-sm font-semibold text-gray-900 dark:text-white mb-2">
              Delete this rule?
            </DialogTitle>
            <p class="text-[11px] text-gray-500 mb-4">{{ deleting ? ruleSummary(deleting) : '' }}</p>
            <div class="flex justify-end gap-2">
              <button @click="deleting = null" class="px-3 py-1.5 text-xs text-gray-600 dark:text-gray-300 hover:text-gray-900 dark:hover:text-white">
                Cancel
              </button>
              <button @click="doDelete" class="px-3 py-1.5 text-xs font-medium text-white bg-red-600 hover:bg-red-700 rounded-md">
                Delete
              </button>
            </div>
          </DialogPanel>
        </div>
      </Dialog>
    </TransitionRoot>
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
  ArrowUpIcon, ArrowDownIcon, ChevronUpDownIcon,
} from '@heroicons/vue/24/outline'
import {
  TransitionRoot, TransitionChild, Dialog, DialogPanel, DialogTitle,
  Listbox, ListboxButton, ListboxOptions, ListboxOption,
  Switch, SwitchGroup,
} from '@headlessui/vue'

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
const deleting = ref<NetworkRule | null>(null)
const draft = ref<NetworkRule>(emptyRule())

const matchTypeOptions = [
  { value: 'ssid_exact', label: 'Wi-Fi SSID (exact)' },
  { value: 'ssid_pattern', label: 'Wi-Fi SSID (pattern *, ?)' },
  { value: 'network_type', label: 'Network type' },
  { value: 'bssid', label: 'Wi-Fi BSSID (MAC)' },
  { value: 'any', label: 'Any network' },
]

const actionOptions = [
  { value: 'no_vpn', label: 'No VPN (trusted)' },
  { value: 'pool', label: 'Use Pool' },
  { value: 'connection', label: 'Use Connection' },
]

function emptyRule(): NetworkRule {
  return {
    id: '', priority: 0,
    match_type: 'ssid_exact', match_value: '',
    action: 'no_vpn', target_id: '',
    enabled: true, name: '',
  }
}

function matchTypeLabel(v: string): string {
  return matchTypeOptions.find(o => o.value === v)?.label ?? v
}
function actionLabel(v: string): string {
  return actionOptions.find(o => o.value === v)?.label ?? v
}
function targetLabel(kind: 'pool' | 'connection', id: string): string {
  if (!id) return kind === 'pool' ? 'Pick a pool…' : 'Pick a connection…'
  if (kind === 'pool') {
    const p = pools.value.find((x: any) => x.id === id)
    return p?.name ?? '(missing)'
  }
  const c = connections.value.find((x: any) => x.id === id)
  return c?.name ?? '(missing)'
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

function onActionChange(v: string) {
  draft.value.action = v
  draft.value.target_id = ''
}

function cancel() {
  editing.value = null
}

async function save() {
  if (!canSave.value) return
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

function confirmDelete(rule: NetworkRule) {
  deleting.value = rule
}

async function doDelete() {
  if (!deleting.value) return
  await DeleteNetworkRule(deleting.value.id)
  deleting.value = null
  await load()
}

async function toggleEnabled(rule: NetworkRule, value: boolean) {
  await UpdateNetworkRule({ ...rule, enabled: value })
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
