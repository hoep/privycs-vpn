import Foundation

/// Minimal, dependency-free MaxMind-DB (MMDB) reader that resolves an IP
/// address to an ISO country code. Reads the **same** `country.mmdb`
/// Android bundles (db-ip "IP to Country Lite", CC BY 4.0), so iOS gets
/// identical pool/server country flags. Only the binary-search tree walk
/// + the `country.iso_code` field are implemented — that's all we need.
///
/// Algorithm validated against the real DB before porting (8.8.8.8→US,
/// 1.1.1.1→AU, 217.247.0.1→DE, 2a02:8071::1→DE, 206.189.0.1→NL, …).
///
/// Attribution (required by CC BY 4.0): *IP to Country Lite by db-ip.com
/// (CC BY 4.0)* — surfaced in the in-app Open-Source Licenses screen.
public final class MmdbCountryResolver: @unchecked Sendable {

    /// Shared instance, loaded once from the bundled DB (nil if unavailable).
    public static let shared: MmdbCountryResolver? = MmdbCountryResolver()

    private let bytes: [UInt8]
    private let nodeCount: Int
    private let recordSize: Int     // bits per record (24/28/32)
    private let nodeBytes: Int      // bytes per node = recordSize*2/8
    private let searchTreeSize: Int
    private let dataStart: Int      // searchTreeSize + 16 (data-section separator)

    public init?(data: Data) {
        let b = [UInt8](data)
        guard let metaStart = MmdbCountryResolver.findMetadata(b),
              case let .map(meta) = MmdbCountryResolver.decode(b, metaStart, pointerBase: metaStart).0,
              case let .uint(nc)? = meta["node_count"],
              case let .uint(rs)? = meta["record_size"], rs % 8 == 0 || rs == 28 || rs == 24
        else { return nil }
        self.bytes = b
        self.nodeCount = nc
        self.recordSize = rs
        self.nodeBytes = rs * 2 / 8
        self.searchTreeSize = nc * (rs * 2 / 8)
        self.dataStart = nc * (rs * 2 / 8) + 16
        guard dataStart <= b.count else { return nil }
    }

    public convenience init?() {
        guard let url = Bundle.module.url(forResource: "country", withExtension: "mmdb"),
              let data = try? Data(contentsOf: url) else { return nil }
        self.init(data: data)
    }

    /// ISO-3166 country code (uppercase, e.g. "DE") for an IP literal, or nil.
    public func country(forIP ip: String) -> String? {
        guard let bits = MmdbCountryResolver.ipBits(ip) else { return nil }
        var node = 0
        for bit in bits {
            if node >= nodeCount { break }
            node = record(node, bit)
        }
        guard node > nodeCount else { return nil }   // == nodeCount → no data
        let off = node - nodeCount - 16 + dataStart
        guard off >= 0, off < bytes.count else { return nil }
        let (value, _) = MmdbCountryResolver.decode(bytes, off, pointerBase: dataStart)
        if case let .map(m) = value,
           case let .map(c)? = m["country"],
           case let .string(iso)? = c["iso_code"] {
            return iso.uppercased()
        }
        return nil
    }

    // MARK: - Tree

    private func record(_ node: Int, _ right: Bool) -> Int {
        let off = node * nodeBytes
        switch recordSize {
        case 28:
            if right {
                return ((Int(bytes[off + 3]) & 0x0F) << 24) | (Int(bytes[off + 4]) << 16)
                    | (Int(bytes[off + 5]) << 8) | Int(bytes[off + 6])
            } else {
                return ((Int(bytes[off + 3]) >> 4) << 24) | (Int(bytes[off]) << 16)
                    | (Int(bytes[off + 1]) << 8) | Int(bytes[off + 2])
            }
        case 32:
            let b = off + (right ? 4 : 0)
            return (Int(bytes[b]) << 24) | (Int(bytes[b + 1]) << 16) | (Int(bytes[b + 2]) << 8) | Int(bytes[b + 3])
        default: // 24
            let b = off + (right ? 3 : 0)
            return (Int(bytes[b]) << 16) | (Int(bytes[b + 1]) << 8) | Int(bytes[b + 2])
        }
    }

    // MARK: - Data-section decoder

    private indirect enum MValue {
        case string(String)
        case map([String: MValue])
        case uint(Int)
        case bool(Bool)
        case array
        case other
    }

    /// Decode the value at absolute offset `p0`; pointers are relative to
    /// `pointerBase`. Returns (value, offset-after-this-value).
    private static func decode(_ bytes: [UInt8], _ p0: Int, pointerBase: Int) -> (MValue, Int) {
        var p = p0
        let ctrl = Int(bytes[p]); p += 1
        var typ = ctrl >> 5
        var size = ctrl & 0x1f
        if typ == 1 {   // pointer
            let ss = (ctrl >> 3) & 0x3
            let val = ctrl & 0x7
            var ptr: Int
            switch ss {
            case 0: ptr = (val << 8) | Int(bytes[p]); p += 1
            case 1: ptr = (val << 16) | (Int(bytes[p]) << 8) | Int(bytes[p + 1]); p += 2; ptr += 2048
            case 2: ptr = (val << 24) | (Int(bytes[p]) << 16) | (Int(bytes[p + 1]) << 8) | Int(bytes[p + 2]); p += 3; ptr += 526336
            default: ptr = (Int(bytes[p]) << 24) | (Int(bytes[p + 1]) << 16) | (Int(bytes[p + 2]) << 8) | Int(bytes[p + 3]); p += 4
            }
            let (v, _) = decode(bytes, pointerBase + ptr, pointerBase: pointerBase)
            return (v, p)
        }
        if typ == 0 { typ = 7 + Int(bytes[p]); p += 1 }   // extended type
        if size >= 29 {
            if size == 29 { size = 29 + Int(bytes[p]); p += 1 }
            else if size == 30 { size = 285 + (Int(bytes[p]) << 8) + Int(bytes[p + 1]); p += 2 }
            else { size = 65821 + (Int(bytes[p]) << 16) + (Int(bytes[p + 1]) << 8) + Int(bytes[p + 2]); p += 3 }
        }
        switch typ {
        case 14:   // boolean — value is in `size`, ZERO payload bytes
            return (.bool(size == 1), p)
        case 2:    // utf8 string
            return (.string(String(decoding: bytes[p..<p + size], as: UTF8.self)), p + size)
        case 7:    // map
            var m = [String: MValue](); var pp = p
            for _ in 0..<size {
                let (k, p1) = decode(bytes, pp, pointerBase: pointerBase)
                let (v, p2) = decode(bytes, p1, pointerBase: pointerBase)
                if case let .string(ks) = k { m[ks] = v }
                pp = p2
            }
            return (.map(m), pp)
        case 11:   // array
            var pp = p
            for _ in 0..<size { pp = decode(bytes, pp, pointerBase: pointerBase).1 }
            return (.array, pp)
        case 5, 6, 8, 9, 10:   // uint16/32, int32, uint64, uint128
            var v = 0
            for k in 0..<size { v = (v &<< 8) | Int(bytes[p + k]) }   // &<< = trap-safe for 128-bit
            return (.uint(v), p + size)
        default:   // double(8), bytes, float(4) — skipped by size
            return (.other, p + size)
        }
    }

    // MARK: - Helpers

    private static let marker: [UInt8] = [0xAB, 0xCD, 0xEF] + Array("MaxMind.com".utf8)

    /// Offset just after the last metadata marker (metadata is near the end).
    private static func findMetadata(_ bytes: [UInt8]) -> Int? {
        let m = marker
        guard bytes.count >= m.count else { return nil }
        // Search only the tail (the metadata section is small + at the end).
        let lower = max(0, bytes.count - 256 * 1024)
        var i = bytes.count - m.count
        while i >= lower {
            var match = true
            for j in 0..<m.count where bytes[i + j] != m[j] { match = false; break }
            if match { return i + m.count }
            i -= 1
        }
        return nil
    }

    /// 128-bit big-endian bit array for an IPv4 (mapped to ::v4) or IPv6 string.
    private static func ipBits(_ ip: String) -> [Bool]? {
        var raw = [UInt8]()
        if ip.contains(":") {
            var addr = in6_addr()
            guard inet_pton(AF_INET6, ip, &addr) == 1 else { return nil }
            raw = withUnsafeBytes(of: &addr) { Array($0) }            // 16 bytes
        } else {
            var addr = in_addr()
            guard inet_pton(AF_INET, ip, &addr) == 1 else { return nil }
            raw = [UInt8](repeating: 0, count: 12) + withUnsafeBytes(of: &addr) { Array($0) }  // ::v4
        }
        guard raw.count == 16 else { return nil }
        var bits = [Bool](); bits.reserveCapacity(128)
        for byte in raw { for k in stride(from: 7, through: 0, by: -1) { bits.append((byte >> k) & 1 == 1) } }
        return bits
    }
}
