<template>
  <div class="p-4 overflow-y-auto max-h-[calc(100vh-7rem)]">
    <div class="flex items-center gap-2 mb-4">
      <button @click="$router.back()" class="text-gray-500 dark:text-gray-400 hover:text-gray-900 dark:hover:text-white">
        <ArrowLeftIcon class="w-5 h-5" />
      </button>
      <h2 class="text-sm font-semibold text-gray-600 dark:text-gray-300">Split Tunnel</h2>
    </div>

    <!-- Platform-specific availability. Per-app split tunnel relies on
         OS primitives that vary wildly: Android has a first-class
         VpnService.addDisallowedApplication API; desktop OSes need
         cgroups (Linux), WFP process-filter (Windows), or Network
         Extensions (macOS, Apple-signed only). We surface this honestly
         instead of pretending it works. -->
    <div class="card p-4 mb-3">
      <div class="flex items-start gap-3">
        <InformationCircleIcon class="w-5 h-5 text-primary-400 mt-0.5 flex-shrink-0" />
        <div>
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white mb-1">
            Per-app split tunnel is not yet available on desktop
          </h3>
          <p class="text-xs text-gray-600 dark:text-gray-400 mb-2">
            Unlike mobile OSes, {{ platformLabel }} does not offer a simple API to route
            specific applications through the VPN and bypass others. Reliable
            implementations need {{ platformNote }}, which we have not shipped yet.
          </p>
          <p class="text-xs text-gray-600 dark:text-gray-400">
            In the meantime you can use <strong>CIDR-based split tunnel</strong>
            via the connection's routing mode (Full vs Split) — this routes by
            destination address instead of by application.
          </p>
          <router-link to="/settings"
            class="inline-block mt-3 text-xs text-primary-400 hover:text-primary-300 underline">
            Open routing settings
          </router-link>
        </div>
      </div>
    </div>

    <!-- Placeholder — keeps the nav target stable for a future feature.
         Tracks with the Android SplitTunnelScreen so when this lands on
         desktop, the UI layer can be wired up to a platform-specific
         backend implementation. -->
    <div class="card p-4 opacity-60">
      <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-3">
        Coming soon
      </h3>
      <ul class="text-xs text-gray-600 dark:text-gray-400 space-y-1 list-disc list-inside">
        <li>Mark individual applications as tunnel-only or bypass</li>
        <li>Search installed apps by name or package</li>
        <li>Persist per-connection preferences to settings</li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { GetPlatformFeatures } from '../../wailsjs/go/main/App'
import { ArrowLeftIcon, InformationCircleIcon } from '@heroicons/vue/24/outline'

const platform = ref<string>('')

const platformLabel = computed(() => {
  switch (platform.value) {
    case 'linux': return 'Linux'
    case 'darwin': return 'macOS'
    case 'windows': return 'Windows'
    default: return 'this system'
  }
})

const platformNote = computed(() => {
  switch (platform.value) {
    case 'linux': return 'cgroup-based packet marking or network namespaces (root required)'
    case 'darwin': return 'a signed Network Extension with per-flow filtering'
    case 'windows': return 'Windows Filtering Platform process-level filters via a driver'
    default: return 'platform-specific OS integration'
  }
})

onMounted(async () => {
  try {
    const pf = await GetPlatformFeatures()
    platform.value = pf.platform || ''
  } catch {
    /* non-fatal */
  }
})
</script>
