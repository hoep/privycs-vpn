<template>
  <span class="inline-flex items-center justify-center flex-shrink-0" :class="sizeClass">
    <!-- Cloudflare — orange cloud (1.1.1.1 mark). Used for all
         Cloudflare DNS variants (standard / malware-block /
         family-filter); the dropdown text-label disambiguates
         which filter level applies. -->
    <svg
      v-if="brand === 'cloudflare'"
      :class="sizeClass"
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      aria-label="Cloudflare"
    >
      <path
        fill="#F38020"
        d="M21.5 12.6c-.2-1.5-1.4-2.7-2.9-2.9-.8-.1-1.6.1-2.3.4-.6.3-1.4 0-1.7-.7-.7-1.6-2.2-2.7-4-2.7-2.4 0-4.4 2-4.4 4.4 0 .3 0 .5.1.8 0 .2-.1.5-.4.5C3.7 12.6 2 14.3 2 16.5c0 2.3 1.9 4.2 4.2 4.2h13.6c1.5 0 2.7-1.2 2.7-2.7 0-1-.4-1.9-1-2.5-.3-.3-.3-.7 0-1 .5-.5 0-1.4 0-1.9z"
      />
    </svg>

    <!-- Google — official 4-color G mark. -->
    <svg
      v-else-if="brand === 'google'"
      :class="sizeClass"
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      aria-label="Google"
    >
      <path
        fill="#4285F4"
        d="M22.56 12.25c0-.78-.07-1.53-.2-2.25H12v4.26h5.92c-.26 1.37-1.04 2.53-2.21 3.31v2.77h3.57c2.08-1.92 3.28-4.74 3.28-8.09z"
      />
      <path
        fill="#34A853"
        d="M12 23c2.97 0 5.46-.98 7.28-2.66l-3.57-2.77c-.98.66-2.23 1.06-3.71 1.06-2.86 0-5.29-1.93-6.16-4.53H2.18v2.84C3.99 20.53 7.7 23 12 23z"
      />
      <path
        fill="#FBBC05"
        d="M5.84 14.09c-.22-.66-.35-1.36-.35-2.09s.13-1.43.35-2.09V7.07H2.18C1.43 8.55 1 10.22 1 12s.43 3.45 1.18 4.93l2.85-2.22.81-.62z"
      />
      <path
        fill="#EA4335"
        d="M12 5.38c1.62 0 3.06.56 4.21 1.64l3.15-3.15C17.45 2.09 14.97 1 12 1 7.7 1 3.99 3.47 2.18 7.07l3.66 2.84c.87-2.6 3.3-4.53 6.16-4.53z"
      />
    </svg>

    <!-- Quad9 — dark-blue shield with "9". Brand uses both green
         and blue; their primary mark is the dark-blue shield. -->
    <svg
      v-else-if="brand === 'quad9'"
      :class="sizeClass"
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      aria-label="Quad9"
    >
      <path
        fill="#005AAB"
        d="M12 1.5L3 4.6v6.5c0 5.4 3.7 10.4 9 11.9 5.3-1.5 9-6.5 9-11.9V4.6l-9-3.1z"
      />
      <text
        x="12"
        y="15.5"
        text-anchor="middle"
        fill="#FFFFFF"
        font-size="10"
        font-weight="700"
        font-family="Arial, sans-serif"
      >9</text>
    </svg>

    <!-- AdGuard — green rounded shield. AdGuard's brand mark is
         a stylised shield. Variants (standard / family / unfiltered)
         all share the shield; dropdown text disambiguates. -->
    <svg
      v-else-if="brand === 'adguard'"
      :class="sizeClass"
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      aria-label="AdGuard"
    >
      <path
        fill="#67B279"
        d="M12 1.5C8.5 2.5 5.3 3.2 3 3.4v8.5c0 5.3 3.7 10.2 9 11.6 5.3-1.4 9-6.3 9-11.6V3.4c-2.3-.2-5.5-.9-9-1.9zm-1.4 14.7L7 12.6l1.6-1.6 2 2 4.8-4.8L17 9.7l-6.4 6.5z"
      />
    </svg>

    <!-- Mullvad — yellow rounded square with mole-silhouette M.
         Mullvad's 2024 rebrand kept yellow as primary. -->
    <svg
      v-else-if="brand === 'mullvad'"
      :class="sizeClass"
      viewBox="0 0 24 24"
      xmlns="http://www.w3.org/2000/svg"
      aria-label="Mullvad"
    >
      <rect width="24" height="24" rx="4" fill="#FFD23F" />
      <path
        fill="#191919"
        d="M5 17V7h2.5l3 7.5L13.5 7H16v10h-2V11l-2.4 6h-2.2L7 11v6H5z"
      />
    </svg>

    <!-- Fallback for unknown providers — neutral grey dot. -->
    <span
      v-else
      class="block w-full h-full rounded-md bg-gray-500"
      :aria-label="t('components.dns-provider-badge.generic-label')"
    />
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = withDefaults(defineProps<{
  id: string
  size?: 'sm' | 'md'
}>(), {
  size: 'sm',
})

const sizeClass = computed(() =>
  props.size === 'md' ? 'w-6 h-6' : 'w-5 h-5'
)

// Variant providers (cloudflare-malware, adguard-family, etc.)
// inherit the parent brand. The dropdown's text label
// disambiguates the specific variant.
const brand = computed(() => {
  const id = props.id
  if (id.startsWith('cloudflare')) return 'cloudflare'
  if (id === 'google') return 'google'
  if (id === 'quad9') return 'quad9'
  if (id.startsWith('adguard')) return 'adguard'
  if (id.startsWith('mullvad')) return 'mullvad'
  return 'unknown'
})
</script>
