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

  /** Refresh everything pool-related. Cheap (no member lists). */
  async function refresh() {
    loading.value = true
    error.value = null
    try {
      pools.value = (await ListPools()) || []
      activePoolId.value = (await ActivePoolID()) || ''
      rotatorStatus.value = (await PoolRotatorStatus()) as RotatorStatus
      userCountry.value = (await SelfIPCountry()) || ''
    } catch (e: any) {
      error.value = e?.toString() || 'failed to load pools'
    } finally {
      loading.value = false
    }
  }

  /** Pull the full member list for a single pool. Heavier than refresh. */
  async function detail(poolId: string) {
    return await GetPoolDetail(poolId)
  }

  async function create(name: string, policy: PoolPolicy, paths: string[]) {
    const created = await CreatePoolFromPaths(name, policy, paths)
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
    detail,
    create,
    update,
    remove,
    removeMember,
    renameMember,
    activate,
    deactivate,
    switchMember,
    pickAndConnect,
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
