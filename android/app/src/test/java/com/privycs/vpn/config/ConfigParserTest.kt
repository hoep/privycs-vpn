package com.privycs.vpn.config

import com.privycs.vpn.data.models.VpnProtocol
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Pure unit tests for [ConfigParser] — the import seam that
 * regressed repeatedly (v0.9.15.x AWG-variant detection, the
 * .58 same-name overwrite, QR `scanned.conf`). No Android
 * framework: ConfigParser only touches kotlin/JVM + the pure
 * model classes, so plain JUnit4 (no Robolectric) is enough.
 */
class ConfigParserTest {

    private val plainWg = """
        [Interface]
        PrivateKey = aGVsbG8td29ybGQtZmFrZS1rZXk=
        Address = 10.0.0.2/32
        [Peer]
        PublicKey = cGVlci1mYWtlLWtleS1oZXJlPT0=
        Endpoint = vpn.example.com:51820
        AllowedIPs = 0.0.0.0/0
    """.trimIndent()

    // Same as plainWg but with one AmneziaWG obfuscation key in
    // [Interface]; TunnelVariant.detect must flip this to AMNEZIAWG.
    private val awg = """
        [Interface]
        PrivateKey = aGVsbG8td29ybGQtZmFrZS1rZXk=
        Address = 10.0.0.2/32
        Jc = 7
        Jmin = 16
        Jmax = 1280
        [Peer]
        PublicKey = cGVlci1mYWtlLWtleS1oZXJlPT0=
        Endpoint = vpn.example.com:51820
        AllowedIPs = 0.0.0.0/0
    """.trimIndent()

    private val ovpn = """
        client
        dev tun
        proto udp
        remote vpn.example.com 1194
        <ca>
        -----BEGIN CERTIFICATE-----
        -----END CERTIFICATE-----
        </ca>
    """.trimIndent()

    // ---- detectProtocol: extension-driven ----

    @Test fun detect_ovpn_byExtension() =
        assertEquals(VpnProtocol.OPENVPN, ConfigParser.detectProtocol(ovpn, "client.ovpn"))

    @Test fun detect_ipsec_bySswan() =
        assertEquals(VpnProtocol.IPSEC, ConfigParser.detectProtocol("anything", "p.sswan"))

    @Test fun detect_ipsec_byMobileconfig() =
        assertEquals(VpnProtocol.IPSEC, ConfigParser.detectProtocol("anything", "p.mobileconfig"))

    @Test fun detect_ipsec_byP12() =
        assertEquals(VpnProtocol.IPSEC, ConfigParser.detectProtocol("anything", "cert.p12"))

    @Test fun detect_plainWireguard_conf() =
        assertEquals(VpnProtocol.WIREGUARD, ConfigParser.detectProtocol(plainWg, "wg0.conf"))

    // The AWG-variant regression surface: a .conf carrying Jc/S1/H1…
    // MUST resolve to AMNEZIAWG, not vanilla WIREGUARD.
    @Test fun detect_amneziawg_conf_byObfuscationKeys() =
        assertEquals(VpnProtocol.AMNEZIAWG, ConfigParser.detectProtocol(awg, "obfs.conf"))

    @Test fun detect_caseInsensitiveExtension() =
        assertEquals(VpnProtocol.OPENVPN, ConfigParser.detectProtocol(ovpn, "Client.OVPN"))

    // ---- detectProtocol: content-driven (no usable extension) ----

    @Test fun detect_wireguard_byContent_noExtension() =
        assertEquals(VpnProtocol.WIREGUARD, ConfigParser.detectProtocol(plainWg, "noext"))

    @Test fun detect_amneziawg_byContent_noExtension() =
        assertEquals(VpnProtocol.AMNEZIAWG, ConfigParser.detectProtocol(awg, "noext"))

    @Test fun detect_openvpn_byContent_noExtension() =
        assertEquals(VpnProtocol.OPENVPN, ConfigParser.detectProtocol(ovpn, "noext"))

    @Test fun detect_unknown_returnsNull() =
        assertNull(ConfigParser.detectProtocol("just some notes", "notes.txt"))

    // ---- parse: AWG variant must survive into ParseResult ----

    @Test fun parse_amneziawg_keepsVariantProtocol() {
        val r = ConfigParser.parse(awg, "obfs.conf")
        assertEquals(VpnProtocol.AMNEZIAWG, r?.protocol)
    }

    @Test fun parse_unknown_returnsNull() =
        assertNull(ConfigParser.parse("garbage", "x.txt"))

    // ---- buildProtocolConfig: content + filename pass through verbatim ----

    @Test fun build_wireguard_passesContentAndFilenameVerbatim() {
        val pc = ConfigParser.buildProtocolConfig(plainWg, "home.conf")
        assertEquals(VpnProtocol.WIREGUARD, pc?.protocol)
        assertEquals(plainWg, pc?.configContent)
        assertEquals("home.conf", pc?.filename)
    }

    @Test fun build_amneziawg_protocolIsVariant() {
        val pc = ConfigParser.buildProtocolConfig(awg, "obfs.conf")
        assertEquals(VpnProtocol.AMNEZIAWG, pc?.protocol)
    }

    @Test fun build_unknown_returnsNull() =
        assertNull(ConfigParser.buildProtocolConfig("garbage", "x.txt"))

    // ---- deriveConnectionName: defensive against junk DISPLAY_NAME ----

    @Test fun derive_stripsExtension() =
        assertEquals("myvpn", ConfigParser.deriveConnectionName("myvpn.conf"))

    @Test fun derive_underscoresAndDashesToSpaces() =
        assertEquals("home server 1", ConfigParser.deriveConnectionName("home_server-1.conf"))

    @Test fun derive_blank_fallsBack() =
        assertEquals("VPN Connection", ConfigParser.deriveConnectionName("   "))

    @Test fun derive_empty_fallsBack() =
        assertEquals("VPN Connection", ConfigParser.deriveConnectionName(""))

    @Test fun derive_jsonBlob_fallsBack() =
        assertEquals("VPN Connection", ConfigParser.deriveConnectionName("{\"a\":1}"))

    @Test fun derive_pemHeader_fallsBack() =
        assertEquals("VPN Connection", ConfigParser.deriveConnectionName("-----BEGIN KEY"))

    @Test fun derive_multilineBlob_fallsBack() =
        assertEquals("VPN Connection", ConfigParser.deriveConnectionName("line1\nline2"))

    @Test fun derive_singleNonAlnum_fallsBack() =
        assertEquals("VPN Connection", ConfigParser.deriveConnectionName("{"))

    @Test fun derive_clampsTo64() {
        val long = "a".repeat(120) + ".conf"
        assertEquals(64, ConfigParser.deriveConnectionName(long).length)
    }

    @Test fun derive_normalNameUnchanged() =
        assertTrue(ConfigParser.deriveConnectionName("Privycs Shielded.conf") == "Privycs Shielded")

    // ---- parse: server/local address extraction (Configs-page column) ----

    @Test fun parse_wireguard_extractsEndpointAndAddress() {
        val r = ConfigParser.parse(plainWg, "home.conf")
        assertEquals("vpn.example.com:51820", r?.serverAddress)
        assertEquals("10.0.0.2/32", r?.localAddress)
    }

    @Test fun parse_amneziawg_stillExtractsEndpoint() {
        val r = ConfigParser.parse(awg, "obfs.conf")
        assertEquals("vpn.example.com:51820", r?.serverAddress)
    }

    @Test fun parse_openvpn_extractsRemoteHostPort() {
        val r = ConfigParser.parse(ovpn, "client.ovpn")
        assertEquals("vpn.example.com:1194", r?.serverAddress)
    }

    @Test fun parse_openvpn_remoteWithoutPort() {
        val r = ConfigParser.parse("client\nremote vpn.example.com\n", "c.ovpn")
        assertEquals("vpn.example.com", r?.serverAddress)
    }

    // .sswan IPSec: proper JSON addr extraction. Regression guard for
    // the documented "server endpoint shows '{' after IPSec
    // disconnect" bug — the old line-based matcher reduced the
    // `"remote": {` object-opening line to a literal "{" and
    // persisted that as serverAddress.
    private val sswan = """
        {
          "uuid": "00000000-0000-0000-0000-000000000000",
          "name": "Privycs IPSec",
          "remote": {
            "addr": "ipsec.example.com",
            "id": "ipsec.example.com"
          },
          "local": { "p12": "" }
        }
    """.trimIndent()

    @Test fun parse_ipsec_extractsRemoteAddrFromJson() {
        val r = ConfigParser.parse(sswan, "profile.sswan")
        assertEquals(VpnProtocol.IPSEC, r?.protocol)
        assertEquals("ipsec.example.com", r?.serverAddress)
    }

    @Test fun parse_ipsec_neverReturnsBraceAsServerAddress() {
        val r = ConfigParser.parse(sswan, "profile.sswan")
        assertNotEquals("{", r?.serverAddress)
    }

    @Test fun parse_ipsec_invalidJson_emptyServer_notCrash() {
        val r = ConfigParser.parse("not even json", "broken.sswan")
        assertEquals(VpnProtocol.IPSEC, r?.protocol)
        assertEquals("", r?.serverAddress)
    }
}
