// Pinia store for Connection Pools - the "many configs, smart pick"
// virtual-connection feature. Keeps pool list + active pool ID + the
// rotator status hot-cached so the picker UI does not have to call
// into Wails on every render.
//
// We follow the same shape as stores/vpn.ts: a few refs, one
// fetch-everything action invoked by view onMounted hooks, and
// EventsOn for backend-pushed state changes.

import { defineStore } from 'pinia'
import { ref } from 'vue'
import {
  ListPools,
  GetPoolDetail,
  CreatePoolFromPaths,
  CreatePoolFromUploads,
  UpdatePool,
  DeletePool,
  DeletePoolMember,
  RenamePoolMember,
  ActivePoolID,
  ActivatePool,
  SwitchPoolMember,
  PickAndConnectActivePool,
  PoolRotatorStatus,
  SelfIPCountry,
  BootstrapState,
  ResetPoolUnreachable,
} from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'

export type PoolPolicy = 'geo-nearest' | 'random' | 'round-robin-region'

export interface PoolListItem {
  id: string
  name: string
  policy: PoolPolicy
  member_count: number
  active_member_id?: string
  active_member_name?: string
  active_member_cc?: string
  // Set by the rotator's pre-warm step (60s before rotation). When
  // present the UI shows "Next: <name>" alongside the countdown.
  pending_member_id?: string
  pending_member_name?: string
  pending_member_cc?: string
  is_active: boolean
}

export interface PoolMember {
  id: string
  name: string
  config: { protocol: string; server_address: string; filename?: string; config_content?: string }
  country: string
  region: string
  active: boolean
  unreachable: boolean
}

export interface RegionCoverage {
  region: string
  servers: number
  countries: number
}

export interface RotatorStatus {
  active: boolean
  pool_id?: string
  interval_min: number
  idle_aware: boolean
  next_rotation_in: number  // nanoseconds (Go time.Duration)
  idle_blocked: boolean
  force_rotate_in?: number
}

export const usePoolStore = defineStore('pool', () => {
  const pools = ref<PoolListItem[]>([])
  const activePoolId = ref<string>('')
  const rotatorStatus = ref<RotatorStatus | null>(null)
  const userCountry = ref<string>('')
  const loading = ref(false)
  const error = ref<string | null>(null)

  /** Refresh everything pool-related. Cheap (no member lists).
   *  Each IPC fires independently and writes its own ref as soon as
   *  it resolves - so the fastest call (ListPools/ActivePoolID, ~10ms
   *  each, in-memory on the Go side) populates the pool card on the
   *  first frame instead of waiting for the slowest call to land.
   *  Earlier `Promise.all` form blocked rendering until ALL four
   *  resolved - cold-start UX showed an empty pool slot for ~80-150ms
   *  even though the data was already in memory.
   *
   *  Returns when ALL calls have settled so callers that await it
   *  (auto-restore, post-rotation refresh) keep their previous
   *  semantics. UI rendering does not wait. */
  async function refresh() {
    loading.value = true
    error.value = null
    const results = await Promise.allSettled([
      ListPools().then((v) => { pools.value = (v as PoolListItem[]) || [] }),
      ActivePoolID().then((v) => { activePoolId.value = (v as string) || '' }),
      PoolRotatorStatus().then((v) => { rotatorStatus.value = v as RotatorStatus }),
      SelfIPCountry().then((v) => { userCountry.value = (v as string) || '' }),
    ])
    const firstReject = results.find((r) => r.status === 'rejected') as PromiseRejectedResult | undefined
    if (firstReject) {
      error.value = firstReject.reason?.toString?.() || 'failed to load pools'
    }
    loading.value = false
  }

  /** Pull the full member list for a single pool. Heavier than refresh. */
  async function detail(poolId: string) {
    return await GetPoolDetail(poolId)
  }

  /** Hydrate the store from the synchronous BootstrapState snapshot.
   *  Called from main.ts before any view mounts AND as a fallback
   *  inside ConnectionView.onMounted in case the event arrives before
   *  the listener was wired (cold-start race).
   *
   *  We only OVERWRITE refs that are still empty - if a refresh()
   *  has already populated them, bootstrap should not regress to a
   *  smaller snapshot (active pool only vs full list). */
  async function bootstrap() {
    try {
      const snap: any = await BootstrapState()
      if (!snap) return
      if (snap.active_pool_id && !activePoolId.value) {
        activePoolId.value = snap.active_pool_id
      }
      if (snap.active_pool && pools.value.length === 0) {
        pools.value = [snap.active_pool as PoolListItem]
      }
    } catch {
      // bootstrap is a best-effort warm-up; refresh() is the
      // authoritative path. Silently swallow.
    }
  }

  /** Apply a pool:bootstrap event payload (same shape as
   *  BootstrapState). Same overwrite semantics as bootstrap(). */
  function applyBootstrap(snap: any) {
    if (!snap) return
    if (snap.active_pool_id && !activePoolId.value) {
      activePoolId.value = snap.active_pool_id
    }
    if (snap.active_pool && pools.value.length === 0) {
      pools.value = [snap.active_pool as PoolListItem]
    }
  }

  async function create(name: string, policy: PoolPolicy, paths: string[]) {
    const created = await CreatePoolFromPaths(name, policy, paths)
    await refresh()
    return created
  }

  async function createFromUploads(
    name: string,
    policy: PoolPolicy,
    uploads: Array<{ filename: string; content: string }>
  ) {
    const created = await CreatePoolFromUploads(name, policy, uploads)
    await refresh()
    return created
  }

  async function update(id: string, patch: Record<string, any>) {
    const updated = await UpdatePool(id, patch)
    await refresh()
    return updated
  }

  async function remove(id: string) {
    await DeletePool(id)
    await refresh()
  }

  async function removeMember(poolId: string, memberId: string) {
    await DeletePoolMember(poolId, memberId)
  }

  async function renameMember(poolId: string, memberId: string, newName: string) {
    await RenamePoolMember(poolId, memberId, newName)
  }

  async function activate(id: string) {
    await ActivatePool(id)
    await refresh()
  }

  async function deactivate() {
    await ActivatePool('')
    await refresh()
  }

  async function switchMember(memberId: string) {
    await SwitchPoolMember(memberId)
    await refresh()
  }

  async function pickAndConnect() {
    await PickAndConnectActivePool()
    await refresh()
  }

  /** Clear the Unreachable flag on every member of the pool. Returns
   *  the count of members whose flag was cleared so the caller can
   *  surface a meaningful confirmation. */
  async function resetUnreachable(poolId: string): Promise<number> {
    const cleared = await ResetPoolUnreachable(poolId)
    return Number(cleared) || 0
  }

  async function pollRotator() {
    try {
      rotatorStatus.value = (await PoolRotatorStatus()) as RotatorStatus
    } catch {
      // ignore - rotator status is non-critical
    }
  }

  // Listen for backend-emitted import progress so the AddPoolView
  // stepper can render without polling.
  let importListener: (() => void) | null = null
  function onImportProgress(handler: (p: any) => void): () => void {
    if (importListener) importListener()
    importListener = EventsOn('pool:import_progress', handler)
    return () => {
      if (importListener) {
        importListener()
        importListener = null
      }
    }
  }

  return {
    pools,
    activePoolId,
    rotatorStatus,
    userCountry,
    loading,
    error,
    refresh,
    bootstrap,
    applyBootstrap,
    detail,
    create,
    createFromUploads,
    update,
    remove,
    removeMember,
    renameMember,
    activate,
    deactivate,
    switchMember,
    pickAndConnect,
    resetUnreachable,
    pollRotator,
    onImportProgress,
  }
})

/** Format a Go time.Duration (nanoseconds) as "MM:SS" for the rotation timer. */
export function formatDuration(ns: number): string {
  if (!ns || ns <= 0) return '00:00'
  const totalSec = Math.floor(ns / 1_000_000_000)
  const m = Math.floor(totalSec / 60)
  const s = totalSec % 60
  return `${m.toString().padStart(2, '0')}:${s.toString().padStart(2, '0')}`
}
