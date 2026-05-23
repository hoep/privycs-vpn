<template>
  <div class="p-4 overflow-y-auto max-h-[calc(100vh-7rem)]">
    <div class="flex items-center gap-2 mb-4">
      <button @click="$router.back()" class="text-gray-500 dark:text-gray-400 hover:text-gray-900 dark:hover:text-white">
        <ArrowLeftIcon class="w-5 h-5" />
      </button>
      <h2 class="text-sm font-semibold text-gray-600 dark:text-gray-300">{{ $t('protocol-selector.title') }}</h2>
    </div>

    <div class="space-y-3">
      <button
        v-for="proto in protocols"
        :key="proto.name"
        @click="selectProtocol(proto.name)"
        :disabled="!proto.available"
        class="w-full text-left card p-4 transition-all border-2"
        :class="[
          vpn.status?.active_protocol === proto.name
            ? 'border-primary-500/50 bg-primary-500/5'
            : proto.available
              ? 'border-transparent hover:border-gray-300 dark:hover:border-gray-600'
              : 'border-transparent opacity-40 cursor-not-allowed'
        ]"
      >
        <div class="flex items-start justify-between">
          <div class="flex items-center gap-3">
            <div class="w-10 h-10 rounded-lg flex items-center justify-center" :class="proto.iconBg">
              <ProtocolIcon :protocol="proto.name" size="lg" />
            </div>
            <div>
              <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ proto.displayName }}</h3>
              <p class="text-xs text-gray-500 dark:text-gray-400 mt-0.5">{{ proto.description }}</p>
            </div>
          </div>
          <div v-if="vpn.status?.active_protocol === proto.name" class="mt-1">
            <CheckCircleIcon class="w-5 h-5 text-primary-400" />
          </div>
        </div>
        <div class="mt-2 flex items-center gap-2 ml-[52px]">
          <span class="text-[10px] font-medium px-1.5 py-0.5 rounded"
            :class="proto.available ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400'">
            {{ proto.available ? $t('protocol-selector.status.available') : $t('protocol-selector.status.not-installed') }}
          </span>
          <span class="text-[10px] text-gray-500">{{ proto.transport }}</span>
        </div>
      </button>
    </div>

    <p v-if="vpn.status?.connected" class="mt-4 text-xs text-gray-500 text-center">
      {{ $t('protocol-selector.disconnect-warning') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useVpnStore } from '@/stores/vpn'
import { SelectProtocol } from '../../wailsjs/go/main/App'
import ProtocolIcon from '@/components/ProtocolIcon.vue'
import {
  ArrowLeftIcon,
  CheckCircleIcon,
} from '@heroicons/vue/24/outline'

const { t } = useI18n()
const router = useRouter()
const vpn = useVpnStore()

const protocols = computed(() => {
  const available = vpn.protocols || []
  const availableMap = new Map(available.map((p: any) => [p.name, p.available]))

  return [
    {
      name: 'amneziawg',
      displayName: 'AmneziaWG',
      description: t('protocol-selector.amneziawg.description'),
      transport: t('protocol-selector.amneziawg.transport'),
      available: availableMap.get('amneziawg') ?? false,
      iconBg: 'bg-indigo-500/20',
    },
    {
      name: 'wireguard',
      displayName: 'WireGuard',
      description: t('protocol-selector.wireguard.description'),
      transport: t('protocol-selector.wireguard.transport'),
      available: availableMap.get('wireguard') ?? false,
      iconBg: 'bg-red-900/20',
    },
    {
      name: 'openvpn',
      displayName: 'OpenVPN',
      description: t('protocol-selector.openvpn.description'),
      transport: t('protocol-selector.openvpn.transport'),
      available: availableMap.get('openvpn') ?? false,
      iconBg: 'bg-orange-500/20',
    },
    {
      name: 'ipsec',
      displayName: 'IPSec / IKEv2',
      description: t('protocol-selector.ipsec.description'),
      transport: t('protocol-selector.ipsec.transport'),
      available: availableMap.get('ipsec') ?? false,
      iconBg: 'bg-blue-500/20',
    },
  ]
})

async function selectProtocol(name: string) {
  try {
    await SelectProtocol(name)
    // Update local state immediately — don't wait for slow Status() poll
    if (vpn.status) {
      vpn.status.active_protocol = name
    }
    router.push('/connection')
  } catch (e: any) {
    vpn.error = t('protocol-selector.error.switch-failed')
  }
}
</script>
