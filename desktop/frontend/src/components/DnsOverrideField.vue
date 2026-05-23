<template>
  <div>
    <input
      :value="modelValue"
      @input="$emit('update:modelValue', ($event.target as HTMLInputElement).value)"
      @blur="$emit('blur')"
      type="text"
      spellcheck="false"
      :placeholder="placeholder || $t('components.dns-override.placeholder')"
      class="input"
    />
    <div class="mt-2 flex items-center gap-2">
      <span class="text-[10px] text-gray-500 whitespace-nowrap">{{ $t('components.dns-override.preset-label') }}</span>
      <div class="flex-1 min-w-0">
        <Listbox :model-value="''" @update:model-value="applyPreset">
          <div class="relative">
            <ListboxButton
              class="relative w-full cursor-pointer rounded-md border border-gray-300 dark:border-gray-600
                     bg-white dark:bg-gray-800 py-1.5 pl-3 pr-8 text-left text-sm
                     text-gray-900 dark:text-gray-200
                     focus:outline-none focus:ring-1 focus:ring-primary-500 focus:border-primary-500
                     min-w-[8rem]"
            >
              <span class="block truncate text-gray-400 dark:text-gray-500">{{ $t('components.dns-override.pick-provider') }}</span>
              <span class="pointer-events-none absolute inset-y-0 right-0 flex items-center pr-2">
                <ChevronUpDownIcon class="h-4 w-4 text-gray-400" />
              </span>
            </ListboxButton>

            <!-- min-w-full w-max max-w-sm so long provider labels stay
                 readable. Per-option layout is a flex row with the
                 brand-colored badge on the left then the label. -->
            <ListboxOptions
              class="absolute z-20 mt-1 max-h-60 min-w-full w-max max-w-sm overflow-auto rounded-md
                     bg-white dark:bg-gray-800 py-1 text-sm shadow-lg
                     ring-1 ring-black/5 dark:ring-white/10
                     focus:outline-none"
            >
              <ListboxOption
                v-for="p in dnsProviders"
                :key="p.id"
                :value="p.id"
                v-slot="{ active }"
                as="template"
              >
                <li
                  class="relative cursor-pointer select-none py-2 pl-3 pr-9 transition-colors flex items-center gap-2.5"
                  :class="active ? 'bg-primary-500/10 text-primary-600 dark:text-primary-400' : 'text-gray-900 dark:text-gray-200'"
                >
                  <DnsProviderBadge :id="p.id" />
                  <div class="flex flex-col min-w-0">
                    <span class="block truncate font-normal">{{ p.label }}</span>
                    <span class="block truncate text-[10px] text-gray-400 dark:text-gray-500">
                      {{ p.servers.slice(0, 2).join(', ') }}
                    </span>
                  </div>
                </li>
              </ListboxOption>
            </ListboxOptions>
          </div>
        </Listbox>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Listbox, ListboxButton, ListboxOption, ListboxOptions } from '@headlessui/vue'
import { ChevronUpDownIcon } from '@heroicons/vue/24/outline'
import { GetDnsProviders } from '../../wailsjs/go/main/App'
import DnsProviderBadge from './DnsProviderBadge.vue'

defineProps<{
  modelValue: string
  placeholder?: string
}>()

const emit = defineEmits(['update:modelValue', 'blur'])

const dnsProviders = ref<any[]>([])

onMounted(async () => {
  try {
    dnsProviders.value = await GetDnsProviders() as any[]
  } catch {
    dnsProviders.value = []
  }
})

function applyPreset(id: string) {
  if (!id) return
  const preset = dnsProviders.value.find((p: any) => p.id === id)
  if (!preset) return
  emit('update:modelValue', preset.servers.join(', '))
}
</script>
