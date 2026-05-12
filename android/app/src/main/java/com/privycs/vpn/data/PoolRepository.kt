package com.privycs.vpn.data

import android.content.Context
import com.privycs.vpn.data.models.Pool
import com.privycs.vpn.data.models.PoolMember
import com.privycs.vpn.data.models.PoolPolicy
import com.privycs.vpn.data.models.PoolRotation
import com.privycs.vpn.data.models.RegionCoverage
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.io.File
import java.time.Instant
import java.util.UUID

/**
 * Pool definition persistence — Android port of pool_registry.go
 * (post v0.9.11.39 state-separation refactor).
 *
 * Holds Pool DEFINITIONS only (members, policy, rotation params,
 * restriction filter). Runtime state lives in PoolStateRepository.
 *
 * Concurrency: a Coroutine Mutex protects mutations. All public
 * methods are `suspend` so callers compose naturally with the
 * coroutine ecosystem and we never block UI threads with
 * runBlocking.
 *
 * StateFlow semantics: every mutation produces a NEW PoolFile
 * value (immutable copy) so Compose's structural-equality check
 * fires recompositions correctly. Earlier in-place mutation +
 * reassignment-to-self relied on map identity which Compose
 * sometimes treated as no-change → UI staleness. Fixed with
 * data-class copy() and fresh List/Map instances on every
 * mutation that changes membership.
 *
 * Index caches (poolByID, per-pool memberByID) for O(1) lookups
 * are rebuilt on every load and update.
 */
@Serializable
data class PoolFile(
    val pools: List<Pool> = emptyList(),
    @SerialName("active_id")
    val activeId: String = ""
)

class PoolRepository(
    private val context: Context,
    val state: PoolStateRepository
) {

    private val json = Json {
        prettyPrint = true
        ignoreUnknownKeys = true
        encodeDefaults = true
    }

    private val file: File
        get() = File(context.filesDir, "pools.json")

    private val mutex = Mutex()

    private val _registry = MutableStateFlow(load())
    val registry: StateFlow<PoolFile> = _registry.asStateFlow()

    /**
     * O(1) pool index. Rebuilt on every mutation. Not persisted.
     * Volatile-equivalent because all writes happen under mutex.
     */
    private var poolByID: Map<String, Pool> = emptyMap()

    /**
     * Per-pool member index. Rebuilt on member-list mutations.
     */
    private var memberIndexByPool: Map<String, Map<String, PoolMember>> = emptyMap()

    init {
        rebuildIndexes(_registry.value)
    }

    val pools: List<Pool>
        get() = _registry.value.pools

    val activeId: String
        get() = _registry.value.activeId

    fun get(id: String): Pool? = poolByID[id]

    fun memberById(poolId: String, memberId: String): PoolMember? =
        memberIndexByPool[poolId]?.get(memberId)

    /**
     * Marks the active pool. Empty string clears the selection.
     * Validates that the ID exists; unknown IDs are silently ignored
     * (matches desktop behaviour - clear-active is the recovery path).
     */
    suspend fun setActiveId(id: String) = mutex.withLock {
        if (id.isNotEmpty() && poolByID[id] == null) return@withLock
        val current = _registry.value
        if (current.activeId == id) return@withLock
        emitNewState(current.copy(activeId = id))
    }

    suspend fun create(name: String, policy: PoolPolicy, members: List<PoolMember>): Pool =
        mutex.withLock {
            require(name.isNotBlank()) { "Pool name must not be blank" }
            val pool = Pool(
                id = UUID.randomUUID().toString(),
                name = name,
                createdAt = Instant.now().toString(),
                policy = policy,
                rotation = PoolRotation.default(),
                members = members.toMutableList()
            )
            val current = _registry.value
            emitNewState(current.copy(pools = current.pools + pool))
            pool
        }

    /**
     * Adds a pool with its existing ID (no UUID regeneration).
     * Used by the backup-restore path: pools were saved with their
     * original IDs and should reappear with the same IDs so any
     * external references survive (e.g. activeId pointers in the
     * backup payload, scheduled-rotation alarms keyed by pool id).
     *
     * Returns true if the pool was added, false if a pool with
     * the same ID already exists (caller decides whether to update
     * or skip - by default we skip, mirroring the connection-merge
     * semantics in importAndMerge).
     */
    suspend fun restorePool(pool: Pool): Boolean = mutex.withLock {
        if (poolByID[pool.id] != null) return@withLock false
        val current = _registry.value
        emitNewState(current.copy(pools = current.pools + pool))
        true
    }

    /**
     * Persists definition-level changes to a pool the caller
     * mutated in place (name, policy, rotation, restrict-regions,
     * country-override).
     *
     * Returns true on success. Validation: pool ID must exist.
     */
    suspend fun update(pool: Pool): Boolean = mutex.withLock {
        if (poolByID[pool.id] == null) return@withLock false
        // Replace the pool in the list rather than mutating in place,
        // so the StateFlow consumer sees a fresh List instance.
        val newPools = _registry.value.pools.map { if (it.id == pool.id) pool else it }
        emitNewState(_registry.value.copy(pools = newPools))
        true
    }

    /**
     * Returns true if the pool was found and deleted.
     *
     * Side effects (run AFTER the registry mutex is released, in
     * order):
     *   1. state.deletePool() to clean runtime state.
     *   2. If the deleted pool was active: cancel rotation alarms.
     *      Without this, AlarmManager fires zombie PRE_WARM/ROTATE
     *      intents for the gone pool. handlePoolRotate's null-safe
     *      get() avoids crashes, but wasted CPU wakeups, foreground
     *      service starts, and log noise are worth avoiding.
     */
    suspend fun delete(id: String): Boolean {
        var wasActive = false
        val deleted: Boolean = mutex.withLock {
            val current = _registry.value
            if (current.pools.none { it.id == id }) return@withLock false
            wasActive = current.activeId == id
            val newActive = if (wasActive) "" else current.activeId
            emitNewState(current.copy(
                pools = current.pools.filterNot { it.id == id },
                activeId = newActive
            ))
            true
        }
        if (deleted) {
            state.deletePool(id)
            if (wasActive) {
                try {
                    com.privycs.vpn.service.PoolRotationScheduler(context).cancelAll()
                } catch (e: Exception) {
                    android.util.Log.w(TAG, "scheduler cancel after delete failed: ${e.message}")
                }
            }
        }
        return deleted
    }

    suspend fun deleteMember(poolId: String, memberId: String): Boolean = mutex.withLock {
        val pool = poolByID[poolId] ?: return@withLock false
        val newMembers = pool.members.filterNot { it.id == memberId }
        if (newMembers.size == pool.members.size) return@withLock false
        // Replace pool with member-removed copy.
        val newPool = pool.copy(members = newMembers.toMutableList())
        val newPools = _registry.value.pools.map { if (it.id == poolId) newPool else it }
        emitNewState(_registry.value.copy(pools = newPools))
        true
    }.also { ok ->
        if (ok) state.deleteMember(poolId, memberId)
    }

    suspend fun renameMember(poolId: String, memberId: String, newName: String): Boolean =
        mutex.withLock {
            require(newName.isNotBlank()) { "Member name must not be blank" }
            val pool = poolByID[poolId] ?: return@withLock false
            val mem = memberIndexByPool[poolId]?.get(memberId) ?: return@withLock false
            // Rename via copy so the data-class-equality machinery in
            // StateFlow / Compose recomposes correctly.
            val newMembers = pool.members.map {
                if (it.id == memberId) it.copy(name = newName) else it
            }
            val newPool = pool.copy(members = newMembers.toMutableList())
            val newPools = _registry.value.pools.map { if (it.id == poolId) newPool else it }
            emitNewState(_registry.value.copy(pools = newPools))
            true
        }

    /**
     * Returns active members that are not currently flagged
     * unreachable AND match RestrictRegions (if any).
     *
     * Pure read with respect to definition state. Runtime state is
     * consulted via PoolStateRepository (TTL sweep + all-unreachable
     * reset both go through the state repo).
     */
    suspend fun eligibleMembers(pool: Pool): List<PoolMember> {
        // Lazy TTL sweep: members flagged longer than the TTL get
        // re-eligible without manual intervention.
        state.sweepStaleUnreachable(pool.id, PoolStateRepository.UNREACHABLE_TTL_MS)

        val out = mutableListOf<PoolMember>()
        for (m in pool.members) {
            if (!m.active) continue
            if (state.isMemberUnreachable(pool.id, m.id)) continue
            if (pool.restrictRegions.isNotEmpty() && m.region !in pool.restrictRegions) continue
            out.add(m)
        }
        if (out.isNotEmpty()) return out

        // All-unreachable reset path. ONLY clear flags if we have
        // plausible connectivity — otherwise the reset is wasted.
        // Without this guard a global outage (no internet, captive
        // portal, airplane mode toggle) traps the pool in a 30-min
        // re-mark loop: every rotation tries each member, fails,
        // re-marks unreachable, rotation timer re-fires, repeat. By
        // keeping the marks when we know the world isn't reachable,
        // we let the genuine recovery path (network returns, then
        // user-triggered reconnect) start from clean state instead.
        if (!hasPlausibleConnectivity()) {
            android.util.Log.w(TAG, "all members unreachable AND no connectivity - keeping marks")
            return out
        }
        state.clearAllMembersUnreachable(pool.id)
        for (m in pool.members) {
            if (!m.active) continue
            if (pool.restrictRegions.isNotEmpty() && m.region !in pool.restrictRegions) continue
            out.add(m)
        }
        return out
    }

    /**
     * Quick-and-cheap connectivity check via ConnectivityManager.
     * Returns true if there is any active non-VPN network with the
     * INTERNET capability. Does NOT validate captive-portal status
     * (that requires a probe round-trip). Bias is "yes, try" — on
     * error or unknown, return true so we don't accidentally lock
     * users out of the all-unreachable reset path.
     */
    private fun hasPlausibleConnectivity(): Boolean {
        return try {
            val cm = context.getSystemService(Context.CONNECTIVITY_SERVICE)
                as android.net.ConnectivityManager
            val net = cm.activeNetwork ?: return false
            val caps = cm.getNetworkCapabilities(net) ?: return false
            caps.hasCapability(android.net.NetworkCapabilities.NET_CAPABILITY_INTERNET) &&
                !caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_VPN)
        } catch (e: Exception) {
            true
        }
    }

    /**
     * Region-coverage breakdown for the Pool-Detail-View.
     * Returned in descending-server-count order. Pure read,
     * non-suspending — coverage is definition-only data.
     */
    fun coverage(pool: Pool): List<RegionCoverage> {
        val byRegion = mutableMapOf<String, MutableSet<String>>()
        val serverCount = mutableMapOf<String, Int>()
        for (m in pool.members) {
            if (!m.active) continue
            val r = m.region.ifEmpty { "Other" }
            serverCount[r] = (serverCount[r] ?: 0) + 1
            if (m.country.isNotEmpty()) {
                byRegion.getOrPut(r) { mutableSetOf() }.add(m.country)
            } else {
                byRegion.getOrPut(r) { mutableSetOf() }
            }
        }
        return serverCount.map { (region, servers) ->
            RegionCoverage(
                region = region,
                servers = servers,
                countries = byRegion[region]?.size ?: 0
            )
        }.sortedWith(
            compareByDescending<RegionCoverage> { it.servers }.thenBy { it.region }
        )
    }

    // ========================================================================
    // RUNTIME STATE HELPERS — delegate to PoolStateRepository.
    // ========================================================================

    suspend fun activeMemberId(poolId: String) = state.activeMemberId(poolId)
    suspend fun pendingMemberId(poolId: String) = state.pendingMemberId(poolId)
    suspend fun activeSlot(poolId: String) = state.activeSlot(poolId)
    suspend fun setActiveMember(poolId: String, memberId: String) =
        state.setActiveMember(poolId, memberId)
    suspend fun setPendingMember(poolId: String, memberId: String) =
        state.setPendingMember(poolId, memberId)
    suspend fun setActiveSlot(poolId: String, slot: String) =
        state.setActiveSlot(poolId, slot)
    suspend fun markMemberUnreachable(poolId: String, memberId: String, reason: String) =
        state.markMemberUnreachable(poolId, memberId, reason)
    suspend fun clearAllMembersUnreachable(poolId: String) =
        state.clearAllMembersUnreachable(poolId)
    suspend fun isMemberUnreachable(poolId: String, memberId: String) =
        state.isMemberUnreachable(poolId, memberId)

    /**
     * Atomically updates the StateFlow to a new immutable PoolFile,
     * rebuilds index caches, and persists to disk. Caller must hold
     * the mutex. Save is best-effort (logged on failure).
     */
    private fun emitNewState(newState: PoolFile) {
        _registry.value = newState
        rebuildIndexes(newState)
        saveSync(newState)
    }

    private fun rebuildIndexes(snapshot: PoolFile) {
        poolByID = snapshot.pools.associateBy { it.id }
        memberIndexByPool = snapshot.pools.associate { p ->
            p.id to p.members.associateBy { it.id }
        }
    }

    private fun saveSync(snapshot: PoolFile) {
        try {
            val data = json.encodeToString(PoolFile.serializer(), snapshot)
            val tmp = File(file.parentFile, file.name + ".tmp")
            tmp.writeText(data)
            if (!tmp.renameTo(file)) {
                file.writeText(data)
                tmp.delete()
            }
        } catch (e: Exception) {
            android.util.Log.e(TAG, "save failed: ${e.message}", e)
        }
    }

    private fun load(): PoolFile {
        if (!file.exists()) return PoolFile()
        val parsed = try {
            json.decodeFromString(PoolFile.serializer(), file.readText())
        } catch (e: Exception) {
            android.util.Log.w(TAG, "parse failed: ${e.message} - resetting", e)
            return PoolFile()
        }
        // Reclassify legacy WIREGUARD pool members whose configContent
        // contains AmneziaWG keys → AMNEZIAWG. Mirror of the
        // ConnectionRepository.load() heal. v0.9.15.x: AWG is its own
        // protocol slot, no longer a runtime variant. Pools must be
        // sortenrein (homogeneous per protocol type), but a pool of
        // formerly-WIREGUARD configs that's actually all-AWG simply
        // migrates wholesale — the slot rename keeps it homogeneous.
        var migrated = false
        val healedPools = parsed.pools.map { pool ->
            val healedMembers = pool.members.map { m ->
                val pc = m.config
                if (pc.protocol == com.privycs.vpn.data.models.VpnProtocol.WIREGUARD &&
                    com.privycs.vpn.data.models.TunnelVariant.detect(pc.configContent) ==
                        com.privycs.vpn.data.models.TunnelVariant.AMNEZIAWG
                ) {
                    migrated = true
                    m.copy(config = pc.copy(protocol = com.privycs.vpn.data.models.VpnProtocol.AMNEZIAWG))
                } else m
            }.toMutableList()
            pool.copy(members = healedMembers)
        }.toMutableList()
        if (migrated) {
            android.util.Log.i(TAG, "migrated legacy WIREGUARD pool members to AMNEZIAWG slot")
            val healedFile = parsed.copy(pools = healedPools)
            try {
                file.parentFile?.mkdirs()
                file.writeText(json.encodeToString(PoolFile.serializer(), healedFile))
            } catch (e: Exception) {
                android.util.Log.w(TAG, "post-migration save failed: ${e.message}")
            }
            return healedFile
        }
        return parsed
    }

    companion object {
        private const val TAG = "PoolRepo"
    }
}
