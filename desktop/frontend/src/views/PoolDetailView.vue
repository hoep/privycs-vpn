<template>
  <div class="p-4 overflow-y-auto max-h-[calc(100vh-7rem)]">
    <!-- Header -->
    <div class="flex items-center justify-between mb-4">
      <div class="flex items-center gap-2 min-w-0">
        <button @click="$router.back()" class="text-gray-500 dark:text-gray-400 hover:text-gray-900 dark:hover:text-white">
          <ArrowLeftIcon class="w-5 h-5" />
        </button>
        <h2 class="text-sm font-semibold text-gray-900 dark:text-white truncate">
          {{ pool?.name || 'Loading...' }}
        </h2>
      </div>
      <div class="flex items-center gap-2">
        <button
          @click="showSettingsModal = true"
          class="text-gray-500 hover:text-primary-400"
          title="Edit pool"
        >
          <Cog6ToothIcon class="w-5 h-5" />
        </button>
      </div>
    </div>

    <p class="text-[10px] text-gray-500 mb-4">
      Pool · {{ pool?.members?.length || 0 }} servers · {{ uniqueProtocols }}
    </p>

    <div v-if="loading" class="text-center text-xs text-gray-500 py-6">
      Loading...
    </div>

    <div v-else-if="!pool" class="text-center text-xs text-red-400 py-6">
      Pool not found.
    </div>

    <template v-else>
      <!-- Coverage section -->
      <div class="card p-3 mb-4">
        <div class="flex items-center justify-between mb-2">
          <h3 class="text-[11px] font-semibold text-gray-500 dark:text-gray-400">Coverage</h3>
          <button
            v-if="coverage.length > 1"
            @click="showRegionFilter = !showRegionFilter"
            class="text-[10px] text-primary-400 hover:text-primary-300"
          >
            {{ showRegionFilter ? 'Hide' : 'Restrict to region' }}
          </button>
        </div>
        <div v-if="coverage.length === 0" class="text-[10px] text-gray-500 italic">
          No country data — geo policies will fall back to Random.
        </div>
        <div v-else>
          <!-- Read-only summary, hidden when the filter is open. -->
          <div v-if="!showRegionFilter" class="space-y-1">
            <div
              v-for="row in coverage"
              :key="row.region"
              class="flex justify-between items-center text-xs"
            >
              <span class="text-gray-700 dark:text-gray-300 flex items-center gap-1.5">
                {{ row.region }}
                <span
                  v-if="restrictedRegions.length > 0 && !restrictedRegions.includes(row.region)"
                  class="text-[9px] text-gray-500 italic"
                >excluded</span>
              </span>
              <span class="text-gray-500">{{ row.servers }} server<span v-if="row.servers !== 1">s</span> · {{ row.countries }} {{ row.countries === 1 ? 'country' : 'countries' }}</span>
            </div>
          </div>

          <!-- Region restriction toggle list. Empty = no restriction. -->
          <div v-else class="space-y-1.5">
            <p class="text-[10px] text-gray-500 mb-1">
              Toggle off any region to exclude it from policy picks. Leave all on for no restriction.
            </p>
            <label
              v-for="row in coverage"
              :key="row.region"
              class="flex items-center justify-between py-1 cursor-pointer hover:bg-gray-100 dark:hover:bg-gray-700/30 rounded px-2"
            >
              <div class="flex items-center gap-2">
                <input
                  type="checkbox"
                  :checked="isRegionEnabled(row.region)"
                  @change="toggleRegion(row.region)"
                  class="rounded"
                />
                <span class="text-xs text-gray-700 dark:text-gray-300">{{ row.region }}</span>
              </div>
              <span class="text-[10px] text-gray-500">{{ row.servers }} · {{ row.countries }}</span>
            </label>
            <div class="flex justify-end pt-2 gap-2">
              <button
                @click="restrictedRegions = []"
                class="text-[10px] text-gray-500 hover:text-gray-700 dark:hover:text-gray-300"
              >
                Reset
              </button>
              <button
                @click="saveRegionRestriction"
                :disabled="regionRestrictSaving"
                class="text-[10px] px-2 py-1 rounded bg-primary-600 text-white hover:bg-primary-700 disabled:opacity-50"
              >
                {{ regionRestrictSaving ? 'Saving...' : 'Apply' }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- Policy summary -->
      <div class="card p-3 mb-4">
        <h3 class="text-[11px] font-semibold text-gray-500 dark:text-gray-400 mb-2">Policy</h3>
        <div class="flex justify-between items-center text-xs">
          <span class="text-gray-700 dark:text-gray-300">{{ policyLabel }}</span>
          <button @click="showSettingsModal = true" class="text-[10px] text-primary-400 hover:text-primary-300">
            Change
          </button>
        </div>
        <div v-if="pool.policy === 'geo-nearest'" class="text-[10px] text-gray-500 mt-1">
          Country override: <span class="text-gray-700 dark:text-gray-300">{{ countryOverrideDisplay }}</span>
        </div>
      </div>

      <!-- Members list -->
      <div class="card p-3 mb-4">
        <div class="flex justify-between items-center mb-2">
          <h3 class="text-[11px] font-semibold text-gray-500 dark:text-gray-400 flex items-center gap-2">
            Members
            <span
              v-if="unreachableCount > 0"
              class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded bg-amber-500/15 ring-1 ring-amber-500/30 text-amber-600 dark:text-amber-400 text-[9px] font-medium"
              :title="`${unreachableCount} member(s) flagged unreachable. Auto-clear after 30 minutes.`"
            >
              <ExclamationTriangleIcon class="w-3 h-3" />
              {{ unreachableCount }} unreachable
            </span>
          </h3>
          <div class="flex items-center gap-2">
            <button
              v-if="unreachableCount > 0"
              @click="onResetUnreachable"
              :disabled="resetting"
              class="text-[10px] text-primary-400 hover:text-primary-300 disabled:opacity-50"
              title="Clear the unreachable flag on all members. Use after a network change so the rotator can pick them again immediately."
            >
              {{ resetting ? 'Resetting...' : 'Reset all' }}
            </button>
            <input
              v-model="memberFilter"
              type="text"
              placeholder="Search..."
              class="text-[10px] bg-gray-50 dark:bg-gray-800 px-2 py-1 rounded border border-gray-200 dark:border-gray-700 w-32 focus:outline-none focus:ring-1 focus:ring-primary-500"
            />
          </div>
        </div>
        <div class="space-y-0.5 max-h-96 overflow-y-auto">
          <div
            v-for="m in filteredMembers"
            :key="m.id"
            class="flex items-center justify-between py-1 px-2 rounded hover:bg-gray-100 dark:hover:bg-gray-700/50 group"
            :class="m.unreachable ? 'bg-amber-500/5' : ''"
          >
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-1.5">
                <span class="text-xs text-gray-700 dark:text-gray-300 truncate">{{ m.name }}</span>
                <span
                  v-if="m.unreachable"
                  class="inline-flex items-center px-1 py-px rounded bg-amber-500/15 ring-1 ring-amber-500/30 text-amber-600 dark:text-amber-400 text-[8px] font-medium uppercase tracking-wide flex-shrink-0"
                  :title="memberUnreachableTooltip(m)"
                >
                  Unreachable
                </span>
              </div>
              <span class="text-[9px] text-gray-500">
                {{ m.country || 'unknown' }} · {{ m.region || 'Other' }}
                <span v-if="m.unreachable && m.last_error" class="ml-1 text-amber-500/80 truncate">
                  · {{ m.last_error }}
                </span>
              </span>
            </div>
            <div class="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
              <button
                @click.stop="renameMember(m)"
                class="p-1 text-gray-400 hover:text-primary-400"
                title="Rename"
              >
                <PencilSquareIcon class="w-3.5 h-3.5" />
              </button>
              <button
                @click.stop="deleteMember(m)"
                class="p-1 text-gray-400 hover:text-red-400"
                title="Delete"
              >
                <TrashIcon class="w-3.5 h-3.5" />
              </button>
            </div>
          </div>
          <div v-if="filteredMembers.length === 0" class="text-center text-[10px] text-gray-500 italic py-3">
            No matches.
          </div>
        </div>
      </div>

      <!-- Action row -->
      <div class="flex justify-between items-center">
        <button
          @click="confirmDeletePool"
          class="text-xs text-red-400 hover:text-red-300"
        >
          Delete Pool
        </button>
        <button
          @click="usePool"
          :disabled="pool.members.length === 0"
          class="px-3 py-1.5 text-xs font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md disabled:opacity-50"
        >
          {{ poolStore.activePoolId === pool.id ? 'Active' : 'Use this Pool' }}
        </button>
      </div>
    </template>

    <!-- Edit Pool Modal -->
    <TransitionRoot :show="showSettingsModal" as="template">
      <Dialog as="div" class="relative z-50" @close="showSettingsModal = false">
        <TransitionChild
          as="template"
          enter="ease-out duration-200"
          enter-from="opacity-0"
          enter-to="opacity-100"
          leave="ease-in duration-150"
          leave-from="opacity-100"
          leave-to="opacity-0"
        >
          <div class="fixed inset-0 bg-black/60 backdrop-blur-sm" />
        </TransitionChild>

        <div class="fixed inset-0 overflow-y-auto">
          <div class="flex min-h-full items-center justify-center p-4">
            <TransitionChild
              as="template"
              enter="ease-out duration-200"
              enter-from="opacity-0 scale-95"
              enter-to="opacity-100 scale-100"
              leave="ease-in duration-150"
              leave-from="opacity-100 scale-100"
              leave-to="opacity-0 scale-95"
            >
              <DialogPanel class="w-full max-w-md transform overflow-hidden rounded-xl bg-white dark:bg-gray-900 shadow-2xl ring-1 ring-black/5 dark:ring-white/10 max-h-[85vh] flex flex-col">
                <div class="flex items-center justify-between px-5 py-3.5 border-b border-gray-200 dark:border-gray-700">
                  <DialogTitle class="text-sm font-semibold text-gray-900 dark:text-white">
                    Edit Pool
                  </DialogTitle>
                  <button
                    @click="showSettingsModal = false"
                    class="text-gray-400 hover:text-gray-600 dark:hover:text-gray-300 transition-colors"
                  >
                    <XMarkIcon class="w-4 h-4" />
                  </button>
                </div>

                <div class="flex-1 overflow-y-auto px-5 py-4 space-y-4">
                  <!-- Name -->
                  <div>
                    <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1.5">
                      Name
                    </label>
                    <input
                      v-model="editName"
                      class="block w-full rounded-md border border-gray-300 dark:border-gray-600
                             bg-white dark:bg-gray-800 px-3 py-1.5 text-sm
                             text-gray-900 dark:text-gray-200
                             focus:outline-none focus:ring-1 focus:ring-primary-500 focus:border-primary-500"
                    />
                  </div>

                  <!-- Policy: HeadlessUI Listbox via AppSelect -->
                  <div>
                    <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1.5">
                      Selection policy
                    </label>
                    <AppSelect
                      v-model="editPolicy"
                      :options="policyOptions"
                    />
                    <p class="text-[10px] text-gray-500 mt-1.5">
                      {{ policyDescriptionFor(editPolicy) }}
                    </p>
                  </div>

                  <!-- Country override -->
                  <div>
                    <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1.5">
                      Country override
                    </label>
                    <input
                      v-model="editCountryOverride"
                      :placeholder="`Auto (currently ${poolStore.userCountry || 'unknown'})`"
                      class="block w-full rounded-md border border-gray-300 dark:border-gray-600
                             bg-white dark:bg-gray-800 px-3 py-1.5 text-sm
                             text-gray-900 dark:text-gray-200 placeholder:text-gray-400
                             focus:outline-none focus:ring-1 focus:ring-primary-500 focus:border-primary-500"
                    />
                    <p class="text-[10px] text-gray-500 mt-1.5">
                      Leave blank for auto-detect via DoH probe.
                    </p>
                  </div>

                  <!-- Rotation params (Round-Robin only) -->
                  <div
                    v-if="editPolicy === 'round-robin-region'"
                    class="space-y-3 border-t border-gray-200 dark:border-gray-700 pt-4"
                  >
                    <div>
                      <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1.5">
                        Rotation interval
                      </label>
                      <AppSelect
                        v-model="editIntervalChoice"
                        :options="intervalOptions"
                      />
                      <!-- Custom: free-form minutes when "Custom..."
                           is selected. Hidden otherwise so the user
                           never sees a number input they do not need. -->
                      <div v-if="editIntervalChoice === 'custom'" class="mt-2 flex items-center gap-2">
                        <input
                          v-model.number="editIntervalMin"
                          type="number"
                          min="1"
                          max="1440"
                          class="block w-24 rounded-md border border-gray-300 dark:border-gray-600
                                 bg-white dark:bg-gray-800 px-3 py-1.5 text-sm
                                 text-gray-900 dark:text-gray-200
                                 focus:outline-none focus:ring-1 focus:ring-primary-500"
                        />
                        <span class="text-[11px] text-gray-500">minutes (1 - 1440)</span>
                      </div>
                    </div>

                    <!-- Idle-aware HeadlessUI Switch -->
                    <SwitchGroup as="div" class="flex items-start justify-between gap-3">
                      <div class="flex-1">
                        <SwitchLabel as="span" class="block text-xs font-medium text-gray-700 dark:text-gray-300">
                          Idle-aware
                        </SwitchLabel>
                        <p class="text-[10px] text-gray-500 mt-0.5">
                          Defer rotation while traffic flows, force-rotate after the cap below.
                        </p>
                      </div>
                      <Switch
                        v-model="editIdleAware"
                        :class="editIdleAware ? 'bg-primary-600' : 'bg-gray-300 dark:bg-gray-700'"
                        class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full
                               transition-colors duration-200 ease-in-out
                               focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
                      >
                        <span
                          :class="editIdleAware ? 'translate-x-4' : 'translate-x-0'"
                          class="pointer-events-none inline-block h-5 w-5 transform rounded-full
                                 bg-white shadow ring-0 transition-transform duration-200 ease-in-out"
                        />
                      </Switch>
                    </SwitchGroup>

                    <div v-if="editIdleAware">
                      <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1.5">
                        Force-rotate after (min idle-blocked)
                      </label>
                      <AppSelect
                        v-model="editForceAfterMinStr"
                        :options="forceAfterOptions"
                      />
                    </div>
                  </div>
                </div>

                <!--
                  Split Tunnel section. Per-pool client-side bypass:
                  CIDRs entered here are excluded from the tunnel
                  (traffic to those ranges goes via the local default
                  gateway). WireGuard + OpenVPN supported; IPSec pool
                  members are skipped silently and a log warning
                  surfaces in the daemon logs.
                -->
                <div class="px-5 py-4 border-t border-gray-200 dark:border-gray-700 space-y-3">
                  <div>
                    <h3 class="text-xs font-semibold text-gray-700 dark:text-gray-300">Split tunnel (bypass)</h3>
                    <p class="text-[11px] text-gray-500 mt-0.5">
                      Traffic to these IP ranges goes around the VPN.
                      WireGuard + OpenVPN; IPSec members skipped
                      (server-side traffic-selector negotiation).
                    </p>
                  </div>
                  <SwitchGroup as="div" class="flex items-start justify-between gap-3">
                    <div class="flex-1">
                      <SwitchLabel as="span" class="block text-xs font-medium text-gray-700 dark:text-gray-300">
                        Exclude private networks
                      </SwitchLabel>
                      <p class="text-[10px] text-gray-500 mt-0.5">
                        RFC1918 (10/8, 172.16/12, 192.168/16) + IPv6 ULA fc00::/7 + link-local
                      </p>
                    </div>
                    <Switch
                      v-model="editExcludePrivateNetworks"
                      :class="editExcludePrivateNetworks ? 'bg-primary-600' : 'bg-gray-300 dark:bg-gray-700'"
                      class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full
                             transition-colors duration-200 ease-in-out
                             focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
                    >
                      <span
                        :class="editExcludePrivateNetworks ? 'translate-x-4' : 'translate-x-0'"
                        class="pointer-events-none inline-block h-5 w-5 transform rounded-full
                               bg-white shadow ring-0 transition-transform duration-200 ease-in-out"
                      />
                    </Switch>
                  </SwitchGroup>
                  <div>
                    <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1.5">
                      Custom bypass CIDRs (one per line)
                    </label>
                    <textarea
                      v-model="editBypassCidrsText"
                      rows="4"
                      spellcheck="false"
                      placeholder="203.0.113.0/24&#10;2001:db8::/32&#10;198.51.100.42"
                      class="w-full bg-gray-50 dark:bg-gray-800
                             text-gray-800 dark:text-gray-200
                             text-xs font-mono p-3 rounded-lg
                             border border-gray-200 dark:border-gray-700
                             focus:outline-none focus:ring-2 focus:ring-primary-500
                             resize-none"
                    />
                    <p
                      v-if="bypassValidation.total === 0"
                      class="text-[10px] text-gray-500 mt-1"
                    >
                      Empty = no custom CIDRs (private-networks toggle still applies)
                    </p>
                    <p
                      v-else-if="bypassValidation.invalidCount === 0"
                      class="text-[10px] text-green-400 mt-1"
                    >
                      {{ bypassValidation.total }} CIDR{{ bypassValidation.total === 1 ? '' : 's' }} valid
                    </p>
                    <p
                      v-else
                      class="text-[10px] text-red-400 mt-1"
                    >
                      {{ bypassValidation.invalidCount }} of {{ bypassValidation.total }} invalid:
                      {{ bypassValidation.invalidSample.join(', ') }}
                    </p>
                  </div>
                </div>

                <div class="flex justify-end gap-2 px-5 py-3 border-t border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800/30">
                  <button
                    @click="showSettingsModal = false"
                    class="px-3 py-1.5 text-xs text-gray-600 dark:text-gray-300 hover:text-gray-900 dark:hover:text-white transition-colors"
                  >
                    Cancel
                  </button>
                  <button
                    @click="saveSettings"
                    class="px-3 py-1.5 text-xs font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md transition-colors"
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
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usePoolStore, type PoolPolicy } from '@/stores/pool'
import {
  Dialog,
  DialogPanel,
  DialogTitle,
  Switch,
  SwitchGroup,
  SwitchLabel,
  TransitionRoot,
  TransitionChild,
} from '@headlessui/vue'
import AppSelect from '@/components/AppSelect.vue'
import {
  ArrowLeftIcon,
  Cog6ToothIcon,
  ExclamationTriangleIcon,
  PencilSquareIcon,
  TrashIcon,
  XMarkIcon,
} from '@heroicons/vue/24/outline'

// AppSelect carries string values via Listbox, so the numeric
// rotation params get a string-typed proxy that converts in/out.
const policyOptions = [
  { value: 'geo-nearest', label: 'Geo-Nearest' },
  { value: 'random', label: 'Random' },
  { value: 'round-robin-region', label: 'Round-Robin (Region)' },
]

const intervalOptions = [
  { value: '5',  label: '5 minutes'  },
  { value: '10', label: '10 minutes' },
  { value: '15', label: '15 minutes' },
  { value: '30', label: '30 minutes' },
  { value: '60', label: '1 hour'     },
  { value: '120', label: '2 hours'   },
  { value: 'custom', label: 'Custom...' },
]

const forceAfterOptions = [
  { value: '15', label: '15 minutes' },
  { value: '30', label: '30 minutes' },
  { value: '60', label: '1 hour'     },
  { value: '120', label: '2 hours'   },
  { value: '240', label: '4 hours'   },
]

function policyDescriptionFor(p: string): string {
  switch (p) {
    case 'geo-nearest': return 'Closest country to you. Falls back to same-region or random.'
    case 'random':      return 'Picks a random server on every connect.'
    case 'round-robin-region': return 'Rotates through different regions on a timer.'
  }
  return ''
}

const route = useRoute()
const router = useRouter()
const poolStore = usePoolStore()

const pool = ref<any>(null)
const coverage = ref<any[]>([])
const loading = ref(true)
const memberFilter = ref('')

// Region restriction state. restrictedRegions mirrors the backend
// pool.restrict_regions: an empty array means "no restriction"; any
// non-empty list means "only members in these regions count for
// policy picks". The toggle UI builds the list as the user checks/
// unchecks boxes; saveRegionRestriction persists via UpdatePool.
const showRegionFilter = ref(false)
const restrictedRegions = ref<string[]>([])
const regionRestrictSaving = ref(false)

function isRegionEnabled(region: string): boolean {
  // Empty restriction list = all regions enabled.
  if (restrictedRegions.value.length === 0) return true
  return restrictedRegions.value.includes(region)
}

function toggleRegion(region: string) {
  if (restrictedRegions.value.length === 0) {
    // Transitioning from "all" to "all except this one": initialise
    // with every region except the toggled-off one.
    restrictedRegions.value = coverage.value
      .map((r: any) => r.region)
      .filter((r: string) => r !== region)
    return
  }
  const idx = restrictedRegions.value.indexOf(region)
  if (idx >= 0) {
    restrictedRegions.value.splice(idx, 1)
  } else {
    restrictedRegions.value.push(region)
  }
  // If we're back to including every visible region, collapse to "all"
  // by clearing the list (semantically identical, simpler persistence).
  const allRegions = coverage.value.map((r: any) => r.region)
  if (allRegions.every((r: string) => restrictedRegions.value.includes(r))) {
    restrictedRegions.value = []
  }
}

async function saveRegionRestriction() {
  if (!pool.value) return
  regionRestrictSaving.value = true
  try {
    await poolStore.update(pool.value.id, {
      restrict_regions: restrictedRegions.value,
    })
    showRegionFilter.value = false
    await load()
  } catch (e) {
    console.error(e)
  } finally {
    regionRestrictSaving.value = false
  }
}

// Edit modal state
const showSettingsModal = ref(false)
const editName = ref('')
const editPolicy = ref<PoolPolicy>('geo-nearest')
const editCountryOverride = ref('')
const editIntervalMin = ref(30)
// editIntervalChoice mirrors the Listbox: "5" | "10" | "30" | "custom"
// etc. When the user picks "custom" the number input below appears
// and editIntervalMin captures the exact value entered.
const editIntervalChoice = ref<string>('30')
const editIdleAware = ref(false)
const editForceAfterMin = ref(30)
// Split-tunnel edit state. Bypass CIDRs are edited as a single
// multi-line textarea string; round-trip to the backend's
// string array happens at saveSettings time.
const editExcludePrivateNetworks = ref(false)
const editBypassCidrsText = ref('')
// Per-line validation: counts valid + invalid entries so the UI
// can surface "3 valid, 1 invalid" as a hint. Pure regex here -
// the backend re-parses with the canonical CidrMath at inject
// time and silently drops anything it can't match, so edge cases
// the regex misses don't cause runtime breakage.
const cidrLineRegex = /^(?:(?:\d{1,3}\.){3}\d{1,3}|[0-9a-fA-F:]+)(?:\/\d{1,3})?$/
const bypassValidation = computed(() => {
  const lines = editBypassCidrsText.value
    .split('\n')
    .map((s: string) => s.trim())
    .filter((s: string) => s.length > 0)
  const invalid = lines.filter((s: string) => !cidrLineRegex.test(s))
  return {
    total: lines.length,
    invalidCount: invalid.length,
    invalidSample: invalid.slice(0, 3),
  }
})

// AppSelect uses string-typed v-model. Bridge the numeric edit refs
// to/from string proxies so the dropdown options match without losing
// integer semantics on the backend round-trip.
const editIntervalMinStr = computed<string>({
  get: () => String(editIntervalMin.value),
  set: (v: string) => { editIntervalMin.value = parseInt(v, 10) || 30 },
})
const editForceAfterMinStr = computed<string>({
  get: () => String(editForceAfterMin.value),
  set: (v: string) => { editForceAfterMin.value = parseInt(v, 10) || 30 },
})

const policyLabel = computed(() => {
  switch (pool.value?.policy) {
    case 'geo-nearest': return 'Geo-Nearest'
    case 'random':      return 'Random'
    case 'round-robin-region': return `Round-Robin (every ${pool.value.rotation?.interval_min || 30} min)`
  }
  return pool.value?.policy || ''
})

const countryOverrideDisplay = computed(() => {
  if (pool.value?.country_override) return pool.value.country_override
  return `Auto (currently ${poolStore.userCountry || 'unknown'})`
})

const uniqueProtocols = computed(() => {
  if (!pool.value?.members) return ''
  const set = new Set<string>(pool.value.members.map((m: any) => m.config?.protocol).filter(Boolean))
  return Array.from(set).map((p: string) => p === 'wireguard' ? 'WireGuard' : p === 'openvpn' ? 'OpenVPN' : p === 'ipsec' ? 'IPSec' : p).join(', ')
})

const filteredMembers = computed(() => {
  if (!pool.value?.members) return []
  const q = memberFilter.value.trim().toLowerCase()
  if (!q) return pool.value.members
  return pool.value.members.filter((m: any) =>
    m.name.toLowerCase().includes(q) ||
    (m.country || '').toLowerCase().includes(q) ||
    (m.region || '').toLowerCase().includes(q)
  )
})

// Number of members the rotator currently has flagged Unreachable.
// Drives the header counter badge AND the "Reset all" button visibility -
// only show the button if there's actually something to reset.
const unreachableCount = computed(() => {
  if (!pool.value?.members) return 0
  return pool.value.members.filter((m: any) => m.unreachable).length
})

// Per-member tooltip carrying the failure timestamp + reason. Falls
// back to "Unreachable" when no timestamp persisted yet (legacy pools
// flagged before v0.9.11.33).
function memberUnreachableTooltip(m: any): string {
  if (!m.unreachable) return ''
  const parts: string[] = []
  if (m.last_unreachable) {
    const t = new Date(m.last_unreachable)
    if (!isNaN(t.getTime())) {
      parts.push('Last failure: ' + t.toLocaleString())
    }
  }
  if (m.last_error) {
    parts.push('Reason: ' + m.last_error)
  }
  parts.push('Auto-clears 30 min after the last failure.')
  return parts.join('\n')
}

// Manual flag reset. Returns silently after re-loading the pool so
// the badge counter and per-member badges disappear in one frame.
const resetting = ref(false)
async function onResetUnreachable() {
  if (!pool.value || resetting.value) return
  resetting.value = true
  try {
    await poolStore.resetUnreachable(pool.value.id)
    await load()
  } catch (e) {
    console.error('reset unreachable failed:', e)
  } finally {
    resetting.value = false
  }
}

async function load() {
  loading.value = true
  try {
    const res: any = await poolStore.detail(route.params.id as string)
    pool.value = res?.pool
    coverage.value = res?.coverage || []
    if (pool.value) {
      editName.value = pool.value.name
      editPolicy.value = pool.value.policy
      editCountryOverride.value = pool.value.country_override || ''
      editIntervalMin.value = pool.value.rotation?.interval_min || 30
      // Match the dropdown to the saved value if it's a preset,
      // else show "Custom..." with the value populated in the input.
      const presets = ['5', '10', '15', '30', '60', '120']
      const savedStr = String(editIntervalMin.value)
      editIntervalChoice.value = presets.includes(savedStr) ? savedStr : 'custom'
      editIdleAware.value = pool.value.rotation?.idle_aware ?? true
      editForceAfterMin.value = pool.value.rotation?.force_after_min || 60
      restrictedRegions.value = [...(pool.value.restrict_regions || [])]
      // Split-tunnel state: pull from the persisted pool struct.
      // Default safe values when the field is missing (older pools
      // pre-v0.9.11.55 won't have it).
      editExcludePrivateNetworks.value =
        pool.value.split_tunnel?.exclude_private_networks ?? false
      editBypassCidrsText.value =
        (pool.value.split_tunnel?.bypass_cidrs ?? []).join('\n')
    }
  } catch (e) {
    console.error(e)
  } finally {
    loading.value = false
  }
}

// Sync editIntervalMin from the dropdown choice when user picks a preset.
// "custom" leaves editIntervalMin alone so the user's typed value survives.
watch(editIntervalChoice, (choice: string) => {
  if (choice !== 'custom') {
    const n = parseInt(choice, 10)
    if (!isNaN(n) && n > 0) {
      editIntervalMin.value = n
    }
  }
})

async function saveSettings() {
  if (!pool.value) return
  try {
    // Strip empty + whitespace lines from the bypass-CIDR textarea
    // before sending. Backend parses canonically and silently drops
    // anything it can't match - we don't filter by regex here so
    // the user's typed strings round-trip to the user even if they
    // include something unusual the regex doesn't anticipate.
    const cidrLines = editBypassCidrsText.value
      .split('\n')
      .map((s: string) => s.trim())
      .filter((s: string) => s.length > 0)
    await poolStore.update(pool.value.id, {
      name: editName.value,
      policy: editPolicy.value,
      country_override: editCountryOverride.value || '',
      rotation: {
        interval_min: editIntervalMin.value,
        idle_aware: editIdleAware.value,
        force_after_min: editForceAfterMin.value,
      },
      split_tunnel: {
        bypass_cidrs: cidrLines,
        exclude_private_networks: editExcludePrivateNetworks.value,
      },
    })
    showSettingsModal.value = false
    await load()
  } catch (e) {
    console.error(e)
  }
}

async function deleteMember(m: any) {
  if (!pool.value) return
  if (!confirm(`Delete ${m.name} from this pool?`)) return
  await poolStore.removeMember(pool.value.id, m.id)
  await load()
}

async function renameMember(m: any) {
  if (!pool.value) return
  const newName = prompt('New name:', m.name)
  if (!newName || newName.trim() === '' || newName === m.name) return
  await poolStore.renameMember(pool.value.id, m.id, newName.trim())
  await load()
}

async function confirmDeletePool() {
  if (!pool.value) return
  if (!confirm(`Delete pool "${pool.value.name}"? This cannot be undone.`)) return
  await poolStore.remove(pool.value.id)
  router.push('/connections')
}

async function usePool() {
  if (!pool.value) return
  await poolStore.activate(pool.value.id)
  router.push('/connection')
}

onMounted(load)
</script>
