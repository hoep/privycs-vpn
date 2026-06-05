import Foundation
#if canImport(Compression)
import Compression
#endif

/// Minimal, dependency-free ZIP reader for pool-config archives.
///
/// Reads the central directory (robust against streaming / data-descriptor
/// quirks) and supports the only two methods a provider config bundle ever
/// uses: stored (0) and deflate (8). Replaces reliance on ZIPFoundation, whose
/// in-memory `Archive(data:)` silently returned zero entries for some provider
/// `.zip`s ("keine gültige config"). This mirrors the robustness of Android's
/// `java.util.zip.ZipInputStream` and desktop's Go `archive/zip`.
///
/// Standard (non-ZIP64) archives only — config bundles are far under the 4 GB /
/// 65535-entry limits where ZIP64 kicks in.
enum MiniZip {
    struct Entry { let name: String; let data: Data }

    /// True if the bytes begin with a ZIP local-file or empty-archive signature.
    static func looksLikeZip(_ data: Data) -> Bool {
        let b = [UInt8](data.prefix(4))
        guard b.count == 4, b[0] == 0x50, b[1] == 0x4B else { return false }
        // PK\x03\x04 (local file) or PK\x05\x06 (empty archive EOCD).
        return (b[2] == 0x03 && b[3] == 0x04) || (b[2] == 0x05 && b[3] == 0x06)
    }

    static func entries(_ zipData: Data) -> [Entry] {
        let b = [UInt8](zipData)
        let n = b.count
        guard n >= 22 else { return [] }

        // Locate the End Of Central Directory record (sig 0x06054b50), scanning
        // back from the end past an optional archive comment (<= 65535 bytes).
        var eocd = -1
        let minStart = max(0, n - 22 - 65535)
        var i = n - 22
        while i >= minStart {
            if b[i] == 0x50, b[i + 1] == 0x4B, b[i + 2] == 0x05, b[i + 3] == 0x06 {
                eocd = i; break
            }
            i -= 1
        }
        guard eocd >= 0 else { return [] }

        let total = Int(u16(b, eocd + 10))
        let cdOffset = Int(u32(b, eocd + 16))
        guard cdOffset >= 0, cdOffset + 4 <= n else { return [] }

        var out: [Entry] = []
        var p = cdOffset
        var count = 0
        while count < total, p + 46 <= n {
            // Central-directory file header signature 0x02014b50.
            guard b[p] == 0x50, b[p + 1] == 0x4B, b[p + 2] == 0x01, b[p + 3] == 0x02 else { break }
            let method = Int(u16(b, p + 10))
            let compSize = Int(u32(b, p + 20))
            let uncompSize = Int(u32(b, p + 24))
            let nameLen = Int(u16(b, p + 28))
            let extraLen = Int(u16(b, p + 30))
            let commentLen = Int(u16(b, p + 32))
            let localOff = Int(u32(b, p + 42))
            let nameStart = p + 46
            guard nameStart + nameLen <= n else { break }
            let name = String(decoding: b[nameStart ..< nameStart + nameLen], as: UTF8.self)

            // Find the compressed bytes via the LOCAL header (it carries its own
            // name/extra lengths, which can differ from the central record).
            if !name.hasSuffix("/"), localOff + 30 <= n,
               b[localOff] == 0x50, b[localOff + 1] == 0x4B, b[localOff + 2] == 0x03, b[localOff + 3] == 0x04 {
                let lNameLen = Int(u16(b, localOff + 26))
                let lExtraLen = Int(u16(b, localOff + 28))
                let dataStart = localOff + 30 + lNameLen + lExtraLen
                if dataStart + compSize <= n {
                    let comp = Data(b[dataStart ..< dataStart + compSize])
                    let content: Data?
                    switch method {
                    case 0: content = comp                                  // stored
                    case 8: content = inflate(comp, expected: uncompSize)   // deflate
                    default: content = nil
                    }
                    if let c = content {
                        out.append(Entry(name: name, data: c))
                    }
                }
            }
            p = nameStart + nameLen + extraLen + commentLen
            count += 1
        }
        return out
    }

    // MARK: - little-endian readers

    private static func u16(_ b: [UInt8], _ o: Int) -> UInt16 {
        UInt16(b[o]) | (UInt16(b[o + 1]) << 8)
    }

    private static func u32(_ b: [UInt8], _ o: Int) -> UInt32 {
        UInt32(b[o]) | (UInt32(b[o + 1]) << 8) | (UInt32(b[o + 2]) << 16) | (UInt32(b[o + 3]) << 24)
    }

    /// Raw-DEFLATE inflate via Apple's Compression framework. `COMPRESSION_ZLIB`
    /// decodes a header-less DEFLATE stream — exactly what ZIP method 8 stores.
    private static func inflate(_ deflated: Data, expected: Int) -> Data? {
        guard !deflated.isEmpty else { return Data() }
        #if canImport(Compression)
        let cap = max(expected, deflated.count * 8, 64 * 1024)
        var dst = Data(count: cap)
        let written = dst.withUnsafeMutableBytes { (dp: UnsafeMutableRawBufferPointer) -> Int in
            deflated.withUnsafeBytes { (sp: UnsafeRawBufferPointer) -> Int in
                guard let dBase = dp.bindMemory(to: UInt8.self).baseAddress,
                      let sBase = sp.bindMemory(to: UInt8.self).baseAddress else { return 0 }
                return compression_decode_buffer(dBase, cap, sBase, deflated.count, nil, COMPRESSION_ZLIB)
            }
        }
        guard written > 0 else { return nil }
        return dst.prefix(written)
        #else
        // Compression is Darwin-only; on Linux (unit tests) deflate entries are
        // unsupported. Stored (method 0) entries still extract — enough to test
        // the central-directory parsing here. iOS always has Compression.
        return nil
        #endif
    }
}
