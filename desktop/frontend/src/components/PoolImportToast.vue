<template>
  <transition name="toast-slide">
    <div
      v-if="visible"
      class="fixed bottom-20 right-4 z-50 w-72 max-w-[calc(100vw-2rem)] bg-white dark:bg-gray-800 rounded-xl shadow-2xl border border-primary-500/30 overflow-hidden"
    >
      <div class="px-3 py-2.5">
        <div class="flex items-center justify-between mb-1.5">
          <span class="text-[11px] font-semibold text-primary-400">
            {{ headline }}
          </span>
          <span v-if="hasNumeric" class="text-[10px] text-gray-500 tabular-nums">
            {{ progress.current }} / {{ progress.total }}
          </span>
        </div>

        <!-- Progress bar -->
        <div v-if="hasNumeric" class="w-full bg-gray-200 dark:bg-gray-700 rounded-full h-1.5 mb-1.5">
          <div
            class="bg-primary-500 h-1.5 rounded-full transition-all duration-200"
            :style="{ width: `${(progress.current / progress.total) * 100}%` }"
          ></div>
        </div>

        <!-- Activity line -->
        <p class="text-[10px] text-gray-500 dark:text-gray-400 truncate">
          {{ activityLine }}
        </p>
      </div>
    </div>
  </transition>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'

interface Progress {
  stage: string
  current: number
  total: number
  imported: number
  skipped: number
  message?: string
}

const progress = ref<Progress>({ stage: '', current: 0, total: 0, imported: 0, skipped: 0 })
const visible = ref(false)

let stopListener: (() => void) | null = null
let hideTimer: ReturnType<typeof setTimeout> | null = null

const hasNumeric = computed(() => progress.value.total > 0)

const headline = computed(() => {
  switch (progress.value.stage) {
    case 'extracting': return 'Extracting archive'
    case 'parsing':    return 'Parsing configs'
    case 'resolving':  return 'Resolving endpoints'
    case 'done':       return 'Import complete'
    default:           return 'Pool import'
  }
})

const activityLine = computed(() => {
  if (progress.value.stage === 'done') {
    return `${progress.value.imported} imported, ${progress.value.skipped} skipped`
  }
  if (progress.value.message) {
    return progress.value.message
  }
  if (progress.value.skipped > 0) {
    return `${progress.value.skipped} skipped so far`
  }
  return ''
})

onMounted(() => {
  // Subscribe at app-level so the toast persists across route changes.
  // The user can navigate from AddPool back to Connections while the
  // import is running and still see progress.
  stopListener = EventsOn('pool:import_progress', (p: Progress) => {
    progress.value = p
    visible.value = true

    // Clear any prior auto-hide timer; reset on every event so the
    // toast stays visible as long as progress is flowing.
    if (hideTimer) {
      clearTimeout(hideTimer)
      hideTimer = null
    }

    // Auto-hide when import completes. 4s gives the user a moment
    // to read the final imported/skipped counts.
    if (p.stage === 'done') {
      hideTimer = setTimeout(() => {
        visible.value = false
      }, 4000)
    }
  })
})

onUnmounted(() => {
  if (stopListener) stopListener()
  if (hideTimer) clearTimeout(hideTimer)
})
</script>

<style scoped>
.toast-slide-enter-active,
.toast-slide-leave-active {
  transition: all 0.25s ease-out;
}
.toast-slide-enter-from,
.toast-slide-leave-to {
  opacity: 0;
  transform: translateX(20px);
}
</style>
