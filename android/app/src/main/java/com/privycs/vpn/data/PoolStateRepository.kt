package com.privycs.vpn.data

import android.content.Context
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.io.File
import java.time.Instant

/**
 * Pool runtime-state persistence — Android port of pool_state.go.
 *
 * Why split out from PoolRepository:
 *   - pools.json is large (~150KB at 600 members) but rarely changes.
 *   - State (active member, pending member, slot, unreachable flags)
 *     changes 4-6× per rotation. Coalescing those into the big
 *     definition file would write ~150KB on every status flip; with
 *     state.json it's <2KB regardless of pool size.
 *   - Eliminates the desktop-era race where multiple goroutines wrote
 *     to *PoolMember pointers from EligibleMembers, markUnreachable
 *     etc. concurrently. Every mutation now goes through a method
 *     that takes a Mutex, with a debounced flusher coroutine writing
 *     atomically.
 *
 * Lifecycle:
 *   - Construct from Application class (process-scoped).
 *   - close() at process exit (Application.onTerminate isn't reliable
 *     on Android, but lifecycleScope cancellation forces a final flush).
 */
@Serializable
data class MemberStateEntry(
    val unreachable: Boolean = false,
    @SerialName("last_unreachable")
    val lastUnreachable: String = "",                   // RFC3339, "" = never
    @SerialName("last_error")
    val lastError: String = ""
)

@Serializable
data class PoolStateEntry(
    @SerialName("active_member_id")
    val activeMemberId: String = "",
    @SerialName("pending_member_id")
    val pendingMemberId: String = "",
    @SerialName("active_slot")
    val activeSlot: String = "",
    val members: MutableMap<String, MemberStateEntry> = mutableMapOf(),
    /**
     * Per-region "last picked member" cursor. Round-Robin advances
     * through region members in deterministic ID-sorted order using
     * this cursor instead of picking randomly within. Closes the
     * privacy hole where the same exit IP could be re-picked within
     * a few rotations.
     */
    @SerialName("member_cursors")
    val memberCursors: MutableMap<String, String> = mutableMapOf()
)

@Serializable
data class PoolStateFile(
    val pools: MutableMap<String, PoolStateEntry> = mutableMapOf()
)

class PoolStateRepository(private val context: Context) {

    private val json = Json {
        prettyPrint = true
        ignoreUnknownKeys = true
        encodeDefaults = true
    }

    private val file: File
        get() = File(context.filesDir, "pool_state.json")

    private val mutex = Mutex()
    private var state: PoolStateFile = load()

    /** Coroutine scope for the debounced flusher. Cancelled on close(). */
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    /** Signals the flusher that mutations have happened. */
    private val dirtySignal = Channel<Unit>(capacity = 1)

    init {
        scope.launch {
            for (signal in dirtySignal) {
                // Debounce window: 500ms. Coalesces rapid sequential
                // mutations (rotation triggers 4-6 in quick succession)
                // into one disk write.
                delay(FLUSH_DEBOUNCE_MS)
                flushNow()
            }
        }
    }

    /**
     * Reads the runtime state for a member. Empty entry means
     * "no failures recorded" which is the correct semantic for
     * "reachable, never failed" — fresh imports start there.
     */
    suspend fun memberState(poolId: String, memberId: String): MemberStateEntry =
        mutex.withLock {
            state.pools[poolId]?.members?.get(memberId) ?: MemberStateEntry()
        }

    /**
     * Synchronous variant for hot paths where coroutine context
     * isn't available.
     *
     * WARNING: NEVER call from the UI thread - blocks until the
     * mutex is acquired. Intended for the PoolAlarmReceiver fast-
     * path (already on a worker thread) and tests. UI/Compose code
     * uses the suspend memberState() instead.
     */
    fun memberStateBlocking(poolId: String, memberId: String): MemberStateEntry =
        runBlocking { memberState(poolId, memberId) }

    suspend fun isMemberUnreachable(poolId: String, memberId: String): Boolean =
        memberState(poolId, memberId).unreachable

    suspend fun activeMemberId(poolId: String): String =
        mutex.withLock { state.pools[poolId]?.activeMemberId.orEmpty() }

    suspend fun pendingMemberId(poolId: String): String =
        mutex.withLock { state.pools[poolId]?.pendingMemberId.orEmpty() }

    suspend fun activeSlot(poolId: String): String =
        mutex.withLock { state.pools[poolId]?.activeSlot.orEmpty() }

    suspend fun regionCursor(poolId: String, region: String): String =
        mutex.withLock { state.pools[poolId]?.memberCursors?.get(region).orEmpty() }

    /**
     * Marks a member unreachable with reason + timestamp. Idempotent:
     * repeat calls update the timestamp (intentional — a repeatedly-
     * failing member's TTL clock effectively resets, lengthening its
     * time out of rotation).
     */
    suspend fun markMemberUnreachable(poolId: String, memberId: String, reason: String) {
        mutex.withLock {
            val entry = state.pools.getOrPut(poolId) { PoolStateEntry() }
            entry.members[memberId] = MemberStateEntry(
                unreachable = true,
                lastUnreachable = Instant.now().toString(),
                lastError = reason
            )
            markDirty()
        }
    }

    /** Clears the unreachable flag for a single member. */
    suspend fun clearMemberUnreachable(poolId: String, memberId: String) {
        mutex.withLock {
            val entry = state.pools[poolId] ?: return
            val mem = entry.members[memberId] ?: return
            if (mem.unreachable || mem.lastError.isNotEmpty()) {
                entry.members[memberId] = mem.copy(unreachable = false, lastError = "")
                markDirty()
            }
        }
    }

    /**
     * Clears every member's unreachable flag for the pool. Returns
     * count cleared so the UI can show "Reset 12 unreachable members".
     */
    suspend fun clearAllMembersUnreachable(poolId: String): Int =
        mutex.withLock {
            val entry = state.pools[poolId] ?: return@withLock 0
            var cleared = 0
            for ((id, mem) in entry.members) {
                if (mem.unreachable) {
                    entry.members[id] = mem.copy(unreachable = false, lastError = "")
                    cleared++
                }
            }
            if (cleared > 0) markDirty()
            cleared
        }

    /**
     * Lazy TTL clear: members flagged unreachable longer than ttl
     * become eligible again. Returns count cleared. Called from
     * EligibleMembers' read path so stale flags rehabilitate without
     * a separate sweeper coroutine.
     */
    suspend fun sweepStaleUnreachable(poolId: String, ttlMs: Long): Int =
        mutex.withLock {
            val entry = state.pools[poolId] ?: return@withLock 0
            val now = Instant.now()
            var cleared = 0
            for ((id, mem) in entry.members) {
                if (!mem.unreachable) continue
                val ts = runCatching { Instant.parse(mem.lastUnreachable) }.getOrNull()
                if (ts != null && (now.toEpochMilli() - ts.toEpochMilli()) > ttlMs) {
                    entry.members[id] = mem.copy(unreachable = false, lastError = "")
                    cleared++
                }
            }
            if (cleared > 0) markDirty()
            cleared
        }

    suspend fun setActiveMember(poolId: String, memberId: String) {
        mutex.withLock {
            val entry = state.pools.getOrPut(poolId) { PoolStateEntry() }
            if (entry.activeMemberId == memberId) return
            state.pools[poolId] = entry.copy(activeMemberId = memberId)
            markDirty()
        }
    }

    suspend fun setPendingMember(poolId: String, memberId: String) {
        mutex.withLock {
            val entry = state.pools.getOrPut(poolId) { PoolStateEntry() }
            if (entry.pendingMemberId == memberId) return
            state.pools[poolId] = entry.copy(pendingMemberId = memberId)
            markDirty()
        }
    }

    suspend fun setActiveSlot(poolId: String, slot: String) {
        mutex.withLock {
            val entry = state.pools.getOrPut(poolId) { PoolStateEntry() }
            if (entry.activeSlot == slot) return
            state.pools[poolId] = entry.copy(activeSlot = slot)
            markDirty()
        }
    }

    suspend fun setRegionCursor(poolId: String, region: String, memberId: String) {
        mutex.withLock {
            val entry = state.pools.getOrPut(poolId) { PoolStateEntry() }
            if (entry.memberCursors[region] == memberId) return
            entry.memberCursors[region] = memberId
            markDirty()
        }
    }

    /** Removes all state for a pool (called when the pool is deleted). */
    suspend fun deletePool(poolId: String) {
        mutex.withLock {
            if (state.pools.remove(poolId) != null) markDirty()
        }
    }

    /** Removes one member's state, cleaning up active/pending dangling refs. */
    suspend fun deleteMember(poolId: String, memberId: String) {
        mutex.withLock {
            val entry = state.pools[poolId] ?: return
            var dirty = false
            if (entry.members.remove(memberId) != null) dirty = true
            val next = entry.copy(
                activeMemberId = if (entry.activeMemberId == memberId) "" else entry.activeMemberId,
                pendingMemberId = if (entry.pendingMemberId == memberId) "" else entry.pendingMemberId
            )
            if (next.activeMemberId != entry.activeMemberId ||
                next.pendingMemberId != entry.pendingMemberId
            ) {
                state.pools[poolId] = next
                dirty = true
            }
            if (dirty) markDirty()
        }
    }

    /**
     * Cancels the flusher and forces a synchronous final save.
     * Call from Application#onLowMemory + ServiceLifecycleObserver.
     *
     * Race-safe: closes the dirty channel FIRST (so no new flush
     * scheduling is possible), then forces a final flush, then
     * cancels the scope. Without this order a markDirty fired
     * during close() could leave its mutation un-persisted because
     * the flusher coroutine was cancelled before draining.
     */
    fun close() {
        dirtySignal.close()
        runBlocking { flushNow() }
        scope.cancel()
    }

    private fun markDirty() {
        // Non-blocking signal. Channel capacity 1 means rapid-fire
        // signals coalesce automatically; the flusher debounces
        // FLUSH_DEBOUNCE_MS after the last signal.
        dirtySignal.trySend(Unit)
    }

    private suspend fun flushNow() {
        mutex.withLock {
            try {
                val data = json.encodeToString(PoolStateFile.serializer(), state)
                val tmp = File(file.parentFile, file.name + ".tmp")
                tmp.writeText(data)
                if (!tmp.renameTo(file)) {
                    // Rename failed; fall back to direct overwrite.
                    file.writeText(data)
                    tmp.delete()
                }
                Unit  // Force the withLock block to return Unit
                      // explicitly. Without this, Kotlin's inferrer
                      // tries to make the if-expression the block's
                      // value and complains about the missing else.
            } catch (e: Exception) {
                android.util.Log.e(TAG, "flush failed: ${e.message}", e)
            }
        }
    }

    private fun load(): PoolStateFile {
        if (!file.exists()) return PoolStateFile()
        return try {
            json.decodeFromString(PoolStateFile.serializer(), file.readText())
        } catch (e: Exception) {
            android.util.Log.w(TAG, "parse failed: ${e.message} - resetting state", e)
            PoolStateFile()
        }
    }

    companion object {
        private const val TAG = "PoolStateRepo"
        private const val FLUSH_DEBOUNCE_MS = 500L
        const val UNREACHABLE_TTL_MS: Long = 30 * 60 * 1000L  // 30 minutes
    }
}
