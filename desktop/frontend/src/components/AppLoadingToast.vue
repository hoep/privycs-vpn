<template>
  <transition name="toast-slide">
    <div
      v-if="visible"
      class="fixed bottom-20 left-4 z-50 w-72 max-w-[calc(100vw-2rem)] bg-white dark:bg-gray-800 rounded-xl shadow-2xl border border-primary-500/30 overflow-hidden"
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

        <!-- Progress bar (only when total > 0) -->
        <div v-if="hasNumeric" class="w-full bg-gray-200 dark:bg-gray-700 rounded-full h-1.5 mb-1.5">
          <div
            class="bg-primary-500 h-1.5 rounded-full transition-all duration-200"
            :style="{ width: `${(progress.current / Math.max(progress.total, 1)) * 100}%` }"
          ></div>
        </div>

        <!-- Indeterminate spinner for stages without a known total
             (e.g. SelfIP DoH probe). Keeps the UI alive so the user
             knows something is happening. -->
        <div v-else class="w-full h-1.5 mb-1.5 overflow-hidden rounded-full bg-gray-200 dark:bg-gray-700">
          <div class="h-full w-1/3 bg-primary-500 rounded-full animate-progress-indeterminate"></div>
        </div>

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

interface AppLoadingPayload {
  stage: string
  current: number
  total: number
  extra: string
}

// Local state. progress is the latest payload; visible drives the
// fade-in transition. hideTimer auto-dismisses 2.5s after the *-done
// event so the user sees the final state but it does not linger.
const progress = ref<AppLoadingPayload>({ stage: '', current: 0, total: 0, extra: '' })
const visible = ref(false)

let stopListener: (() => void) | null = null
let hideTimer: ReturnType<typeof setTimeout> | null = null

const hasNumeric = computed(() => progress.value.total > 0)

const headline = computed(() => {
  switch (progress.value.stage) {
    case 'geo-detect':       return 'Detecting your location'
    case 'geo-detect-done':  return progress.value.extra
      ? `Location: ${progress.value.extra}`
      : 'Location unknown'
    case 'backfill':         return 'Loading exit-point countries'
    case 'backfill-done':    return 'Exit points ready'
    default:                 return 'Working...'
  }
})

const activityLine = computed(() => {
  switch (progress.value.stage) {
    case 'geo-detect':       return 'Probing public IP via DoH...'
    case 'geo-detect-done':  return 'Used by Geo-Nearest pool selection'
    case 'backfill':         return `Resolving ${progress.value.current} of ${progress.value.total}...`
    case 'backfill-done':    return 'Country flags now visible'
    default:                 return ''
  }
})

function clearHide() {
  if (hideTimer) {
    clearTimeout(hideTimer)
    hideTimer = null
  }
}

onMounted(() => {
  stopListener = EventsOn('app:loading', (p: AppLoadingPayload) => {
    progress.value = p
    visible.value = true
    clearHide()

    // *-done stages auto-dismiss after a short read window. Active
    // stages stay visible until the next event flips them.
    if (p.stage.endsWith('-done')) {
      hideTimer = setTimeout(() => {
        visible.value = false
      }, 2500)
    }
  })
})

onUnmounted(() => {
  if (stopListener) stopListener()
  clearHide()
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
  transform: translateX(-20px);
}

@keyframes progress-indeterminate {
  0%   { transform: translateX(-100%); }
  100% { transform: translateX(400%); }
}
.animate-progress-indeterminate {
  animation: progress-indeterminate 1.4s ease-in-out infinite;
}
</style>
