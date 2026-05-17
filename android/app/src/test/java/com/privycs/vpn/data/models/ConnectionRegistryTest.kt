package com.privycs.vpn.data.models

import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Guards the v0.9.15.63 root cause + fix invariant.
 *
 * Bug: ConnectionRepository mutators edit nested lists in place,
 * then save()/notifyChanged() reassign _registry.value to a
 * .copy() to force a StateFlow re-emit. Because ConnectionRegistry
 * is a data class (structural equals) and .copy() is shallow, the
 * "new" value was .equals() the held one → MutableStateFlow
 * conflated the emission → the UI never recomposed until a manual
 * page switch.
 *
 * Fix: a @Transient `rev` counter in the primary constructor that
 * participates in equals (so a bumped value is genuinely distinct)
 * but is excluded from JSON (old persisted files still load,
 * default 0). Pure model behaviour — no Android framework.
 */
class ConnectionRegistryTest {

    private fun pc(id: String) = ProtocolConfig(
        protocol = VpnProtocol.WIREGUARD,
        configContent = "[Interface]\n# $id",
        filename = "$id.conf",
        id = id,
    )

    private fun sampleRegistry() = ConnectionRegistry(
        connections = mutableListOf(
            VpnConnection(
                id = "conn-1",
                name = "Home",
                activeProtocol = VpnProtocol.WIREGUARD,
                protocols = mutableListOf(pc("a"), pc("b")),
                activeConfigId = "a",
            ),
        ),
        activeId = "conn-1",
    )

    /**
     * Demonstrates the bug: after an in-place nested mutation a
     * plain .copy() is STILL structurally equal to the held value,
     * so StateFlow would conflate it. This test pins that property
     * so the reason the fix is necessary stays documented.
     */
    @Test
    fun plainCopyAfterInPlaceMutation_isStillEqual_henceConflated() {
        val reg = sampleRegistry()
        // Simulate ConnectionRepository.removeConfig: mutate the
        // nested protocols list in place.
        reg.connections[0].protocols.removeAll { it.id == "b" }

        // .copy() shares the (already mutated) connections list →
        // structurally equal → StateFlow.value setter conflates it.
        assertEquals(reg, reg.copy())
    }

    /**
     * The fix invariant: bumping rev makes the emitted value
     * genuinely distinct so StateFlow always delivers it.
     */
    @Test
    fun copyWithBumpedRev_isNotEqual_soStateFlowEmits() {
        val reg = sampleRegistry()
        reg.connections[0].protocols.removeAll { it.id == "b" }

        val bumped = reg.copy(rev = reg.rev + 1)
        assertNotEquals(reg, bumped)
        assertEquals(reg.rev + 1, bumped.rev)
    }

    /** rev defaults to 0 and is part of equals/hashCode. */
    @Test
    fun rev_defaultsToZero_andDifferentiatesEquality() {
        val a = ConnectionRegistry(activeId = "x")
        val b = ConnectionRegistry(activeId = "x")
        assertEquals(0L, a.rev)
        assertEquals(a, b)
        assertNotEquals(a, b.copy(rev = 1L))
    }

    /**
     * @Transient: rev must NOT be serialised, otherwise it would
     * pollute connections.json and an old file (no rev) must still
     * decode with rev = 0.
     */
    @Test
    fun rev_isNotSerialised_andOldJsonStillLoads() {
        val json = Json { encodeDefaults = true; ignoreUnknownKeys = true }
        val reg = ConnectionRegistry(activeId = "conn-1").copy(rev = 99L)

        val encoded = json.encodeToString(ConnectionRegistry.serializer(), reg)
        assertFalse(
            "rev must be @Transient (absent from JSON), was: $encoded",
            encoded.contains("\"rev\""),
        )

        // A previously-persisted registry without a rev field loads
        // fine and defaults rev to 0.
        val legacy = """{"connections":[],"active_id":"old"}"""
        val decoded = json.decodeFromString(ConnectionRegistry.serializer(), legacy)
        assertEquals("old", decoded.activeId)
        assertEquals(0L, decoded.rev)
        assertTrue(decoded.connections.isEmpty())
    }
}
