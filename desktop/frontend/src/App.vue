<template>
  <div class="min-h-screen bg-gray-100 dark:bg-gray-900 text-gray-900 dark:text-gray-200 flex flex-col">
    <!-- Header -->
    <header class="flex items-center justify-between px-4 py-3 bg-white dark:bg-gray-800 border-b border-gray-200 dark:border-gray-700/50">
      <div class="flex items-center gap-2">
        <img src="@/assets/images/privycs-logo.png" alt="Privycs" class="w-7 h-7 rounded-lg" />
        <span class="text-sm font-semibold text-gray-700 dark:text-gray-200">Privycs VPN</span>
        <span v-if="apiUser" class="text-[10px] text-gray-400 dark:text-gray-500 truncate max-w-[140px]">{{ apiUser }}</span>
      </div>
      <div class="flex items-center gap-1">
        <span v-if="vpn.status?.connected" class="w-2 h-2 rounded-full bg-green-400 animate-pulse"></span>
        <span v-else class="w-2 h-2 rounded-full bg-gray-500"></span>
      </div>
    </header>

    <!-- Main Content -->
    <main class="flex-1 overflow-y-auto">
      <router-view v-slot="{ Component }">
        <transition name="fade" mode="out-in">
          <component :is="Component" />
        </transition>
      </router-view>
    </main>

    <!-- Pool import progress toast: subscribes globally to
         "pool:import_progress" events so the user sees XX of YY
         feedback regardless of which view they navigate to while
         a 600-server provider ZIP is resolving. -->
    <PoolImportToast />

    <!-- App-startup loading toast: surfaces background tasks (SelfIP
         DoH probe, MMDB country backfill) that would otherwise look
         like the app is frozen for a few seconds at cold start. -->
    <AppLoadingToast />

    <!-- Bottom Navigation -->
    <nav class="flex items-center justify-around py-2 bg-white dark:bg-gray-800 border-t border-gray-200 dark:border-gray-700/50">
      <router-link
        v-for="item in navItems"
        :key="item.path"
        :to="item.path"
        class="flex flex-col items-center gap-0.5 px-3 py-1 rounded-lg transition-colors"
        :class="isActiveRoute(item.path) ? 'text-primary-400' : 'text-gray-500 hover:text-gray-300'"
      >
        <component :is="item.icon" class="w-5 h-5" />
        <span class="text-[10px] font-medium">{{ item.label }}</span>
      </router-link>
    </nav>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, markRaw } from 'vue'
import { useRoute } from 'vue-router'
import { useVpnStore } from '@/stores/vpn'
import PoolImportToast from '@/components/PoolImportToast.vue'
import AppLoadingToast from '@/components/AppLoadingToast.vue'
import {
  ShieldCheckIcon,
  Cog6ToothIcon,
  PlusCircleIcon,
  QueueListIcon,
} from '@heroicons/vue/24/outline'

const route = useRoute()
const vpn = useVpnStore()
const apiUser = ref(localStorage.getItem('privycs-api-user') || '')

const navItems = [
  { path: '/connection', label: 'Connect', icon: markRaw(ShieldCheckIcon) },
  { path: '/connections', label: 'Configs', icon: markRaw(QueueListIcon) },
  { path: '/add', label: 'Add', icon: markRaw(PlusCircleIcon) },
  { path: '/settings', label: 'Settings', icon: markRaw(Cog6ToothIcon) },
]

function isActiveRoute(path: string) {
  return route.path === path
}

onMounted(async () => {
  vpn.init()
  // Load theme from saved settings and apply immediately
  try {
    const { GetSettings } = await import('../wailsjs/go/main/App')
    const settings = await GetSettings()
    const theme = settings?.theme || 'system'
    const systemDark = window.matchMedia('(prefers-color-scheme: dark)').matches
    const useDark = theme === 'dark' || (theme === 'system' && systemDark)
    if (useDark) {
      document.documentElement.classList.add('dark')
    } else {
      document.documentElement.classList.remove('dark')
    }
  } catch {
    // Settings not available yet, keep system default
  }
})

onUnmounted(() => {
  vpn.stopListening()
})
</script>
