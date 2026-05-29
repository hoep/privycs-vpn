<template>
  <div class="p-4 overflow-y-auto max-h-[calc(100vh-4rem)]">
    <!-- Header with back + add -->
    <div class="flex items-center justify-between mb-4">
      <div class="flex items-center gap-2">
        <button @click="$router.back()" class="text-gray-500 dark:text-gray-400 hover:text-gray-900 dark:hover:text-white">
          <ArrowLeftIcon class="w-5 h-5" />
        </button>
        <h2 class="text-sm font-semibold text-gray-900 dark:text-white">{{ $t('network-rules.title') }}</h2>
      </div>
      <button
        @click="openCreate"
        class="flex items-center gap-1 text-xs text-primary-400 hover:text-primary-300"
      >
        <PlusIcon class="w-4 h-4" /> {{ $t('network-rules.button.add-rule') }}
      </button>
    </div>

    <!-- v1.0.5: Master on/off — pinned at the top, primary-colored card
         when enabled so it reads as the first thing on the screen.
         Mirrors Android's MasterToggleCard. When OFF, the engine no-
         ops and no rule below fires; the user can still manually
         Connect/Disconnect from the Connect screen. -->
    <div
      class="card p-4 mb-3 border-2"
      :class="settings.network_rules_enabled
        ? 'bg-primary-500/10 border-primary-500/40'
        : 'bg-gray-50 dark:bg-gray-800/30 border-gray-300 dark:border-gray-700'"
    >
      <div class="flex items-center justify-between gap-3">
        <div class="flex-1 min-w-0">
          <h3
            class="text-sm font-semibold"
            :class="settings.network_rules_enabled
              ? 'text-primary-700 dark:text-primary-300'
              : 'text-text'"
          >
            {{ $t('network-rules.master.title') }}
          </h3>
          <!-- v1.0.5.1: subtitle colour pairs to the card's
               background. The original gray-500 was too low contrast
               on the primary-tinted card (light-mode mostly). Use
               primary tones with reduced contrast when the card is
               primary-tinted, gray when neutral. -->
          <p
            class="text-[11px] mt-1 leading-relaxed"
            :class="settings.network_rules_enabled
              ? 'text-primary-700/75 dark:text-primary-200/80'
              : 'text-gray-500 dark:text-gray-400'"
          >
            {{ settings.network_rules_enabled
              ? $t('network-rules.master.subtitle-on')
              : $t('network-rules.master.subtitle-off') }}
          </p>
        </div>
        <Switch
          :model-value="!!settings.network_rules_enabled"
          @update:model-value="onEngineToggle"
          :class="settings.network_rules_enabled ? 'toggle-enabled' : 'toggle-disabled'"
          class="toggle flex-shrink-0"
        >
          <span class="toggle-knob" :class="settings.network_rules_enabled ? 'translate-x-5' : 'translate-x-0'" />
        </Switch>
      </div>
    </div>

    <!-- Empty-state explainer -->
    <div v-if="rules.length === 0" class="card p-4 mb-3 bg-gray-50 dark:bg-gray-800/30">
      <h3 class="text-xs font-semibold text-gray-700 dark:text-gray-300 mb-1">{{ $t('network-rules.empty.heading') }}</h3>
      <p class="text-[11px] text-gray-500 leading-relaxed" v-html="$t('network-rules.empty.body')"></p>
    </div>

    <!-- Rule list -->
    <div v-else class="space-y-2 mb-3">
      <p class="text-[10px] font-semibold uppercase tracking-wider text-gray-400 px-0.5">
        {{ $t('network-rules.list.heading') }}
      </p>
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
          <span class="ml-2 text-[10px] text-gray-500">{{ $t('network-rules.list.priority', { n: i + 1 }) }}</span>
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

    <!-- v1.0.5.23: dynamic evaluation card — replaces the previous
         static fallback explainer. Subscribes (poll every 2 s) to
         GetCurrentNetworkRulesEval, renders the user's current
         engine decision in their own language. Mirrors Android's
         LiveEvalCard composable in NetworkRulesScreen.kt. -->
    <div class="card p-4 bg-gray-50 dark:bg-gray-800/30">
      <p class="text-[10px] font-semibold uppercase tracking-wider text-gray-400 mb-2">
        {{ $t('network-rules.eval.heading') }}
      </p>
      <p class="text-[13px] text-gray-700 dark:text-gray-200 font-semibold mb-1">
        {{ evalNetworkText }}
      </p>
      <p class="text-[11px] text-gray-500 mb-2">
        {{ evalMasterText }}
      </p>
      <p class="text-[12px] font-semibold text-primary-600 dark:text-primary-400">
        {{ $t('network-rules.eval.arrow') }} {{ evalDecisionText }}
      </p>
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
              <!-- overflow-visible (was overflow-hidden) so HeadlessUI
                   Listbox dropdowns inside the dialog (Action picker
                   especially) can extend past the dialog's bottom
                   edge. With overflow-hidden the third action option
                   "Use Connection" was clipped — user reported
                   "network rules erlauben bisher nur use pool" in
                   v0.9.14.4 because they could not see the third
                   option through the cropped dropdown. v0.9.14.5. -->
              <DialogPanel class="w-full max-w-md transform overflow-visible rounded-xl bg-white dark:bg-gray-900 shadow-2xl ring-1 ring-black/5 dark:ring-white/10">
                <div class="px-4 py-3 border-b border-gray-200 dark:border-gray-800">
                  <DialogTitle class="text-sm font-semibold text-gray-900 dark:text-white">
                    {{ editing?.id ? $t('network-rules.modal.edit-title') : $t('network-rules.modal.new-title') }}
                  </DialogTitle>
                </div>
                <div class="p-4 space-y-3">
                  <div>
                    <label class="text-[11px] text-gray-500 block mb-1">{{ $t('network-rules.field.name') }}</label>
                    <input v-model="draft.name" type="text" :placeholder="$t('network-rules.field.name-placeholder')"
                           class="input" />
                  </div>

                  <!-- HeadlessUI Listbox for match-type instead of native select -->
                  <div>
                    <label class="text-[11px] text-gray-500 block mb-1">{{ $t('network-rules.field.match-by') }}</label>
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
                    <label class="text-[11px] text-gray-500 block mb-1">{{ $t('network-rules.field.action') }}</label>
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
                    <label class="text-[11px] text-gray-500 block mb-1">{{ $t('network-rules.field.pool') }}</label>
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
                    <label class="text-[11px] text-gray-500 block mb-1">{{ $t('network-rules.field.connection') }}</label>
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
                    {{ $t('network-rules.button.cancel') }}
                  </button>
                  <button
                    @click="save"
                    :disabled="!canSave"
                    class="px-3 py-1.5 text-xs font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md disabled:opacity-50"
                  >
                    {{ $t('network-rules.button.save') }}
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
              {{ $t('network-rules.delete-confirm.title') }}
            </DialogTitle>
            <p class="text-[11px] text-gray-500 mb-4">{{ deleting ? ruleSummary(deleting) : '' }}</p>
            <div class="flex justify-end gap-2">
              <button @click="deleting = null" class="px-3 py-1.5 text-xs text-gray-600 dark:text-gray-300 hover:text-gray-900 dark:hover:text-white">
                {{ $t('network-rules.button.cancel') }}
              </button>
              <button @click="doDelete" class="px-3 py-1.5 text-xs font-medium text-white bg-red-600 hover:bg-red-700 rounded-md">
                {{ $t('network-rules.button.delete') }}
              </button>
            </div>
          </DialogPanel>
        </div>
      </Dialog>
    </TransitionRoot>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ListNetworkRules, AddNetworkRule, UpdateNetworkRule, DeleteNetworkRule,
  SetNetworkRulesOrder, ListPools, ListConnections,
  GetSettings, UpdateSettings, GetPlatformFeatures, GetConnectOnDemandStatus,
  GetCurrentNetworkRulesEval,
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
import AppSelect from '@/components/AppSelect.vue'

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

const { t } = useI18n()

const rules = ref<NetworkRule[]>([])
const pools = ref<any[]>([])
const connections = ref<any[]>([])
const editing = ref<NetworkRule | null>(null)
const deleting = ref<NetworkRule | null>(null)
const draft = ref<NetworkRule>(emptyRule())

// --- v1.0.5.23: live evaluation snapshot for the bottom card ---
// Polled every 2s from the Go side; the eval card composables
// (evalNetworkText / evalMasterText / evalDecisionText) read from
// this and the i18n catalog.
interface NetworkRulesEvalSnapshot {
  network_type: string
  ssid: string
  master_enabled: boolean
  has_rules: boolean
  engine_active: boolean
  rule_matching: boolean
}
const evalSnap = ref<NetworkRulesEvalSnapshot>({
  network_type: 'none',
  ssid: '',
  master_enabled: false,
  has_rules: false,
  engine_active: false,
  rule_matching: false,
})
let evalPollTimer: number | undefined
const evalNetworkText = computed(() => {
  const t1 = t
  if (evalSnap.value.network_type === 'wifi' && evalSnap.value.ssid) {
    return t1('network-rules.eval.network-wifi-named', { ssid: evalSnap.value.ssid })
  }
  if (evalSnap.value.network_type === 'wifi') {
    return t1('network-rules.eval.network-wifi-unnamed')
  }
  if (evalSnap.value.network_type === 'mobile') {
    return t1('network-rules.eval.network-mobile')
  }
  if (evalSnap.value.network_type === 'ethernet') {
    return t1('network-rules.eval.network-ethernet')
  }
  return t1('network-rules.eval.network-none')
})
const evalMasterText = computed(() =>
  evalSnap.value.master_enabled
    ? t('network-rules.eval.master-on')
    : t('network-rules.eval.master-off')
)
const evalDecisionText = computed(() => {
  if (!evalSnap.value.master_enabled) {
    return t('network-rules.eval.decision-manual')
  }
  if (evalSnap.value.rule_matching) {
    return t('network-rules.eval.decision-rule-active')
  }
  return t('network-rules.eval.decision-no-match')
})

// --- Settings + Connect-on-Demand ("Default behaviour") state ---
// In v0.9.15.73 the legacy COD config and the rules-engine gate were
// pulled out of Settings and onto this unified screen (Option A1).
const settings = ref<any>({ network_rules_enabled: false })
const platform = ref<any>({ auto_connect_supported: false })
const connectOnDemand = ref<any>({
  enabled: false,
  trigger: 'any',
  ssid_mode: 'all',
  ssid_list: [],
})
const newSSID = ref('')
const codStatus = ref<any>(null)
let codStatusInterval: ReturnType<typeof setInterval> | null = null

const matchTypeOptions = computed(() => [
  { value: 'ssid_exact', label: t('network-rules.match-type.ssid-exact') },
  { value: 'ssid_pattern', label: t('network-rules.match-type.ssid-pattern') },
  { value: 'network_type', label: t('network-rules.match-type.network-type') },
  { value: 'bssid', label: t('network-rules.match-type.bssid') },
  { value: 'any', label: t('network-rules.match-type.any') },
])

const actionOptions = computed(() => [
  { value: 'no_vpn', label: t('network-rules.action.no-vpn') },
  { value: 'pool', label: t('network-rules.action.pool') },
  { value: 'connection', label: t('network-rules.action.connection') },
])

const triggerOptions = computed(() => [
  { value: 'any', label: t('network-rules.trigger.any') },
  { value: 'wifi', label: t('network-rules.trigger.wifi') },
  { value: 'ethernet', label: t('network-rules.trigger.ethernet') },
  { value: 'mobile', label: t('network-rules.trigger.mobile') },
  { value: 'wifi_mobile', label: t('network-rules.trigger.wifi-mobile') },
])

const ssidModeOptions = computed(() => [
  { value: 'all', label: t('network-rules.ssid-mode.all') },
  { value: 'only', label: t('network-rules.ssid-mode.only') },
  { value: 'except', label: t('network-rules.ssid-mode.except') },
])

function emptyRule(): NetworkRule {
  return {
    id: '', priority: 0,
    match_type: 'ssid_exact', match_value: '',
    action: 'no_vpn', target_id: '',
    enabled: true, name: '',
  }
}

function matchTypeLabel(v: string): string {
  return matchTypeOptions.value.find(o => o.value === v)?.label ?? v
}
function actionLabel(v: string): string {
  return actionOptions.value.find(o => o.value === v)?.label ?? v
}
function targetLabel(kind: 'pool' | 'connection', id: string): string {
  if (!id) return kind === 'pool' ? t('network-rules.target.pick-pool') : t('network-rules.target.pick-connection')
  if (kind === 'pool') {
    const p = pools.value.find((x: any) => x.id === id)
    return p?.name ?? t('network-rules.target.missing')
  }
  const c = connections.value.find((x: any) => x.id === id)
  return c?.name ?? t('network-rules.target.missing')
}

const matchValueLabel = computed(() => {
  switch (draft.value.match_type) {
    case 'ssid_exact': return t('network-rules.match-value-label.ssid-exact')
    case 'ssid_pattern': return t('network-rules.match-value-label.ssid-pattern')
    case 'network_type': return t('network-rules.match-value-label.network-type')
    case 'bssid': return t('network-rules.match-value-label.bssid')
    default: return ''
  }
})

const matchValueHint = computed(() => {
  switch (draft.value.match_type) {
    case 'ssid_exact': return t('network-rules.match-value-hint.ssid-exact')
    case 'ssid_pattern': return t('network-rules.match-value-hint.ssid-pattern')
    case 'network_type': return t('network-rules.match-value-hint.network-type')
    case 'bssid': return t('network-rules.match-value-hint.bssid')
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
    case 'ssid_exact': m = t('network-rules.summary.ssid-exact', { value: rule.match_value }); break
    case 'ssid_pattern': m = t('network-rules.summary.ssid-pattern', { value: rule.match_value }); break
    case 'network_type': m = t('network-rules.summary.network-type', { value: rule.match_value }); break
    case 'bssid': m = t('network-rules.summary.bssid', { value: rule.match_value }); break
    case 'any': m = t('network-rules.summary.any'); break
  }
  let action = ''
  switch (rule.action) {
    case 'no_vpn': action = t('network-rules.summary.action-no-vpn'); break
    case 'pool': {
      const p = pools.value.find((x: any) => x.id === rule.target_id)
      action = t('network-rules.summary.action-pool', { name: p?.name ?? t('network-rules.target.missing') }); break
    }
    case 'connection': {
      const c = connections.value.find((x: any) => x.id === rule.target_id)
      action = t('network-rules.summary.action-connection', { name: c?.name ?? t('network-rules.target.missing') }); break
    }
  }
  return `${m}  ${action}`
}

async function load() {
  rules.value = (await ListNetworkRules()) as NetworkRule[] || []
  pools.value = (await ListPools()) as any[] || []
  connections.value = (await ListConnections()) as any[] || []
}

// --- Settings / Connect-on-Demand plumbing (moved from SettingsView) ---
async function loadSettings() {
  try {
    settings.value = await GetSettings()
    if (settings.value.connect_on_demand) {
      connectOnDemand.value = { ...settings.value.connect_on_demand }
      if (connectOnDemand.value.enabled) {
        startCodStatusPolling()
      }
    }
  } catch (e) {
    console.error('Failed to load settings:', e)
  }
}

async function saveSettingsImmediate() {
  try {
    await UpdateSettings(settings.value)
  } catch (e) {
    console.error('Failed to save settings:', e)
  }
}

async function onEngineToggle(v: boolean) {
  settings.value.network_rules_enabled = v
  await saveSettingsImmediate()
}

async function toggleConnectOnDemand() {
  connectOnDemand.value.enabled = !connectOnDemand.value.enabled
  await saveOnDemandSettings()
  if (connectOnDemand.value.enabled) {
    startCodStatusPolling()
  } else {
    stopCodStatusPolling()
    codStatus.value = null
  }
}

async function saveOnDemandSettings() {
  settings.value.connect_on_demand = { ...connectOnDemand.value }
  // Keep legacy field in sync
  settings.value.auto_connect_on_start = connectOnDemand.value.enabled
  // Synchronous save — see the original SettingsView rationale: the
  // debounced path raced view re-mount and undid the toggle.
  await saveSettingsImmediate()
}

// Add a single SSID via the chip-input pattern: user types one
// network name, presses Enter (or clicks Add), it appears as a
// removable chip below. Duplicates are silently ignored.
function addSSID() {
  const trimmed = newSSID.value.trim()
  if (!trimmed) return
  if (!connectOnDemand.value.ssid_list) {
    connectOnDemand.value.ssid_list = []
  }
  if (connectOnDemand.value.ssid_list.includes(trimmed)) {
    newSSID.value = ''
    return
  }
  connectOnDemand.value.ssid_list = [...connectOnDemand.value.ssid_list, trimmed]
  newSSID.value = ''
  saveOnDemandSettings()
}

function removeSSID(ssid: string) {
  if (!connectOnDemand.value.ssid_list) return
  connectOnDemand.value.ssid_list = connectOnDemand.value.ssid_list.filter(
    (s: string) => s !== ssid,
  )
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

async function refreshEvalSnap() {
  try {
    const snap = await GetCurrentNetworkRulesEval()
    if (snap) {
      evalSnap.value = snap as any
    }
  } catch (e) {
    // Non-fatal: backend not ready yet, or transient. Card just
    // shows the last good state (or initial defaults). Don't log
    // — polls every 2 s, would flood the console.
  }
}

onMounted(async () => {
  load()
  loadSettings()
  try {
    platform.value = await GetPlatformFeatures()
  } catch (e) {
    console.error('Failed to load platform features:', e)
  }
  // v1.0.5.23: live eval poll (2 s) for the bottom card. One-shot
  // initial fetch + interval. Setting a longer interval would feel
  // sluggish when the user joins / leaves a Wi-Fi.
  refreshEvalSnap()
  evalPollTimer = window.setInterval(refreshEvalSnap, 2000)
})

onUnmounted(() => {
  stopCodStatusPolling()
  if (evalPollTimer !== undefined) {
    window.clearInterval(evalPollTimer)
    evalPollTimer = undefined
  }
})
</script>
