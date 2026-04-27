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
import kotlinx.coroutines.runBlocking
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
 * Thread safety: all public methods take an internal mutex via
 * @Synchronized. Goroutine-style fanout from the rotator paths is
 * Coroutine-context-safe because mutations are all short
 * (in-memory + occasional file write).
 *
 * Index caches (poolByID, per-pool memberByID) for O(1) lookups
 * are rebuilt on every load() and update mutation.
 */
@Serializable
data class PoolFile(
    val pools: MutableList<Pool> = mutableListOf(),
    @SerialName("active_id")
    var activeId: String = ""
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

    private val _registry = MutableStateFlow(load())
    val registry: StateFlow<PoolFile> = _registry.asStateFlow()

    /**
     * O(1) pool index. Rebuilt on every mutation. Not persisted.
     */
    private var poolByID: MutableMap<String, Pool> = mutableMapOf()

    /**
     * Per-pool member index, keyed by pool ID then member ID.
     * Rebuilt on member-list mutations.
     */
    private val memberIndexByPool: MutableMap<String, MutableMap<String, PoolMember>> = mutableMapOf()

    init {
        rebuildIndexes()
    }

    val pools: List<Pool>
        get() = _registry.value.pools.toList()

    val activeId: String
        get() = _registry.value.activeId

    @Synchronized
    fun get(id: String): Pool? = poolByID[id]

    @Synchronized
    fun memberById(poolId: String, memberId: String): PoolMember? =
        memberIndexByPool[poolId]?.get(memberId)

    @Synchronized
    fun setActiveId(id: String) {
        if (id.isNotEmpty() && poolByID[id] == null) return  // unknown pool
        val current = _registry.value
        if (current.activeId == id) return
        current.activeId = id
        save()
    }

    @Synchronized
    fun create(name: String, policy: PoolPolicy, members: List<PoolMember>): Pool {
        require(name.isNotBlank()) { "Pool name must not be blank" }
        val pool = Pool(
            id = UUID.randomUUID().toString(),
            name = name,
            createdAt = Instant.now().toString(),
            policy = policy,
            rotation = PoolRotation.default(),
            members = members.toMutableList()
        )
        _registry.value.pools.add(pool)
        rebuildIndexes()
        save()
        return pool
    }

    @Synchronized
    fun update(pool: Pool): Boolean {
        if (poolByID[pool.id] == null) return false
        // Pool pointer is shared with caller — they've mutated it
        // in place. Just persist.
        rebuildIndexes()
        save()
        return true
    }

    @Synchronized
    fun delete(id: String): Boolean {
        val current = _registry.value
        if (!current.pools.removeAll { it.id == id }) return false
        if (current.activeId == id) current.activeId = ""
        rebuildIndexes()
        save()
        // State cleanup happens out-of-lock, on a coroutine.
        runBlocking { state.deletePool(id) }
        return true
    }

    @Synchronized
    fun deleteMember(poolId: String, memberId: String): Boolean {
        val pool = poolByID[poolId] ?: return false
        if (!pool.members.removeAll { it.id == memberId }) return false
        rebuildIndexes()
        save()
        runBlocking { state.deleteMember(poolId, memberId) }
        return true
    }

    @Synchronized
    fun renameMember(poolId: String, memberId: String, newName: String): Boolean {
        require(newName.isNotBlank()) { "Member name must not be blank" }
        val pool = poolByID[poolId] ?: return false
        val mem = memberIndexByPool[poolId]?.get(memberId) ?: return false
        mem.name = newName
        save()
        return true
    }

    /**
     * Returns active members that are not currently flagged
     * unreachable AND match RestrictRegions (if any).
     *
     * Pure read with respect to definition state. Runtime state is
     * consulted via PoolStateRepository (TTL sweep + all-unreachable
     * reset both go through the state repo).
     *
     * suspend because the state-repo methods are suspend.
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

        // All-unreachable reset path: if every region-eligible
        // member was filtered out, we treat that as "local network
        // is broken, not all servers genuinely dead". Clear flags
        // and return them.
        state.clearAllMembersUnreachable(pool.id)
        for (m in pool.members) {
            if (!m.active) continue
            if (pool.restrictRegions.isNotEmpty() && m.region !in pool.restrictRegions) continue
            out.add(m)
        }
        return out
    }

    /**
     * Region-coverage breakdown for the Pool-Detail-View.
     * Returned in descending-server-count order.
     */
    @Synchronized
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
    // Compact wrappers so the rotation hot-paths can do
    // pools.activeMemberId(id) instead of pools.state.activeMemberId(id).
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

    private fun rebuildIndexes() {
        poolByID = _registry.value.pools.associateBy { it.id }.toMutableMap()
        memberIndexByPool.clear()
        for (p in _registry.value.pools) {
            memberIndexByPool[p.id] = p.members.associateBy { it.id }.toMutableMap()
        }
        // Bump StateFlow so observers re-read.
        _registry.value = _registry.value
    }

    private fun save() {
        try {
            val data = json.encodeToString(PoolFile.serializer(), _registry.value)
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
        return try {
            json.decodeFromString(PoolFile.serializer(), file.readText())
        } catch (e: Exception) {
            android.util.Log.w(TAG, "parse failed: ${e.message} - resetting", e)
            PoolFile()
        }
    }

    companion object {
        private const val TAG = "PoolRepo"
    }
}
