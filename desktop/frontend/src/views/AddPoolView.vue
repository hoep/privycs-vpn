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
          @click="filePicker?.click()"
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
        <input
          ref="filePicker"
          type="file"
          multiple
          accept=".zip,.conf,.ovpn,.sswan"
          @change="onFileChange"
          class="hidden"
        />
        <p v-if="selectedFiles.length > 0" class="text-[10px] text-primary-400 mt-2">
          {{ $t('add-pool.selected.count', selectedFiles.length, { n: selectedFiles.length }) }}
          <span class="text-gray-500"> ({{ formatBytes(totalSize) }})</span>
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

const router = useRouter()
const pool = usePoolStore()
const { t } = useI18n()

const poolName = ref('')
const policy = ref<PoolPolicy>('geo-nearest')
const selectedFiles = ref<File[]>([])
const dragHover = ref(false)
const filePicker = ref<HTMLInputElement | null>(null)

const importing = ref(false)
const error = ref<string | null>(null)
const progress = ref({ stage: '', current: 0, total: 0, imported: 0, skipped: 0 })

let stopProgressListener: (() => void) | null = null

const canImport = computed(() => poolName.value.trim() !== '' && selectedFiles.value.length > 0)

const totalSize = computed(() => selectedFiles.value.reduce((sum, f) => sum + f.size, 0))

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

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

function onDrop(e: DragEvent) {
  dragHover.value = false
  if (!e.dataTransfer?.files) return
  selectedFiles.value = Array.from(e.dataTransfer.files)
}

function onFileChange(e: Event) {
  const input = e.target as HTMLInputElement
  if (!input.files) return
  selectedFiles.value = Array.from(input.files)
}

// readFileAsBytes returns the file's raw bytes as a Uint8Array. We
// use ArrayBuffer for both ZIPs (binary) and config files (text) for
// uniformity - text files just happen to contain printable ASCII.
function readFileAsBytes(file: File): Promise<Uint8Array> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => {
      const buf = r.result as ArrayBuffer
      resolve(new Uint8Array(buf))
    }
    r.onerror = () => reject(new Error(t('add-pool.error.failed-to-read', { name: file.name, error: String(r.error) })))
    r.readAsArrayBuffer(file)
  })
}

// uint8ToBase64 converts a Uint8Array into a base64 string. Wails
// transparently base64-decodes string fields landing in []byte
// parameters on the Go side, so we ship the content as a base64
// string per upload.
function uint8ToBase64(data: Uint8Array): string {
  let binary = ''
  const chunkSize = 0x8000
  for (let i = 0; i < data.length; i += chunkSize) {
    binary += String.fromCharCode.apply(null, data.subarray(i, i + chunkSize) as unknown as number[])
  }
  return btoa(binary)
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
    // Read every selected file's bytes in JS - the browser sandbox
    // does not give us absolute filesystem paths to pass to the
    // backend, so we ship the content directly. Mirrors how
    // AddConnectionView already works for single configs.
    const uploads = await Promise.all(
      selectedFiles.value.map(async (f) => ({
        filename: f.name,
        content: uint8ToBase64(await readFileAsBytes(f)),
      }))
    )
    await pool.createFromUploads(poolName.value.trim(), policy.value, uploads)
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
})

onUnmounted(() => {
  if (stopProgressListener) stopProgressListener()
})
</script>
