<template>
  <div class="p-4 overflow-y-auto max-h-[calc(100vh-7rem)]">
    <div class="flex items-center gap-2 mb-4">
      <button @click="$router.back()" class="text-gray-500 dark:text-gray-400 hover:text-gray-900 dark:hover:text-white">
        <ArrowLeftIcon class="w-5 h-5" />
      </button>
      <h2 class="text-sm font-semibold text-gray-600 dark:text-gray-300">
        Add Connection Pool
      </h2>
    </div>

    <div class="card p-4 space-y-4">
      <!-- Pool name -->
      <div>
        <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">Pool name</label>
        <input
          v-model="poolName"
          type="text"
          placeholder="e.g. Mullvad WG"
          class="w-full bg-gray-50 dark:bg-gray-800 text-sm text-gray-900 dark:text-white px-3 py-2 rounded-lg border border-gray-200 dark:border-gray-700 focus:outline-none focus:ring-2 focus:ring-primary-500"
        />
      </div>

      <!-- File drop zone -->
      <div>
        <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">Configuration files</label>
        <div
          @drop.prevent="onDrop"
          @dragover.prevent="dragHover = true"
          @dragleave.prevent="dragHover = false"
          @click="filePicker?.click()"
          :class="['border-2 border-dashed rounded-lg p-6 text-center cursor-pointer transition-colors',
                   dragHover ? 'border-primary-500 bg-primary-500/10' : 'border-gray-300 dark:border-gray-700 hover:border-primary-400']"
        >
          <ArrowDownTrayIcon class="w-8 h-8 mx-auto text-gray-400 mb-2" />
          <p class="text-xs text-gray-600 dark:text-gray-400">
            Drop a ZIP, or multiple
            <span class="font-mono">.conf</span> /
            <span class="font-mono">.ovpn</span> /
            <span class="font-mono">.sswan</span> files
          </p>
          <p class="text-[10px] text-gray-500 mt-1">or click to browse</p>
        </div>
        <input
          ref="filePicker"
          type="file"
          multiple
          accept=".zip,.conf,.ovpn,.sswan"
          @change="onFileChange"
          class="hidden"
        />
        <p v-if="selectedPaths.length > 0" class="text-[10px] text-primary-400 mt-2">
          Selected: {{ selectedPaths.length }} file<span v-if="selectedPaths.length !== 1">s</span>
        </p>
      </div>

      <!-- Policy -->
      <div>
        <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">Selection policy</label>
        <select
          v-model="policy"
          class="w-full bg-gray-50 dark:bg-gray-800 text-sm text-gray-900 dark:text-white px-3 py-2 rounded-lg border border-gray-200 dark:border-gray-700 focus:outline-none focus:ring-2 focus:ring-primary-500"
        >
          <option value="geo-nearest">Geo-Nearest (closest country)</option>
          <option value="random">Random</option>
          <option value="round-robin-region">Round-Robin Region</option>
        </select>
        <p class="text-[10px] text-gray-500 mt-1">
          {{ policyDescription }}
        </p>
      </div>

      <!-- Import progress -->
      <div v-if="importing" class="bg-primary-500/10 rounded-lg p-3 space-y-1">
        <p class="text-xs text-primary-400 font-medium">{{ progressLabel }}</p>
        <div v-if="progress.total > 0" class="w-full bg-gray-200 dark:bg-gray-700 rounded-full h-1.5">
          <div
            class="bg-primary-500 h-1.5 rounded-full transition-all"
            :style="{ width: `${(progress.current / progress.total) * 100}%` }"
          ></div>
        </div>
        <p class="text-[10px] text-gray-500">
          {{ progress.imported }} imported, {{ progress.skipped }} skipped
        </p>
      </div>

      <!-- Error -->
      <p v-if="error" class="text-xs text-red-400">{{ error }}</p>

      <!-- Actions -->
      <div class="flex justify-end gap-2 pt-2">
        <button
          @click="$router.back()"
          class="px-3 py-1.5 text-xs text-gray-500 hover:text-gray-700 dark:hover:text-gray-300"
        >
          Cancel
        </button>
        <button
          @click="doImport"
          :disabled="!canImport || importing"
          class="px-3 py-1.5 text-xs font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {{ importing ? 'Importing...' : 'Import Pool' }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { usePoolStore, type PoolPolicy } from '@/stores/pool'
import { ArrowLeftIcon, ArrowDownTrayIcon } from '@heroicons/vue/24/outline'

const router = useRouter()
const pool = usePoolStore()

const poolName = ref('')
const policy = ref<PoolPolicy>('geo-nearest')
const selectedPaths = ref<string[]>([])
const dragHover = ref(false)
const filePicker = ref<HTMLInputElement | null>(null)

const importing = ref(false)
const error = ref<string | null>(null)
const progress = ref({ stage: '', current: 0, total: 0, imported: 0, skipped: 0 })

let stopProgressListener: (() => void) | null = null

const canImport = computed(() => poolName.value.trim() !== '' && selectedPaths.value.length > 0)

const policyDescription = computed(() => {
  switch (policy.value) {
    case 'geo-nearest': return 'Picks the closest server to your country (auto-detected). Falls back to same-region or random.'
    case 'random':      return 'Picks a random server from the pool on every connect.'
    case 'round-robin-region': return 'Rotates through different regions on a timer. Configure interval after import.'
  }
  return ''
})

const progressLabel = computed(() => {
  if (progress.value.stage === 'extracting') return 'Extracting archive...'
  if (progress.value.stage === 'parsing')    return `Resolving endpoints... ${progress.value.current}/${progress.value.total}`
  if (progress.value.stage === 'done')       return 'Finishing...'
  return 'Working...'
})

function onDrop(e: DragEvent) {
  dragHover.value = false
  if (!e.dataTransfer?.files) return
  selectedPaths.value = Array.from(e.dataTransfer.files).map(f => (f as any).path || f.name)
}

function onFileChange(e: Event) {
  const input = e.target as HTMLInputElement
  if (!input.files) return
  selectedPaths.value = Array.from(input.files).map(f => (f as any).path || f.name)
}

async function doImport() {
  if (!canImport.value) return
  importing.value = true
  error.value = null
  progress.value = { stage: '', current: 0, total: 0, imported: 0, skipped: 0 }

  stopProgressListener = pool.onImportProgress((p: any) => {
    progress.value = p
  })

  try {
    await pool.create(poolName.value.trim(), policy.value, selectedPaths.value)
    router.push('/connections')
  } catch (e: any) {
    error.value = e?.toString() || 'import failed'
  } finally {
    importing.value = false
    if (stopProgressListener) {
      stopProgressListener()
      stopProgressListener = null
    }
  }
}

onMounted(() => {
  pool.refresh()
})

onUnmounted(() => {
  if (stopProgressListener) stopProgressListener()
})
</script>
