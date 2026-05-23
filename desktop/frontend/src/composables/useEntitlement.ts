// useEntitlement — single source of Pro-tier state for the entire UI.
//
// Mounts a one-shot fetch from the Go backend's GetEntitlement, then
// keeps the local ref reactive via the 'entitlement:changed' Wails
// event. Components calling useEntitlement get the same reactive ref
// across the app (module-singleton pattern).

import { ref, readonly } from 'vue'

// Wails-generated runtime bindings. The wailsjs/ folder is auto-
// generated at build time so we import lazily inside functions to keep
// the test runner happy (it may not have the runtime stub installed).
//
// Lazy module-level imports keep this composable usable in
// dev-without-Wails contexts (Vite + Vue running standalone for UI
// experiments).

interface EntitlementState {
  source: string
  license_key?: string
  is_pro: boolean
  first_activated?: string
  last_verified?: string
}

const emptyState: EntitlementState = {
  source: '',
  is_pro: false,
}

const entitlementRef = ref<EntitlementState>(emptyState)
const gatingEnabledRef = ref<boolean>(false)

let mounted = false

// hydrate calls the backend once on first useEntitlement() invocation
// and wires the event listener. Idempotent — repeat calls do nothing.
async function hydrate() {
  if (mounted) return
  mounted = true
  try {
    const [{ GetEntitlement, ProGatingEnabledJS }, { EventsOn }] = await Promise.all([
      import('../../wailsjs/go/main/App'),
      import('../../wailsjs/runtime/runtime'),
    ])
    const [state, gating] = await Promise.all([GetEntitlement(), ProGatingEnabledJS()])
    entitlementRef.value = (state as EntitlementState) ?? emptyState
    gatingEnabledRef.value = !!gating
    EventsOn('entitlement:changed', (s: EntitlementState) => {
      entitlementRef.value = s ?? emptyState
    })
  } catch (e) {
    // wailsjs/* not present (e.g. running Vite outside Wails) — fall
    // back to empty state. The UI will treat the user as free tier;
    // good default for dev experiments.
    console.warn('useEntitlement: hydrate failed', e)
  }
}

export function useEntitlement() {
  // Fire-and-forget hydrate so the first caller doesn't have to await.
  hydrate()

  // proGateAllowed: feature-gate helper. When gating is globally off
  // (ProGatingEnabled=false in the Go build), returns true unconditionally
  // — features stay open while we prepare the marketing rollout. Once the
  // constant flips, non-Pro users get false back and the caller is
  // expected to show the UpgradeDialog instead of running the action.
  function proGateAllowed(): boolean {
    if (!gatingEnabledRef.value) return true
    return entitlementRef.value.is_pro
  }

  async function activate(rawKey: string): Promise<EntitlementState> {
    const { ActivateLicenseKey } = await import('../../wailsjs/go/main/App')
    const out = await ActivateLicenseKey(rawKey)
    entitlementRef.value = (out as EntitlementState) ?? emptyState
    return entitlementRef.value
  }

  async function deactivate(): Promise<void> {
    const { DeactivateLicense } = await import('../../wailsjs/go/main/App')
    const out = await DeactivateLicense()
    entitlementRef.value = (out as EntitlementState) ?? emptyState
  }

  async function openStore(sku: 'privycs_pro_desktop' | 'privycs_pro_bundle_all'): Promise<void> {
    const { OpenStoreURL } = await import('../../wailsjs/go/main/App')
    await OpenStoreURL(sku)
  }

  return {
    entitlement: readonly(entitlementRef),
    gatingEnabled: readonly(gatingEnabledRef),
    isPro: () => entitlementRef.value.is_pro,
    proGateAllowed,
    activate,
    deactivate,
    openStore,
  }
}
