package com.privycs.vpn.data

import java.math.BigInteger
import java.net.Inet4Address
import java.net.Inet6Address
import java.net.InetAddress

/**
 * Pure CIDR set arithmetic for the split-tunnel feature.
 *
 * The core operation is [subtractFromUniverse]: given a list of
 * "bypass" CIDRs that should NOT go through the tunnel, compute
 * the complement against the IPv4 universe (0.0.0.0/0) and the
 * IPv6 universe (::/0) and return them as a flat list of CIDRs
 * that the WireGuard `AllowedIPs` line can use.
 *
 * Reference: this is the same algorithm wg-quick uses for its
 * "exclude" syntax. A typical 5-CIDR exclude expands to 30-50
 * output CIDRs covering every range NOT in the exclude set.
 *
 * IPv6 subtraction uses BigInteger because Long is 64 bits and
 * IPv6 addresses are 128. Allocation cost is acceptable for the
 * one-shot patch we do at connect time.
 */
object CidrMath {

    /**
     * Parsed CIDR: address + prefix length. `addr` is held as a
     * BigInteger so the same code path handles v4 (32 bits) and v6
     * (128 bits). [bits] is 32 or 128 - identifies the family.
     */
    data class Cidr(val addr: BigInteger, val prefix: Int, val bits: Int) {
        /** Is this an IPv4 CIDR (vs IPv6). */
        val isV4: Boolean get() = bits == 32

        /** First address in this range (inclusive). Equal to [addr]. */
        fun start(): BigInteger = addr

        /** Last address in this range (inclusive). */
        fun end(): BigInteger {
            val hostBits = bits - prefix
            if (hostBits == 0) return addr
            val mask = BigInteger.ONE.shiftLeft(hostBits).subtract(BigInteger.ONE)
            return addr.or(mask)
        }

        /** Canonical CIDR string. */
        fun toCidrString(): String {
            val ip = bigIntToInetAddress(addr, bits)
            return "${ip.hostAddress}/$prefix"
        }
    }

    /**
     * Parse "10.0.0.0/8" or "fe80::/10" into a [Cidr]. Returns null
     * for malformed input. Plain IPs (no slash) are accepted as
     * /32 (v4) or /128 (v6) for convenience - the user types
     * "1.2.3.4" and we treat it as a single host.
     */
    fun parse(s: String): Cidr? {
        val trimmed = s.trim()
        if (trimmed.isEmpty()) return null
        val (addrStr, prefStr) = if ('/' in trimmed) {
            val parts = trimmed.split('/', limit = 2)
            parts[0] to parts[1]
        } else {
            trimmed to null
        }
        val ip = try {
            InetAddress.getByName(addrStr)
        } catch (e: Exception) {
            return null
        }
        val bits = when (ip) {
            is Inet4Address -> 32
            is Inet6Address -> 128
            else -> return null
        }
        val prefix = if (prefStr == null) bits
        else prefStr.toIntOrNull() ?: return null
        if (prefix < 0 || prefix > bits) return null
        // Mask off host bits so 10.0.0.5/8 normalises to 10.0.0.0/8.
        val raw = BigInteger(1, ip.address)
        val hostBits = bits - prefix
        val masked = if (hostBits == 0) raw
        else raw.shiftRight(hostBits).shiftLeft(hostBits)
        return Cidr(masked, prefix, bits)
    }

    /**
     * Returns the complement of [bypass] within 0.0.0.0/0 and ::/0
     * combined. The two families are processed independently so
     * mixing v4 and v6 in [bypass] works without family-mixing
     * artifacts.
     *
     * Example: subtract([10.0.0.0/8]) returns roughly
     *   [0.0.0.0/5, 8.0.0.0/7, 11.0.0.0/8, 12.0.0.0/6, 16.0.0.0/4,
     *    32.0.0.0/3, 64.0.0.0/2, 128.0.0.0/1, ::/0]
     * (8 IPv4 ranges + the full IPv6 universe since no v6 bypass).
     *
     * Always emits at least one v4 and one v6 range in the result -
     * if [bypass] is empty, returns [0.0.0.0/0, ::/0]. If a family
     * has no bypass entries, its universe is emitted unchanged.
     */
    fun subtractFromUniverse(bypass: List<Cidr>): List<Cidr> {
        val v4Bypass = bypass.filter { it.isV4 }
        val v6Bypass = bypass.filterNot { it.isV4 }
        val result = mutableListOf<Cidr>()
        result.addAll(subtractWithinFamily(BigInteger.ZERO, BigInteger.ONE.shiftLeft(32).subtract(BigInteger.ONE), v4Bypass, 32))
        result.addAll(subtractWithinFamily(BigInteger.ZERO, BigInteger.ONE.shiftLeft(128).subtract(BigInteger.ONE), v6Bypass, 128))
        return result
    }

    /**
     * Range-based subtraction within one address family. Treat
     * [start, end] as a sorted list of "kept" intervals; for each
     * bypass CIDR, split intervals around the bypass range. Then
     * convert the surviving intervals to CIDR notation.
     *
     * O(n*m) where n = bypass count, m = surviving interval count.
     * For the typical case (n < 20) this is microseconds.
     */
    private fun subtractWithinFamily(
        start: BigInteger,
        end: BigInteger,
        bypass: List<Cidr>,
        bits: Int
    ): List<Cidr> {
        var kept: MutableList<Pair<BigInteger, BigInteger>> = mutableListOf(start to end)
        for (b in bypass) {
            val bs = b.start()
            val be = b.end()
            val next = mutableListOf<Pair<BigInteger, BigInteger>>()
            for ((s, e) in kept) {
                // No overlap.
                if (be < s || bs > e) {
                    next.add(s to e)
                    continue
                }
                // Bypass fully covers this kept range.
                if (bs <= s && be >= e) continue
                // Bypass clips left edge.
                if (bs <= s && be < e) {
                    next.add(be.add(BigInteger.ONE) to e)
                    continue
                }
                // Bypass clips right edge.
                if (bs > s && be >= e) {
                    next.add(s to bs.subtract(BigInteger.ONE))
                    continue
                }
                // Bypass splits the middle.
                next.add(s to bs.subtract(BigInteger.ONE))
                next.add(be.add(BigInteger.ONE) to e)
            }
            kept = next
        }
        // Convert each kept range to a minimal CIDR cover.
        val out = mutableListOf<Cidr>()
        for ((s, e) in kept) {
            rangeToCidrs(s, e, bits, out)
        }
        return out
    }

    /**
     * Decompose a contiguous range [start, end] into the smallest
     * possible set of aligned CIDRs. Standard greedy algorithm:
     * find the largest prefix that starts at `start` and doesn't
     * overshoot `end`, emit it, advance.
     */
    private fun rangeToCidrs(
        start: BigInteger,
        end: BigInteger,
        bits: Int,
        out: MutableList<Cidr>
    ) {
        var cur = start
        while (cur <= end) {
            // Largest power-of-2 alignment cur supports without
            // exceeding end.
            val maxByAlign = if (cur == BigInteger.ZERO) bits
            else cur.lowestSetBit
            val rangeLen = end.subtract(cur).add(BigInteger.ONE)
            val maxByLen = rangeLen.bitLength() - 1
            val sizeBits = minOf(maxByAlign, maxByLen)
            val prefix = bits - sizeBits
            out.add(Cidr(cur, prefix, bits))
            cur = cur.add(BigInteger.ONE.shiftLeft(sizeBits))
        }
    }

    /**
     * Convert a BigInteger address back to InetAddress for human-
     * readable rendering. Caller knows the family via [bits].
     */
    private fun bigIntToInetAddress(addr: BigInteger, bits: Int): InetAddress {
        val byteCount = bits / 8
        val raw = addr.toByteArray()
        val padded = ByteArray(byteCount)
        // BigInteger.toByteArray may have a leading zero (sign bit)
        // or may be shorter than byteCount; align right.
        val srcOffset = if (raw.size > byteCount) raw.size - byteCount else 0
        val dstOffset = if (raw.size < byteCount) byteCount - raw.size else 0
        val copyLen = minOf(byteCount, raw.size - srcOffset)
        System.arraycopy(raw, srcOffset, padded, dstOffset, copyLen)
        return InetAddress.getByAddress(padded)
    }

    /**
     * Convenience: standard "private network" CIDRs the user can
     * exclude with one toggle. RFC1918 (v4) + IPv6 ULA + link-local.
     * Multicast is intentionally NOT included since most hosts don't
     * route multicast through the VPN regardless.
     */
    val PRIVATE_NETWORKS: List<Cidr> = listOf(
        // RFC1918 IPv4
        parse("10.0.0.0/8")!!,
        parse("172.16.0.0/12")!!,
        parse("192.168.0.0/16")!!,
        // IPv4 link-local (RFC 3927)
        parse("169.254.0.0/16")!!,
        // IPv6 unique local addresses (RFC 4193)
        parse("fc00::/7")!!,
        // IPv6 link-local (RFC 4291)
        parse("fe80::/10")!!,
    )
}
