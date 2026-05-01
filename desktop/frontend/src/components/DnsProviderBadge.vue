<template>
  <span
    class="inline-flex items-center justify-center rounded-md font-bold flex-shrink-0"
    :class="sizeClass"
    :style="{ backgroundColor: visual.color, color: visual.textColor }"
  >
    {{ visual.letter }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  id: string
  size?: 'sm' | 'md'
}>(), {
  size: 'sm',
})

const sizeClass = computed(() =>
  props.size === 'md'
    ? 'w-6 h-6 text-xs'
    : 'w-5 h-5 text-[10px]'
)

// Brand-color + initial mapping. Variant-providers (cloudflare-malware,
// adguard-family, mullvad-adblock, etc.) inherit the parent brand's
// color and letter — the dropdown's text label disambiguates the
// specific variant. This keeps the visual library small (5 brand
// identities) while supporting all 10+ provider entries.
const visual = computed(() => {
  const id = props.id
  if (id.startsWith('cloudflare')) {
    return { color: '#F38020', letter: 'C', textColor: '#FFFFFF' }
  }
  if (id === 'google') {
    return { color: '#4285F4', letter: 'G', textColor: '#FFFFFF' }
  }
  if (id === 'quad9') {
    return { color: '#005AAB', letter: '9', textColor: '#FFFFFF' }
  }
  if (id.startsWith('adguard')) {
    return { color: '#67B279', letter: 'A', textColor: '#FFFFFF' }
  }
  if (id.startsWith('mullvad')) {
    return { color: '#FFD23F', letter: 'M', textColor: '#000000' }
  }
  return { color: '#6B7280', letter: '?', textColor: '#FFFFFF' }
})
</script>
