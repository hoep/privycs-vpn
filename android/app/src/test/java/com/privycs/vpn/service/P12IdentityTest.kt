package com.privycs.vpn.service

import org.bouncycastle.jce.provider.BouncyCastleProvider
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.BeforeClass
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.File
import java.security.KeyStore
import java.security.Signature
import java.security.spec.MGF1ParameterSpec
import java.security.spec.PSSParameterSpec
import java.util.Base64

/**
 * Dry-runs the exact JCA calls charon makes against a key parsed from a gateway-shaped
 * PKCS#12, on the JVM. The signing assertions are the point: they prove a BouncyCastle-parsed
 * key survives handover to a Signature obtained with no provider argument, which is what
 * android_private_key.c:264-291 does and the only thing charon ever does with the key.
 *
 * Fixtures are minted by openssl at test time rather than committed, so no key material and
 * no long-lived secret enters the repo.
 */
class P12IdentityTest {

    companion object {
        private const val PASSWORD = "correct-horse-battery-staple"

        private lateinit var dir: File

        /**
         * The two encodings the gateway actually emits (ipsec_pki_crypto.go:444-452). Both must
         * decode or a fraction of downloads breaks depending on which the server chose.
         *
         * gopkcs12.LegacyDES is 3DES-CBC on BOTH the key and cert bags with a SHA-1 MAC, which
         * is OpenSSL's -descert. Naming the algorithms explicitly (instead of `-legacy`, whose
         * default is RC2-40 for certs — that is LegacyRC2) both matches the gateway and keeps
         * the test off OpenSSL 3's legacy provider module, which need not be installed.
         */
        private val LEGACY_DES = arrayOf(
            "-keypbe", "PBE-SHA1-3DES", "-certpbe", "PBE-SHA1-3DES", "-macalg", "sha1",
        )

        /** gopkcs12.Modern == Modern2023: PBES2 / AES-256-CBC + SHA-256 MAC. */
        private val MODERN = arrayOf(
            "-keypbe", "AES-256-CBC", "-certpbe", "AES-256-CBC", "-macalg", "sha256",
        )

        @BeforeClass
        @JvmStatic
        fun mintFixtures() {
            dir = File(System.getProperty("java.io.tmpdir"), "p12identity-${System.nanoTime()}")
            check(dir.mkdirs())
            dir.deleteOnExit()

            openssl(
                "req", "-x509", "-newkey", "rsa:2048", "-keyout", "ca.key", "-out", "ca.crt",
                "-days", "1", "-nodes", "-subj", "/CN=P12IdentityTest CA",
            )
            leaf("rsa", "test-client-rsa", "-newkey", "rsa:4096")
            leaf("ec", "test-client-ec", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-384")

            for ((name, algo) in listOf("des" to LEGACY_DES, "modern" to MODERN)) {
                for (kind in listOf("rsa", "ec")) {
                    openssl(
                        "pkcs12", "-export", "-inkey", "$kind.key", "-in", "$kind.crt",
                        // -certfile puts the CA in the p12 exactly as the gateway does, so
                        // "the CA chain is discarded" is a claim with something to discard.
                        "-certfile", "ca.crt",
                        *algo, "-out", "$kind-$name.p12", "-passout", "pass:$PASSWORD",
                    )
                }
            }
        }

        private fun leaf(kind: String, cn: String, vararg keyArgs: String) {
            openssl("req", "-new", *keyArgs, "-keyout", "$kind.key", "-out", "$kind.csr", "-nodes", "-subj", "/CN=$cn")
            openssl(
                "x509", "-req", "-in", "$kind.csr", "-CA", "ca.crt", "-CAkey", "ca.key",
                "-CAcreateserial", "-out", "$kind.crt", "-days", "1",
            )
        }

        private fun openssl(vararg args: String) {
            val p = ProcessBuilder(listOf("openssl") + args)
                .directory(dir)
                .redirectErrorStream(true)
                .start()
            val out = p.inputStream.bufferedReader().readText()
            check(p.waitFor() == 0) { "openssl ${args.joinToString(" ")} failed:\n$out" }
        }

        private fun b64(fixture: String): String =
            Base64.getEncoder().encodeToString(File(dir, fixture).readBytes())
    }

    @Test
    fun `decodes both gateway encodings for both key types`() {
        for (fixture in listOf("rsa-des.p12", "rsa-modern.p12", "ec-des.p12", "ec-modern.p12")) {
            val id = P12Identity.parse(b64(fixture), PASSWORD)
            assertTrue(fixture, id.leaf.subjectX500Principal.name.contains("test-client-"))
        }
    }

    @Test
    fun `keeps the leaf and discards the CA chain`() {
        val fixture = "rsa-modern.p12"

        // The fixture really does carry the CA — otherwise this proves nothing.
        val store = KeyStore.getInstance("PKCS12", BouncyCastleProvider())
        ByteArrayInputStream(File(dir, fixture).readBytes()).use { store.load(it, PASSWORD.toCharArray()) }
        val alias = store.aliases().toList().first { store.isKeyEntry(it) }
        assertEquals("fixture should hold leaf + CA", 2, store.getCertificateChain(alias).size)

        val id = P12Identity.parse(b64(fixture), PASSWORD)
        assertTrue(id.leaf.subjectX500Principal.name.contains("test-client-rsa"))
        assertNotEquals("CA must not be handed over as the identity", id.leaf, store.getCertificateChain(alias)[1])
        assertArrayEquals(id.leaf.encoded, id.leafDer)
    }

    /**
     * The decisive assertion. Signature.getInstance(alg) takes no provider — as in charon —
     * so the JCA picks the platform default while the key came from BC.
     */
    @Test
    fun `parsed key signs verifiably under the algorithms charon requests`() {
        val cases = listOf(
            Triple("rsa-des.p12", "SHA256withRSA", "RSA/LegacyDES"),
            Triple("rsa-modern.p12", "SHA256withRSA", "RSA/Modern"),
            Triple("ec-des.p12", "SHA384withECDSA", "EC/LegacyDES"),
            Triple("ec-modern.p12", "SHA384withECDSA", "EC/Modern"),
        )
        val data = "IKE_AUTH payload".toByteArray()

        for ((fixture, alg, label) in cases) {
            val id = P12Identity.parse(b64(fixture), PASSWORD)

            val signer = Signature.getInstance(alg)
            signer.initSign(id.privateKey)
            signer.update(data)
            val sig = signer.sign()

            // charon verifies nothing itself; the peer does, against the leaf we ship.
            val verifier = Signature.getInstance(alg)
            verifier.initVerify(id.leaf.publicKey)
            verifier.update(data)
            assertTrue("$label: signature must verify against the leaf", verifier.verify(sig))
        }
    }

    /**
     * strongSwan 6 negotiates RSASSA-PSS by default, so PKCS#1 alone would be an optimistic
     * test — charon reaches SIGN_RSA_EMSA_PSS at android_private_key.c:199-225.
     *
     * The algorithm NAME differs by platform and cannot be shared: charon asks for
     * "SHA256withRSA/PSS", which only Conscrypt registers; this JVM exposes PSS solely as
     * SunRsaSign's "RSASSA-PSS". The property under test is the key's usability under PSS
     * with a provider-less getInstance, which the JDK spelling exercises just as well.
     */
    @Test
    fun `parsed RSA key signs under PSS`() {
        val id = P12Identity.parse(b64("rsa-modern.p12"), PASSWORD)
        val data = "IKE_AUTH payload".toByteArray()
        val params = PSSParameterSpec("SHA-256", "MGF1", MGF1ParameterSpec.SHA256, 32, 1)

        val signer = Signature.getInstance("RSASSA-PSS")
        signer.setParameter(params)
        signer.initSign(id.privateKey)
        signer.update(data)
        val sig = signer.sign()

        val verifier = Signature.getInstance("RSASSA-PSS")
        verifier.setParameter(params)
        verifier.initVerify(id.leaf.publicKey)
        verifier.update(data)
        assertTrue(verifier.verify(sig))
    }

    /**
     * Pins the round-trip in [P12Identity.neutralize]. The signing tests above do NOT cover it:
     * this JVM's SunRsaSign happily consumes a BouncyCastle key via its encoding, so they pass
     * either way (verified by mutation). Conscrypt makes no such promise for a foreign key, and
     * charon's Signature.getInstance names no provider — so the key must leave here unbound to
     * BC, and only this assertion says so.
     */
    @Test
    fun `handed-over key is provider-neutral`() {
        for (fixture in listOf("rsa-modern.p12", "ec-modern.p12")) {
            val id = P12Identity.parse(b64(fixture), PASSWORD)
            assertFalse(
                "$fixture: key must be re-imported off BouncyCastle, was ${id.privateKey.javaClass.name}",
                id.privateKey.javaClass.name.startsWith("org.bouncycastle"),
            )
        }
    }

    @Test
    fun `wrong password yields a typed error`() {
        for (fixture in listOf("rsa-des.p12", "rsa-modern.p12")) {
            try {
                P12Identity.parse(b64(fixture), "not-the-password")
                fail("$fixture: expected P12Exception")
            } catch (e: P12Identity.P12Exception) {
                assertEquals(fixture, P12Identity.Reason.WRONG_PASSWORD, e.reason)
                // An error surfaced to the user or the log must not leak the secret.
                assertTrue(e.message!!.isNotBlank())
                assertTrue(!e.message!!.contains(PASSWORD))
            }
        }
    }

    @Test
    fun `tolerates whitespace in the base64`() {
        val pretty = b64("rsa-modern.p12").chunked(64).joinToString("\n    ", prefix = "\n    ", postfix = "\n  ")
        val id = P12Identity.parse(pretty, PASSWORD)
        assertTrue(id.leaf.subjectX500Principal.name.contains("test-client-rsa"))
    }

    @Test
    fun `garbage base64 yields a typed error`() {
        try {
            P12Identity.parse("!!!not base64!!!", PASSWORD)
            fail("expected P12Exception")
        } catch (e: P12Identity.P12Exception) {
            assertEquals(P12Identity.Reason.INVALID_BASE64, e.reason)
        }
    }

    @Test
    fun `valid base64 that is not a PKCS12 yields a typed error`() {
        try {
            P12Identity.parse(Base64.getEncoder().encodeToString(ByteArray(64) { 7 }), PASSWORD)
            fail("expected P12Exception")
        } catch (e: P12Identity.P12Exception) {
            assertEquals(P12Identity.Reason.MALFORMED, e.reason)
        }
    }
}
