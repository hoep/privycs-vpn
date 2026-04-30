<template>
  <div class="p-4 flex flex-col max-h-[calc(100vh-7rem)]">
    <div class="flex items-center justify-between mb-3">
      <div class="flex items-center gap-2">
        <button @click="$router.back()" class="text-gray-500 dark:text-gray-400 hover:text-gray-900 dark:hover:text-white">
          <ArrowLeftIcon class="w-5 h-5" />
        </button>
        <h2 class="text-sm font-semibold text-gray-600 dark:text-gray-300">Logs</h2>
      </div>
      <div class="flex items-center gap-2">
        <button
          @click="copyLogs"
          :disabled="logs.length === 0"
          class="flex items-center gap-1 text-xs text-gray-500 hover:text-primary-400 disabled:opacity-30 disabled:cursor-not-allowed"
          :title="copyLabel"
        >
          <ClipboardIcon class="w-3.5 h-3.5" />
          {{ copyLabel }}
        </button>
        <button
          @click="confirmClear"
          :disabled="logs.length === 0 || clearing"
          class="flex items-center gap-1 text-xs text-gray-500 hover:text-red-400 disabled:opacity-30 disabled:cursor-not-allowed"
          title="Delete the contents of all log files owned by Privycs"
        >
          <TrashIcon class="w-3.5 h-3.5" />
          Clear
        </button>
        <button @click="loadLogs" :disabled="loadingLogs" class="text-xs text-primary-400 hover:text-primary-300 disabled:opacity-50">
          <ArrowPathIcon class="w-3.5 h-3.5" :class="loadingLogs ? 'animate-spin' : ''" />
        </button>
      </div>
    </div>

    <div ref="logContainer" @scroll="onScroll" class="flex-1 overflow-y-auto card p-3 font-mono text-[11px] leading-relaxed">
      <div v-if="logs.length === 0" class="text-gray-500 text-center mt-8">
        No logs available
      </div>
      <div
        v-for="(line, i) in logs"
        :key="i"
        class="whitespace-pre-wrap break-all"
        :class="lineClass(line)"
      >
        {{ line }}
      </div>
    </div>

    <!-- Confirm-clear modal, because Clear is irreversible and we just
         shipped a feature that makes diagnosing user issues easier. Losing
         logs before asking support "what went wrong" defeats the point. -->
    <div v-if="showConfirm" class="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4" @click.self="showConfirm = false">
      <div class="card p-4 max-w-sm w-full">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white mb-2">Clear all logs?</h3>
        <p class="text-xs text-gray-500 dark:text-gray-400 mb-4">
          This deletes every log entry from the app log and the OpenVPN log.
          Cannot be undone.
        </p>
        <div class="flex justify-end gap-2">
          <button @click="showConfirm = false" class="px-3 py-1.5 text-xs rounded-lg bg-gray-200 dark:bg-gray-700 text-gray-600 dark:text-gray-300 hover:bg-gray-300 dark:hover:bg-gray-600">
            Cancel
          </button>
          <button @click="doClear" class="px-3 py-1.5 text-xs rounded-lg bg-red-500 text-white hover:bg-red-600">
            Clear logs
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick, computed } from 'vue'
import { GetLogs, ClearLogs } from '../../wailsjs/go/main/App'
import { ArrowLeftIcon, ArrowPathIcon, ClipboardIcon, TrashIcon } from '@heroicons/vue/24/outline'

const logs = ref<string[]>([])
const logContainer = ref<HTMLElement | null>(null)
const loadingLogs = ref(false)
const clearing = ref(false)
const showConfirm = ref(false)
const copied = ref(false)

const copyLabel = computed(() => copied.value ? 'Copied!' : 'Copy')

// Tracks whether the user has scrolled away from the bottom. If they
// have, the auto-scroll-to-bottom on each refresh would tear them
// away from where they were reading. We only auto-scroll when the
// user is already at (or near) the bottom — same convention as
// every chat / log app.
const userAwayFromBottom = ref(false)

function isNearBottom(el: HTMLElement, slackPx = 40): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= slackPx
}

function onScroll() {
  if (logContainer.value) {
    userAwayFromBottom.value = !isNearBottom(logContainer.value)
  }
}

async function loadLogs(silent = false) {
  if (loadingLogs.value) return
  if (!silent) loadingLogs.value = true
  try {
    const next = await GetLogs()
    // Only assign + scroll if content actually changed. Avoids
    // re-render flicker on every poll tick when the log file is
    // idle. Cheap reference-array compare is overkill; we
    // compare the last line + length as a stable fingerprint.
    const fingerprint =
      next.length + ':' + (next.length > 0 ? next[next.length - 1] : '')
    if (fingerprint === lastFingerprint.value) return
    lastFingerprint.value = fingerprint

    const stickToBottom = !userAwayFromBottom.value
    logs.value = next
    await nextTick()
    if (stickToBottom && logContainer.value) {
      logContainer.value.scrollTop = logContainer.value.scrollHeight
    }
  } catch (e) {
    if (!silent) logs.value = ['--- Log file could not be loaded ---']
  } finally {
    if (!silent) loadingLogs.value = false
  }
}

const lastFingerprint = ref('')

async function copyLogs() {
  try {
    await navigator.clipboard.writeText(logs.value.join('\n'))
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch (e) {
    // Fallback for older WebView2 / WebKit versions without
    // Clipboard API: put logs into a hidden textarea and execCommand
    // the copy. Good enough for any desktop WebView shipping in 2024+.
    const ta = document.createElement('textarea')
    ta.value = logs.value.join('\n')
    document.body.appendChild(ta)
    ta.select()
    try { document.execCommand('copy') } catch {}
    document.body.removeChild(ta)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  }
}

function confirmClear() {
  showConfirm.value = true
}

async function doClear() {
  showConfirm.value = false
  clearing.value = true
  try {
    await ClearLogs()
    await loadLogs()
  } catch (e) {
    // Show error inline — mirrors other destructive actions in the app
    logs.value = [`--- Clear failed: ${e} ---`]
  } finally {
    clearing.value = false
  }
}

// Colorize lines by source tag and severity to make scanning easier.
// Matches Android LogsScreen's subtle color hinting.
function lineClass(line: string): string {
  if (line.startsWith('[openvpn]')) return 'text-blue-500 dark:text-blue-400'
  if (/ERROR|FATAL|failed/i.test(line)) return 'text-red-500 dark:text-red-400'
  if (/WARN|warning/i.test(line)) return 'text-amber-500 dark:text-amber-400'
  return 'text-gray-500 dark:text-gray-400'
}

// Live-tail the log file. Pre-fix the screen used a single
// onMounted(loadLogs) which read the file once and never refreshed,
// so users on the Logs view during a connect / disconnect saw a
// frozen snapshot - the manual ⟳ button was the only way to get
// fresh data. Polling at 1.5s mirrors Android's LogsScreen behavior
// and is cheap (the GetLogs Wails call returns a tail of the same
// rotated 200-KB file). Stops on screen unmount.
let pollHandle: ReturnType<typeof setInterval> | null = null

onMounted(async () => {
  await loadLogs(false)
  pollHandle = setInterval(() => loadLogs(true), 1500)
})

onUnmounted(() => {
  if (pollHandle !== null) {
    clearInterval(pollHandle)
    pollHandle = null
  }
})
</script>
