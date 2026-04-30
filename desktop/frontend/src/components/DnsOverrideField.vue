<template>
  <div>
    <input
      :value="modelValue"
      @input="$emit('update:modelValue', ($event.target as HTMLInputElement).value)"
      @blur="$emit('blur')"
      type="text"
      spellcheck="false"
      :placeholder="placeholder || 'e.g. 1.1.1.1, 2606:4700:4700::1111'"
      class="input"
    />
    <div class="mt-2 flex items-center gap-2">
      <span class="text-[10px] text-gray-500 whitespace-nowrap">Or use preset:</span>
      <div class="flex-1 min-w-0">
        <AppSelect
          :model-value="''"
          @update:model-value="applyPreset"
          :options="presetOptions"
          placeholder="— pick a provider —"
        />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { GetDnsProviders } from '../../wailsjs/go/main/App'
import AppSelect from './AppSelect.vue'

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

const presetOptions = computed(() => {
  return dnsProviders.value.map((p: any) => ({
    value: p.id,
    label: `${p.label} — ${p.servers.slice(0, 2).join(', ')}`,
  }))
})

function applyPreset(id: string) {
  if (!id) return
  const preset = dnsProviders.value.find((p: any) => p.id === id)
  if (!preset) return
  emit('update:modelValue', preset.servers.join(', '))
}
</script>
