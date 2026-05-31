import Foundation

/// Parser für AmneziaWG-Config (wg-quick INI + obfuscation params).
/// Validated minimal — wir vertrauen dem amneziawg-go core das
/// detaillierte Format-Parsing übernehmen zu lassen, dieser Parser
/// extrahiert nur was wir client-side für NEPacketTunnelNetworkSettings
/// brauchen.
public struct AmneziaWGConfig {
    /// Peer-Endpoint (für tunnelRemoteAddress NEPacketTunnelNetworkSettings).
    public let peerEndpoint: String
    public let ipv4Addresses: [String]
    public let ipv4SubnetMasks: [String]
    public let ipv6Addresses: [String]
    public let ipv6PrefixLengths: [NSNumber]
    /// AllowedIPs gemapped zu (address, mask) tuples für NEIPv4Route.
    public let includedV4Routes: [(String, String)]
    public let includedV6Routes: [(String, NSNumber)]
    public let dnsServers: [String]
    public let mtu: Int
    /// UAPI format string, ready to pass to amneziawg-go's
    /// SetConfig. Contains private_key, public_key, endpoint,
    /// allowed_ip, plus AmneziaWG obfuscation lines (Jc, Jmin,
    /// Jmax, S1, S2, H1, H2, H3, H4).
    public let uapiConfig: String

    public static func parse(_ raw: String) throws -> AmneziaWGConfig {
        var section: String?
        var iface: [String: String] = [:]
        var peer: [String: String] = [:]
        for rawLine in raw.split(separator: "\n") {
            let line = rawLine.trimmingCharacters(in: .whitespaces)
            if line.isEmpty || line.hasPrefix("#") { continue }
            if line.hasPrefix("[") && line.hasSuffix("]") {
                section = String(line.dropFirst().dropLast()).lowercased()
                continue
            }
            guard let eq = line.firstIndex(of: "=") else { continue }
            let key = line[..<eq].trimmingCharacters(in: .whitespaces).lowercased()
            let value = line[line.index(after: eq)...].trimmingCharacters(in: .whitespaces)
            if section == "interface" {
                iface[key] = value
            } else if section == "peer" {
                peer[key] = value
            }
        }

        guard let privateKey = iface["privatekey"],
              let publicKey = peer["publickey"],
              let endpoint = peer["endpoint"] else {
            throw NSError(domain: "AmneziaWGConfig", code: 1, userInfo: [
                NSLocalizedDescriptionKey: "Missing required keys (privatekey/peer.publickey/peer.endpoint)"
            ])
        }

        // Address parsing — supports "10.0.0.1/32" and "fd00::1/128"
        let addresses = iface["address"]?.split(separator: ",") ?? []
        var v4Addrs: [String] = []
        var v4Masks: [String] = []
        var v6Addrs: [String] = []
        var v6PrefixLengths: [NSNumber] = []
        for a in addresses {
            let parts = a.trimmingCharacters(in: .whitespaces).split(separator: "/")
            guard parts.count == 2 else { continue }
            let ip = String(parts[0])
            let prefix = Int(parts[1]) ?? 32
            if ip.contains(":") {
                v6Addrs.append(ip)
                v6PrefixLengths.append(NSNumber(value: prefix))
            } else {
                v4Addrs.append(ip)
                v4Masks.append(prefixToMaskV4(prefix))
            }
        }

        // AllowedIPs parsing → routes
        let allowedIPs = peer["allowedips"]?.split(separator: ",") ?? []
        var v4Routes: [(String, String)] = []
        var v6Routes: [(String, NSNumber)] = []
        for r in allowedIPs {
            let parts = r.trimmingCharacters(in: .whitespaces).split(separator: "/")
            guard parts.count == 2 else { continue }
            let ip = String(parts[0])
            let prefix = Int(parts[1]) ?? 32
            if ip.contains(":") {
                v6Routes.append((ip, NSNumber(value: prefix)))
            } else {
                v4Routes.append((ip, prefixToMaskV4(prefix)))
            }
        }

        let dns = iface["dns"]?.split(separator: ",").map {
            $0.trimmingCharacters(in: .whitespaces)
        } ?? []
        let mtu = Int(iface["mtu"] ?? "1420") ?? 1420

        // Build UAPI config-string. amneziawg-go akzeptiert das
        // gleiche UAPI-Format wie wireguard-go plus obfuscation-
        // params. Hex-encoded keys ("private_key=<hex>") sind
        // standard; wir lassen amneziawg-go den base64→hex
        // conversion via SetConfig "private_key_base64" extension
        // machen.
        var uapi = ""
        uapi += "private_key_base64=\(privateKey)\n"
        uapi += "public_key_base64=\(publicKey)\n"
        uapi += "endpoint=\(endpoint)\n"
        for (ip, mask) in v4Routes {
            uapi += "allowed_ip=\(ip)/\(maskToPrefixV4(mask))\n"
        }
        for (ip, prefix) in v6Routes {
            uapi += "allowed_ip=\(ip)/\(prefix.intValue)\n"
        }
        // AmneziaWG obfuscation params — passed through verbatim
        let obfuscationKeys = ["jc", "jmin", "jmax", "s1", "s2", "h1", "h2", "h3", "h4"]
        for k in obfuscationKeys {
            if let v = iface[k] {
                uapi += "\(k)=\(v)\n"
            }
        }
        if let psk = peer["presharedkey"] {
            uapi += "preshared_key_base64=\(psk)\n"
        }

        return AmneziaWGConfig(
            peerEndpoint: endpoint.split(separator: ":").first.map(String.init) ?? endpoint,
            ipv4Addresses: v4Addrs,
            ipv4SubnetMasks: v4Masks,
            ipv6Addresses: v6Addrs,
            ipv6PrefixLengths: v6PrefixLengths,
            includedV4Routes: v4Routes,
            includedV6Routes: v6Routes,
            dnsServers: dns,
            mtu: mtu,
            uapiConfig: uapi
        )
    }

    private static func prefixToMaskV4(_ prefix: Int) -> String {
        let mask = UInt32(0xFFFFFFFF) << (32 - prefix)
        let bytes = [
            (mask >> 24) & 0xff,
            (mask >> 16) & 0xff,
            (mask >> 8) & 0xff,
            mask & 0xff,
        ]
        return bytes.map { String($0) }.joined(separator: ".")
    }

    private static func maskToPrefixV4(_ mask: String) -> Int {
        let octets = mask.split(separator: ".").compactMap { UInt8($0) }
        guard octets.count == 4 else { return 32 }
        var count = 0
        for b in octets {
            count += b.nonzeroBitCount
        }
        return count
    }
}
