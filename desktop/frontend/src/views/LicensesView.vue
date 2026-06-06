<template>
  <!-- Self-constraining height + own scroll container — same pattern as
       HelpView/SettingsView (App.vue's flex-1 wrapper doesn't reliably bound
       child height). -->
  <div class="text-sm leading-relaxed overflow-y-auto overflow-x-hidden max-h-[calc(100vh-7rem)] p-4 space-y-4">
    <h2 class="text-lg font-semibold text-gray-800 dark:text-gray-100">{{ $t('licenses.title') }}</h2>

    <p class="text-gray-600 dark:text-gray-300">{{ $t('licenses.intro') }}</p>

    <button
      class="text-primary-600 dark:text-primary-400 underline break-all text-left"
      @click="openSource"
    >{{ sourceUrl }}</button>

    <div>
      <h3 class="text-xs font-semibold text-gray-500 dark:text-gray-400 uppercase tracking-wider mb-2">
        {{ $t('licenses.deps-heading') }}
      </h3>
      <ul class="space-y-2">
        <li v-for="d in deps" :key="d.name" class="flex flex-col">
          <span class="text-gray-800 dark:text-gray-200 font-medium">{{ d.name }}</span>
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ d.license }}<span v-if="d.note"> — {{ d.note }}</span></span>
        </li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
import { BrowserOpenURL } from '../../wailsjs/runtime/runtime'

// Source-availability offer for GPL-3.0 compliance: the full corresponding
// source of this client is public. strongSwan (IPSec) and OpenVPN are GPL-2.0/
// AGPL programs the desktop client INVOKES as external system binaries — they
// are NOT bundled/linked into the app; their own source is provided by their
// projects. The Go libraries that ARE linked in are permissive (MIT).
const sourceUrl = 'https://github.com/hoep/privycs-vpn'

const deps = [
  { name: 'Privycs VPN Client', license: 'GPL-3.0-or-later', note: 'this application' },
  { name: 'amneziawg-go', license: 'MIT' },
  { name: 'wireguard-go', license: 'MIT' },
  { name: 'Wails', license: 'MIT' },
  { name: 'strongSwan (IPSec/IKEv2)', license: 'GPL-2.0', note: 'invoked as an external system binary, not bundled' },
  { name: 'OpenVPN', license: 'GPL-2.0', note: 'invoked as an external system binary, not bundled' },
]

function openSource() {
  BrowserOpenURL(sourceUrl)
}
</script>
