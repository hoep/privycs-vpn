package com.privycs.vpn.engine

import com.privycs.vpn.data.models.VpnProtocol
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * Verifies the pure-Kotlin Smart Decision Engine port matches the Go original
 * (engine/select.go + reason.go) — the deterministic ranking + reasoning that
 * replaced the gomobile binding on Android.
 */
class LocalEngineTest {

    private val all = listOf(
        VpnProtocol.WIREGUARD, VpnProtocol.AMNEZIAWG, VpnProtocol.OPENVPN, VpnProtocol.IPSEC,
    )

    @Test
    fun openCountryWifi_speedFirst() {
        val order = LocalEngine.selectOrder(all, country = "AT", iface = "wifi", nowSec = 1000)
        assertEquals(
            listOf(VpnProtocol.WIREGUARD, VpnProtocol.AMNEZIAWG, VpnProtocol.OPENVPN, VpnProtocol.IPSEC),
            order,
        )
    }

    @Test
    fun restrictiveCountry_evasionFirst() {
        val order = LocalEngine.selectOrder(all, country = "CN", iface = "wifi", nowSec = 1000)
        assertEquals(
            listOf(VpnProtocol.AMNEZIAWG, VpnProtocol.OPENVPN, VpnProtocol.WIREGUARD, VpnProtocol.IPSEC),
            order,
        )
    }

    @Test
    fun cellularOpen_bumpsIPSecForMobike() {
        val order = LocalEngine.selectOrder(all, country = "AT", iface = "cellular", nowSec = 1000)
        assertEquals(
            listOf(VpnProtocol.WIREGUARD, VpnProtocol.IPSEC, VpnProtocol.AMNEZIAWG, VpnProtocol.OPENVPN),
            order,
        )
    }

    @Test
    fun recentFailureDemotesProtocol() {
        // WireGuard failed 50s ago (within the 600s cooldown) → heavy penalty.
        val stats = mapOf(VpnProtocol.WIREGUARD to LocalEngine.Stat(successEwma = 500, lastFailSec = 950))
        val order = LocalEngine.selectOrder(all, country = "AT", iface = "wifi", nowSec = 1000, stats = stats)
        assertEquals(VpnProtocol.WIREGUARD, order.last())
        assertEquals(VpnProtocol.AMNEZIAWG, order.first())
    }

    @Test
    fun onlyAvailableProtocolsRanked() {
        val order = LocalEngine.selectOrder(
            listOf(VpnProtocol.OPENVPN, VpnProtocol.IPSEC), country = "AT", iface = "wifi", nowSec = 1000,
        )
        assertEquals(listOf(VpnProtocol.OPENVPN, VpnProtocol.IPSEC), order)
    }

    @Test
    fun countryReason_open() {
        assertEquals("reason.country_open" to listOf("AT"),
            LocalEngine.countryReason("at", VpnProtocol.WIREGUARD, awgAvailable = false))
    }

    @Test
    fun countryReason_restrictiveOnAwg() {
        assertEquals("reason.country_restrictive_awg" to listOf("CN"),
            LocalEngine.countryReason("CN", VpnProtocol.AMNEZIAWG, awgAvailable = true))
    }

    @Test
    fun countryReason_restrictiveRecommendAwg() {
        assertEquals("reason.country_restrictive_use_awg" to listOf("IR"),
            LocalEngine.countryReason("IR", VpnProtocol.WIREGUARD, awgAvailable = true))
    }

    @Test
    fun countryReason_restrictiveNoAwgProfile() {
        assertEquals("reason.country_restrictive_no_awg" to listOf("RU"),
            LocalEngine.countryReason("RU", VpnProtocol.OPENVPN, awgAvailable = false))
    }

    @Test
    fun countryReason_unknownCountry_empty() {
        assertEquals("" to emptyList<String>(),
            LocalEngine.countryReason("", VpnProtocol.WIREGUARD, awgAvailable = false))
    }
}
