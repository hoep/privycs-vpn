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
        <h3 class="text-[11px] font-semibold text-gray-500 dark:text-gray-400 mb-2">Coverage</h3>
        <div v-if="coverage.length === 0" class="text-[10px] text-gray-500 italic">
          No country data — geo policies will fall back to Random.
        </div>
        <div v-else class="space-y-1">
          <div
            v-for="row in coverage"
            :key="row.region"
            class="flex justify-between items-center text-xs"
          >
            <span class="text-gray-700 dark:text-gray-300">{{ row.region }}</span>
            <span class="text-gray-500">{{ row.servers }} server<span v-if="row.servers !== 1">s</span> · {{ row.countries }} {{ row.countries === 1 ? 'country' : 'countries' }}</span>
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
          <h3 class="text-[11px] font-semibold text-gray-500 dark:text-gray-400">Members</h3>
          <input
            v-model="memberFilter"
            type="text"
            placeholder="Search..."
            class="text-[10px] bg-gray-50 dark:bg-gray-800 px-2 py-1 rounded border border-gray-200 dark:border-gray-700 w-32 focus:outline-none focus:ring-1 focus:ring-primary-500"
          />
        </div>
        <div class="space-y-0.5 max-h-96 overflow-y-auto">
          <div
            v-for="m in filteredMembers"
            :key="m.id"
            class="flex items-center justify-between py-1 px-2 rounded hover:bg-gray-100 dark:hover:bg-gray-700/50 group"
          >
            <div class="min-w-0 flex-1">
              <span class="text-xs text-gray-700 dark:text-gray-300 truncate block">{{ m.name }}</span>
              <span class="text-[9px] text-gray-500">
                {{ m.country || 'unknown' }} · {{ m.region || 'Other' }}
                <span v-if="m.unreachable" class="text-amber-400 ml-1">• unreachable</span>
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
    <div
      v-if="showSettingsModal"
      class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4"
      @click.self="showSettingsModal = false"
    >
      <div class="bg-white dark:bg-gray-900 rounded-xl shadow-2xl w-full max-w-md max-h-[80vh] flex flex-col">
        <div class="flex items-center justify-between px-4 py-3 border-b border-gray-200 dark:border-gray-700">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">Edit Pool</h3>
          <button @click="showSettingsModal = false" class="text-gray-400 hover:text-gray-600">
            <XMarkIcon class="w-4 h-4" />
          </button>
        </div>
        <div class="flex-1 overflow-y-auto p-4 space-y-3 text-xs">
          <div>
            <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">Name</label>
            <input
              v-model="editName"
              class="w-full bg-gray-50 dark:bg-gray-800 px-3 py-1.5 rounded border border-gray-200 dark:border-gray-700"
            />
          </div>
          <div>
            <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">Selection policy</label>
            <select v-model="editPolicy" class="w-full bg-gray-50 dark:bg-gray-800 px-3 py-1.5 rounded border border-gray-200 dark:border-gray-700">
              <option value="geo-nearest">Geo-Nearest</option>
              <option value="random">Random</option>
              <option value="round-robin-region">Round-Robin Region</option>
            </select>
          </div>
          <div>
            <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">Country override</label>
            <input
              v-model="editCountryOverride"
              :placeholder="`Auto (currently ${poolStore.userCountry || 'unknown'})`"
              class="w-full bg-gray-50 dark:bg-gray-800 px-3 py-1.5 rounded border border-gray-200 dark:border-gray-700"
            />
            <p class="text-[10px] text-gray-500 mt-1">Leave blank for auto-detect via DoH probe.</p>
          </div>
          <div v-if="editPolicy === 'round-robin-region'" class="space-y-2 border-t border-gray-200 dark:border-gray-700 pt-3">
            <div>
              <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">Rotation interval (min)</label>
              <input
                v-model.number="editIntervalMin"
                type="number"
                min="1"
                class="w-full bg-gray-50 dark:bg-gray-800 px-3 py-1.5 rounded border border-gray-200 dark:border-gray-700"
              />
            </div>
            <label class="flex items-center gap-2 text-xs text-gray-700 dark:text-gray-300">
              <input v-model="editIdleAware" type="checkbox" class="rounded" />
              Idle-aware (don't rotate during traffic)
            </label>
            <div>
              <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">Force-rotate after (min idle-blocked)</label>
              <input
                v-model.number="editForceAfterMin"
                type="number"
                min="1"
                class="w-full bg-gray-50 dark:bg-gray-800 px-3 py-1.5 rounded border border-gray-200 dark:border-gray-700"
              />
            </div>
          </div>
        </div>
        <div class="flex justify-end gap-2 px-4 py-3 border-t border-gray-200 dark:border-gray-700">
          <button @click="showSettingsModal = false" class="px-3 py-1.5 text-[11px] text-gray-500 hover:text-gray-700 dark:hover:text-gray-300">
            Cancel
          </button>
          <button @click="saveSettings" class="px-3 py-1.5 text-[11px] font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md">
            Save
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { usePoolStore, type PoolPolicy } from '@/stores/pool'
import {
  ArrowLeftIcon,
  Cog6ToothIcon,
  PencilSquareIcon,
  TrashIcon,
  XMarkIcon,
} from '@heroicons/vue/24/outline'

const route = useRoute()
const router = useRouter()
const poolStore = usePoolStore()

const pool = ref<any>(null)
const coverage = ref<any[]>([])
const loading = ref(true)
const memberFilter = ref('')

// Edit modal state
const showSettingsModal = ref(false)
const editName = ref('')
const editPolicy = ref<PoolPolicy>('geo-nearest')
const editCountryOverride = ref('')
const editIntervalMin = ref(30)
const editIdleAware = ref(true)
const editForceAfterMin = ref(60)

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
      editIdleAware.value = pool.value.rotation?.idle_aware ?? true
      editForceAfterMin.value = pool.value.rotation?.force_after_min || 60
    }
  } catch (e) {
    console.error(e)
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  if (!pool.value) return
  try {
    await poolStore.update(pool.value.id, {
      name: editName.value,
      policy: editPolicy.value,
      country_override: editCountryOverride.value || '',
      rotation: {
        interval_min: editIntervalMin.value,
        idle_aware: editIdleAware.value,
        force_after_min: editForceAfterMin.value,
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
