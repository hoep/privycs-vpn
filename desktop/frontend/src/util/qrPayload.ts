// Mirror of android/app/src/main/java/com/privycs/vpn/util/QrCodePayload.kt.
// Keep the two in sync so cross-device QRs decode identically.

export type QrPayload =
  | { kind: 'wireguard'; content: string }
  | {
      kind: 'privycs'
      gatewayUrl?: string
      apiKey?: string
      connectionId?: string
      protocol?: string
      peerId?: string
    }
  | { kind: 'unknown'; raw: string }

/**
 * Detect and parse a scanned QR payload. Two shapes are recognised:
 *
 *  1. Raw wg-quick WireGuard config (standard QR from WireGuard apps).
 *     Recognised by a leading `[Interface]` section header, ignoring
 *     case and leading whitespace or `#` comments.
 *
 *  2. Privycs enrollment URL `privycs://enroll?url=...&apikey=...`.
 *     Used for OpenVPN and IPSec where embedding the full config in
 *     a QR is impractical (.ovpn with inline certs is 8-20 KB). The
 *     URL points at the gateway that holds the canonical config.
 */
export function parseQrPayload(raw: string): QrPayload {
  const trimmed = raw.trim()

  if (trimmed.toLowerCase().startsWith('privycs://')) {
    return parsePrivycsUri(trimmed)
  }

  // Find first non-blank, non-comment line. WireGuard configs always
  // start (after any comments) with `[Interface]`.
  const firstReal = trimmed
    .split(/\r?\n/)
    .map(l => l.trim())
    .find(l => l.length > 0 && !l.startsWith('#')) ?? ''
  if (firstReal.toLowerCase() === '[interface]') {
    return { kind: 'wireguard', content: trimmed }
  }

  return { kind: 'unknown', raw: trimmed }
}

function parsePrivycsUri(uri: string): QrPayload {
  try {
    const parsed = new URL(uri)
    if (parsed.protocol.toLowerCase() !== 'privycs:') {
      return { kind: 'unknown', raw: uri }
    }
    // URL parses `privycs://enroll?...` with host='enroll'. Match
    // leniently in case anyone tooled the URL as `privycs:enroll?...`.
    const host = (parsed.host || parsed.pathname.replace(/^\/+/, '')).toLowerCase()
    if (host !== 'enroll') {
      return { kind: 'unknown', raw: uri }
    }
    const q = parsed.searchParams
    return {
      kind: 'privycs',
      gatewayUrl: q.get('url') || q.get('gateway') || undefined,
      apiKey: q.get('apikey') || q.get('token') || undefined,
      connectionId: q.get('connection_id') || q.get('connection') || undefined,
      protocol: (q.get('protocol') || undefined)?.toLowerCase(),
      peerId: q.get('peer_id') || q.get('peer') || undefined,
    }
  } catch {
    return { kind: 'unknown', raw: uri }
  }
}
