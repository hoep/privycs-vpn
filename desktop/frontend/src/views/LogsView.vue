<template>
  <div class="p-4 flex flex-col max-h-[calc(100vh-7rem)]">
    <div class="flex items-center justify-between mb-3">
      <div class="flex items-center gap-2">
        <button @click="$router.back()" class="text-gray-500 dark:text-gray-400 hover:text-gray-900 dark:hover:text-white">
          <ArrowLeftIcon class="w-5 h-5" />
        </button>
        <h2 class="text-sm font-semibold text-gray-600 dark:text-gray-300">Logs</h2>
      </div>
      <button @click="loadLogs" :disabled="loadingLogs" class="text-xs text-primary-400 hover:text-primary-300 disabled:opacity-50">
        {{ loadingLogs ? 'Loading...' : 'Refresh' }}
      </button>
    </div>

    <div ref="logContainer" class="flex-1 overflow-y-auto card p-3 font-mono text-[11px] leading-relaxed">
      <div v-if="logs.length === 0" class="text-gray-500 text-center mt-8">
        No logs available
      </div>
      <div v-for="(line, i) in logs" :key="i" class="text-gray-500 dark:text-gray-400 whitespace-pre-wrap break-all">
        {{ line }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, nextTick } from 'vue'
import { GetLogs } from '../../wailsjs/go/main/App'
import { ArrowLeftIcon } from '@heroicons/vue/24/outline'

const logs = ref<string[]>([])
const logContainer = ref<HTMLElement | null>(null)
const loadingLogs = ref(false)

async function loadLogs() {
  if (loadingLogs.value) return // prevent rapid-fire clicks
  loadingLogs.value = true
  try {
    logs.value = await GetLogs()
    await nextTick()
    if (logContainer.value) {
      logContainer.value.scrollTop = logContainer.value.scrollHeight
    }
  } catch (e) {
    logs.value = ['--- Log file could not be loaded ---']
  } finally {
    loadingLogs.value = false
  }
}

onMounted(loadLogs)
</script>
