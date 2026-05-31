import Foundation

/// OpenVPN 2.x → OpenVPN3 Config-Compatibility-Preprocessor.
///
/// Transformiert .ovpn-Files vor dem Übergeben an OpenVPNAdapter
/// (OpenVPN3 core, Apache 2.0). Strippt Direktiven die in
/// OpenVPN3 nicht implementiert sind und normalisiert Legacy-
/// Syntax. Erzeugt User-facing Warnings für entfernte Features.
///
/// Phase 4 — Layer 1 von 3 aus dem [[project_ios_port_plan]]
/// Compat-Layer-Strategie. Reicht für ~95% kompat mit user-imported
/// .ovpn-Files. Layer 2 (runtime event translation) + Layer 3
/// (deeper FAQ) später nachrüsten basierend auf Issue-Reports.
public struct OVPNCompatPreprocessor {

    public struct Result: Equatable, Sendable {
        public let cleanedConfig: String
        public let warnings: [Warning]
    }

    public enum Warning: Equatable, Sendable {
        case scriptHooksStripped         // up/down/route-up etc.
        case pluginsStripped             // dynamic .so plugin load
        case authUserPassRedirected      // inline-prompt → use Keychain
        case weakCipherDetected(String)  // BF-CBC etc.
        case deprecatedCompressDirective // comp-lzo → compress
        case unrecognizedDirective(String) // unknown line — passed through but flagged
    }

    public init() {}

    public func preprocess(_ rawConfig: String) -> Result {
        var lines: [String] = []
        var warnings: [Warning] = []
        var inInlineBlock = false
        var inlineBlockTag: String?
        var sawScriptHook = false
        var sawPlugin = false
        var sawDeprecatedCompress = false

        for raw in rawConfig.split(separator: "\n", omittingEmptySubsequences: false) {
            let line = String(raw)
            let trimmed = line.trimmingCharacters(in: .whitespaces)

            // 1. Inline-Block-Awareness (<cert>...</cert> etc.).
            //    Don't apply directive-level rules inside inline blocks.
            if let tag = inlineBlockTag {
                lines.append(line)
                if trimmed.lowercased() == "</\(tag)>" {
                    inlineBlockTag = nil
                    inInlineBlock = false
                }
                continue
            }
            if trimmed.hasPrefix("<") && trimmed.hasSuffix(">") && !trimmed.hasPrefix("</") {
                let tag = trimmed.dropFirst().dropLast()
                inlineBlockTag = String(tag)
                inInlineBlock = true
                lines.append(line)
                continue
            }

            // 2. Comments + empty lines pass through unchanged.
            if trimmed.isEmpty || trimmed.hasPrefix("#") || trimmed.hasPrefix(";") {
                lines.append(line)
                continue
            }

            // 3. Tokenize first word as directive name.
            let directive = trimmed.split(separator: " ", maxSplits: 1).first.map(String.init)?.lowercased() ?? ""

            switch directive {
            case "script-security":
                if !sawScriptHook {
                    warnings.append(.scriptHooksStripped)
                    sawScriptHook = true
                }
                // Drop the directive — iOS sandbox can't run scripts anyway.
                continue

            case "up", "down", "route-up", "route-pre-down", "learn-address", "client-connect", "client-disconnect":
                if !sawScriptHook {
                    warnings.append(.scriptHooksStripped)
                    sawScriptHook = true
                }
                continue

            case "plugin":
                if !sawPlugin {
                    warnings.append(.pluginsStripped)
                    sawPlugin = true
                }
                continue

            case "auth-user-pass":
                // Inline-prompt isn't possible inside an iOS NE — fold
                // through to the next config layer that wires Keychain.
                warnings.append(.authUserPassRedirected)
                lines.append("auth-user-pass") // bare directive, OVPN3 will surface a "need creds" event
                continue

            case "auth-user-pass-verify":
                // Server-side directive accidentally in client config.
                continue

            case "comp-lzo":
                if !sawDeprecatedCompress {
                    warnings.append(.deprecatedCompressDirective)
                    sawDeprecatedCompress = true
                }
                // Normalize to modern equivalent.
                let argPart = trimmed.split(separator: " ", maxSplits: 1)
                if argPart.count == 2 && argPart[1].lowercased() == "no" {
                    // skip
                } else {
                    lines.append("compress lzo")
                }
                continue

            case "cipher":
                let value = trimmed.split(separator: " ", maxSplits: 1).dropFirst().first.map(String.init) ?? ""
                if isWeakCipher(value) {
                    warnings.append(.weakCipherDetected(value))
                }
                lines.append(line)

            case "client-cert-not-required":
                // Removed in OpenVPN 2.5+, gone in OVPN3.
                continue

            case "ns-cert-type":
                // Deprecated long ago; OVPN3 ignores. Drop quietly.
                continue

            default:
                lines.append(line)
            }
        }
        _ = inInlineBlock
        return Result(cleanedConfig: lines.joined(separator: "\n"), warnings: warnings)
    }

    private func isWeakCipher(_ value: String) -> Bool {
        let lower = value.lowercased()
        return ["bf-cbc", "des-cbc", "3des", "rc2-cbc"].contains { lower.contains($0) }
    }
}
