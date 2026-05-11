package com.privycs.vpn.data.models

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * AmneziaWG (AWG) obfuscation foundation — Stage 0 of the
 * AMNEZIAWG_CLIENT_PLAN.md rollout.
 *
 * Mirrors the server-side `obfuscation` enrollment block (plan
 * §2.1 / §2.2) and the desktop client's `ObfuscationConfig` struct.
 * Stage 0 only defines the type + detection helper; the
 * `amneziawg-android` submodule + dual-backend selection wires it
 * into the connect path in Stage 1.
 *
 *   jc        0..128    junk packets before handshake
 *   jmin      0..1280   junk packet size range (lower)
 *   jmax      0..1280   junk packet size range (upper, >= jmin)
 *   s1..s4    0..64     per-message padding lengths (s4 is 0..32)
 *   h1..h4    UInt      dynamic magic-header bytes replacing WG's
 *                       fixed 0x01-0x04 markers
 *   i1..i5    hex str   optional mimicry-packet blobs
 *
 * Vanilla WG: enabled=false, all fields zero, struct lives but the
 * connect path stays on the vanilla wireguard-android backend.
 * AWG: enabled=true, all fields populated, connect path branches
 * to the amneziawg-android backend.
 */
@Serializable
data class ObfuscationConfig(
    val enabled: Boolean = false,
    val jc: Int = 0,
    val jmin: Int = 0,
    val jmax: Int = 0,
    val s1: Int = 0,
    val s2: Int = 0,
    val s3: Int = 0,
    val s4: Int = 0,
    // Server emits H1-H4 as 32-bit unsigned ints up to 2^32-1.
    // Kotlin's Long covers the range without unsigned-type drama;
    // the AWG library accepts the value directly.
    @SerialName("h1") val h1: Long = 0L,
    @SerialName("h2") val h2: Long = 0L,
    @SerialName("h3") val h3: Long = 0L,
    @SerialName("h4") val h4: Long = 0L,
    val i1: String? = null,
    val i2: String? = null,
    val i3: String? = null,
    val i4: String? = null,
    val i5: String? = null,
)

/**
 * Tunnel variant marker used by the connect path to pick the
 * right backend (com.wireguard.android.backend.GoBackend vs
 * org.amnezia.awg.backend.GoBackend). Filled by content
 * detection on the .conf payload at connect time.
 */
enum class TunnelVariant {
    WIREGUARD, AMNEZIAWG;

    companion object {
        /**
         * Regex-detect: ANY of the AWG-specific [Interface] keys
         * (`Jc = ...`, `Jmin = ...`, ..., `H4 = ...`) in the conf
         * payload is sufficient evidence to switch to the AWG
         * backend. Vanilla wireguard-android's parser rejects
         * these unknown keys with a parse error, so detection
         * must precede backend selection.
         *
         * Matches plan §2.4 — pre-rendered `obfuscation_config_lines`
         * server output looks like:
         *     Jc = 7
         *     Jmin = 16
         *     ...
         */
        private val AWG_MARKER_RE = Regex(
            """(?m)^[ \t]*(Jc|Jmin|Jmax|S[1-4]|H[1-4]|I[1-5])[ \t]*=""",
            RegexOption.IGNORE_CASE,
        )

        fun detect(confContent: String): TunnelVariant {
            return if (AWG_MARKER_RE.containsMatchIn(confContent)) {
                AMNEZIAWG
            } else {
                WIREGUARD
            }
        }
    }
}
