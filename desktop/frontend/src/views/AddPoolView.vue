<template>
  <div class="p-4 overflow-y-auto max-h-[calc(100vh-7rem)]">
    <div class="flex items-center gap-2 mb-4">
      <button @click="$router.back()" class="text-gray-500 dark:text-gray-400 hover:text-gray-900 dark:hover:text-white">
        <ArrowLeftIcon class="w-5 h-5" />
      </button>
      <h2 class="text-sm font-semibold text-gray-600 dark:text-gray-300">
        {{ $t('add-pool.title') }}
      </h2>
    </div>

    <div class="card p-4 space-y-4">
      <!-- Pool name -->
      <div>
        <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">{{ $t('add-pool.field.name') }}</label>
        <input
          v-model="poolName"
          type="text"
          :placeholder="$t('add-pool.field.name-placeholder')"
          class="w-full bg-gray-50 dark:bg-gray-800 text-sm text-gray-900 dark:text-white px-3 py-2 rounded-lg border border-gray-200 dark:border-gray-700 focus:outline-none focus:ring-2 focus:ring-primary-500"
        />
      </div>

      <!-- File drop zone -->
      <div>
        <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">{{ $t('add-pool.field.config-files') }}</label>
        <div
          @drop.prevent="onDrop"
          @dragover.prevent="dragHover = true"
          @dragleave.prevent="dragHover = false"
          @click="openNativePicker"
          :class="['border-2 border-dashed rounded-lg p-6 text-center cursor-pointer transition-colors',
                   dragHover ? 'border-primary-500 bg-primary-500/10' : 'border-gray-300 dark:border-gray-700 hover:border-primary-400']"
        >
          <ArrowDownTrayIcon class="w-8 h-8 mx-auto text-gray-400 mb-2" />
          <i18n-t keypath="add-pool.dropzone.instructions" tag="p" class="text-xs text-gray-600 dark:text-gray-400">
            <template #conf><span class="font-mono">.conf</span></template>
            <template #ovpn><span class="font-mono">.ovpn</span></template>
            <template #sswan><span class="font-mono">.sswan</span></template>
          </i18n-t>
          <p class="text-[10px] text-gray-500 mt-1">{{ $t('add-pool.dropzone.or-browse') }}</p>
        </div>
        <p v-if="fileCount > 0" class="text-[10px] text-primary-400 mt-2">
          {{ $t('add-pool.selected.count', { n: fileCount }, fileCount) }}
        </p>
      </div>

      <!-- Policy -->
      <div>
        <label class="block text-[11px] font-medium text-gray-500 dark:text-gray-400 mb-1">{{ $t('add-pool.field.policy') }}</label>
        <select
          v-model="policy"
          class="w-full bg-gray-50 dark:bg-gray-800 text-sm text-gray-900 dark:text-white px-3 py-2 rounded-lg border border-gray-200 dark:border-gray-700 focus:outline-none focus:ring-2 focus:ring-primary-500"
        >
          <option value="geo-nearest">{{ $t('add-pool.policy.geo-nearest') }}</option>
          <option value="random">{{ $t('add-pool.policy.random') }}</option>
          <option value="round-robin-region">{{ $t('add-pool.policy.round-robin-region') }}</option>
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
          {{ $t('add-pool.progress.imported-skipped', { imported: progress.imported, skipped: progress.skipped }) }}
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
          {{ $t('add-pool.button.cancel') }}
        </button>
        <button
          @click="doImport"
          :disabled="!canImport || importing"
          class="px-3 py-1.5 text-xs font-medium text-white bg-primary-600 hover:bg-primary-700 rounded-md disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {{ importing ? $t('add-pool.button.importing') : $t('add-pool.button.import') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { usePoolStore, type PoolPolicy } from '@/stores/pool'
import { ArrowLeftIcon, ArrowDownTrayIcon } from '@heroicons/vue/24/outline'
import { OnFileDrop, OnFileDropOff } from '../../wailsjs/runtime/runtime'
import { PickPoolConfigFiles } from '../../wailsjs/go/main/App'

const router = useRouter()
const pool = usePoolStore()
const { t } = useI18n()

const poolName = ref('')
const policy = ref<PoolPolicy>('geo-nearest')
// Absolute file paths chosen via the native OS dialog (click) or delivered by
// Wails' native file-drop (OnFileDrop). Both go through the path-based importer
// CreatePoolFromPaths — Go reads the files server-side. We deliberately do NOT
// use <input type=file> + FileReader: on the desktop webviews (macOS WKWebView
// especially) FileReader on a picked file can resolve neither onload nor
// onerror and base64-encoding a large ZIP in JS fails silently, so the import
// did nothing — no error, no backend call, no log.
const selectedPaths = ref<string[]>([])
const dragHover = ref(false)

const importing = ref(false)
const error = ref<string | null>(null)
const progress = ref({ stage: '', current: 0, total: 0, imported: 0, skipped: 0 })

let stopProgressListener: (() => void) | null = null

const fileCount = computed(() => selectedPaths.value.length)

const canImport = computed(() => poolName.value.trim() !== '' && fileCount.value > 0)

const policyDescription = computed(() => {
  switch (policy.value) {
    case 'geo-nearest': return t('add-pool.policy.geo-nearest-description')
    case 'random':      return t('add-pool.policy.random-description')
    case 'round-robin-region': return t('add-pool.policy.round-robin-region-description')
  }
  return ''
})

const progressLabel = computed(() => {
  if (progress.value.stage === 'extracting') return t('add-pool.progress.extracting')
  if (progress.value.stage === 'parsing')    return t('add-pool.progress.parsing', { current: progress.value.current, total: progress.value.total })
  if (progress.value.stage === 'resolving')  return t('add-pool.progress.resolving', { current: progress.value.current, total: progress.value.total })
  if (progress.value.stage === 'done')       return t('add-pool.progress.finishing')
  return t('add-pool.progress.working')
})

// onDrop only resets the hover state. The actual dropped files arrive through
// Wails' native OnFileDrop callback (registered in onMounted) as absolute
// paths — the HTML5 dataTransfer.files is empty for OS file drags on the
// desktop webviews, which is exactly why the silent "nothing happens" bug
// existed before EnableFileDrop + OnFileDrop were wired up.
function onDrop() {
  dragHover.value = false
}

// openNativePicker opens the OS file dialog via Go and stores the chosen
// absolute paths. Replaces the old <input type=file> + FileReader path which
// failed silently on macOS WKWebView.
async function openNativePicker() {
  try {
    const paths = await PickPoolConfigFiles()
    if (paths && paths.length > 0) selectedPaths.value = paths
  } catch (e: any) {
    error.value = e?.toString() || t('add-pool.error.import-failed')
  }
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
    // Both click (native dialog) and drop deliver absolute paths — Go reads
    // the files directly (CreatePoolFromPaths). No FileReader / base64 round
    // trip, and large provider ZIPs stream off disk server-side.
    await pool.create(poolName.value.trim(), policy.value, selectedPaths.value)
    router.push('/connections')
  } catch (e: any) {
    error.value = e?.toString() || t('add-pool.error.import-failed')
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
  // Receive OS file drops as absolute paths (the only reliable way on the
  // desktop webviews — HTML5 dataTransfer.files is empty for OS drags).
  // useDropTarget=false: any file dropped on the window while this view is
  // mounted counts, so the user can drop anywhere over the page, not only
  // the dashed zone. Filter to importable extensions defensively.
  OnFileDrop((_x, _y, paths) => {
    const accepted = (paths || []).filter((p) =>
      /\.(zip|conf|ovpn|sswan)$/i.test(p)
    )
    if (accepted.length === 0) return
    selectedPaths.value = accepted
    dragHover.value = false
  }, false)
})

onUnmounted(() => {
  if (stopProgressListener) stopProgressListener()
  OnFileDropOff()
})
</script>
