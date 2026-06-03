# iOS VPN Client

[Back to Index](README.md) | [Android Client](android-client.md) | [Desktop Client](desktop-client.md) | [Connect Client](connect-guide.md)

The Privycs VPN iOS Client is a native app (SwiftUI + Apple NetworkExtension) that connects your iPhone or iPad to your Privycs VPN infrastructure. It supports **WireGuard**, **AmneziaWG**, **OpenVPN** and **IPSec/IKEv2** in a single app, with per-SSID On-Demand & Network Rules, VPN pools with rotation, a tunnel-health monitor, an IPv6 leak killswitch, QR / file / gateway import, and an encrypted backup that is byte-compatible with the Android and Desktop clients.

> **Status:** currently in **TestFlight beta**. The App Store release is on the way. This page describes the app's actual behaviour as implemented; honest platform limitations are called out in the last section.

---

## What is the iOS Client?

One native app instead of three (WireGuard, OpenVPN Connect, strongSwan):

- Manages multiple VPN connections from one place, with several protocols and endpoints under one connection
- Switches between AmneziaWG / WireGuard / OpenVPN / IPSec without juggling apps
- Auto-connects by Wi-Fi name (SSID), BSSID, or network type via On-Demand & Network Rules
- Rotates through **VPN pools** (geo-nearest / round-robin / random) with automatic failover
- Pulls configs one-tap from your Privycs gateway, or imports any standards-compliant config
- Backs up everything encrypted, restorable on Android and Desktop too

**Minimum: iOS / iPadOS 17.** iPhone and iPad.

---

## Installation

### TestFlight (current)

1. Open the TestFlight invite link for Privycs VPN on your device.
2. Install **TestFlight** from the App Store if prompted, then tap **Install** for Privycs VPN.
3. On first connect, iOS asks you to **allow Privycs to add VPN configurations** — see *Getting Started*.

### App Store (planned)

The public App Store listing is in preparation. Watch the releases page for the announcement.

---

## Getting Started

### Step 1 — Import a configuration

On the **Configs** tab tap **+** on a connection (or use the **Add** flow):

| Method | Use when |
|--------|----------|
| **Import from file** (`.conf` / `.ovpn` / `.sswan`) | You downloaded a config from your gateway or provider |
| **Scan QR code** | Your gateway generated a QR (WireGuard config or `privycs://enroll` enrollment) |
| **Pull from Privycs Gateway** | Pro feature: enter your gateway URL + API key once, then pull your configs directly |

The app auto-detects the protocol from the extension/content and parses the server address.

### Step 2 — Allow the VPN configuration

On the first connect, iOS shows *"Privycs VPN would like to add VPN configurations"* — tap **Allow** and authenticate. This is Apple's **Personal VPN** consent (NetworkExtension); it's a one-time per-install grant.

### Step 3 — Connect

Tap the large circle on the **Connect** screen. It turns teal, shows the uptime, the server endpoint + a flag/city line, live Download/Upload cards, and a detail panel (VPN IP / Endpoint / Last handshake for WireGuard/AmneziaWG).

### Step 4 — Switch protocols

If a connection holds more than one protocol, a protocol-pill row appears. Each pill shows the protocol and an **×N** count when a protocol has several configs. Tapping a pill switches the active config — if you're connected, the tunnel reconnects with the new protocol.

---

## Protocols

| Protocol | Engine on iOS | Runs in |
|----------|---------------|---------|
| **AmneziaWG** | `amneziawg-apple` (the WireGuardKit module, `libwg-go.a` from amneziawg-go) | In-process NEPacketTunnelProvider |
| **WireGuard** | same unified backend (a config without obfuscation keys runs as vanilla WG) | In-process NEPacketTunnelProvider |
| **OpenVPN** | OpenVPNAdapter (OpenVPN 3, Apache-2) | In-process NEPacketTunnelProvider |
| **IPSec / IKEv2** | Apple **NEVPNManager** Personal VPN (`NEVPNProtocolIKEv2`), certificate auth | System-managed |

### AmneziaWG: built in, zero setup

The AmneziaWG engine is bundled and runs **in-process** inside the NetworkExtension — exactly like vanilla WireGuard, one backend for both. Nothing to install. A config with no obfuscation keys (`Jc`, `S1`–`S4`, `H1`–`H4`, …) simply runs as standard WireGuard.

### Multi-config & failover

A connection can hold many protocols and many endpoints of the same protocol (a failover bag). Re-importing the exact same config updates it in place; a different server is added as a new entry with a disambiguating name.

---

## On-Demand & Network Rules

Settings → **On-Demand & Network Rules**. A single rule list is the source of truth; the engine walks it top-down on every network change and the first match wins.

**Match types:** Any network · Network type (Wi-Fi / Mobile / Ethernet / Wi-Fi-or-Mobile) · Wi-Fi name **exact** · Wi-Fi name **pattern** (glob, e.g. `Cafe*`) · **BSSID** (access-point MAC, anti-spoofing).

**Actions:** Disconnect (no VPN) · Connect active selection · Connect to a specific connection · Activate a specific pool.

iOS keeps the tunnel up via Apple's **on-demand** mechanism (`NEOnDemandRule`), so the rules survive backgrounding.

> **Wi-Fi name / BSSID matching** requires the **Access Wi-Fi Information** capability and reads the current network via `NEHotspotNetwork` while the app is in the foreground (Apple restriction — iOS only reveals the SSID/BSSID to apps with that entitlement). Network-type and "any" rules work without it.

---

## Kill Switch

On iOS the kill switch is enforced by the system: on-demand keeps the VPN configuration active and the NetworkExtension carries all traffic, so when on-demand is enabled traffic does not silently fall back outside the tunnel. A manual disconnect turns on-demand off (so iOS doesn't immediately reconnect the tunnel you just tore down).

## Tunnel Health

While connected, a reachability monitor probes a configurable target (default `1.1.1.1`) through the tunnel and shows a health pill: 🟢 **Healthy** · 🟡 **Degraded** · 🔴 **Recovering** after repeated failures. Configure interval/target/threshold in Settings → Tunnel Health. (Foreground-only — see platform limits.)

## IPv6 Leak Killswitch

**Always-on, no setting** — identical to Android. Every connect injects an IPv6 sink so a v4-only tunnel can't leak your device's IPv6 traffic out the native interface:

| Protocol | Mechanism |
|----------|-----------|
| WireGuard / AmneziaWG | `::/0` appended to `[Peer].AllowedIPs` |
| OpenVPN | `route-ipv6 ::/0` + `redirect-gateway ipv6` appended |

Idempotent — configs that already cover `::/0` pass through unchanged.

---

## VPN Pools

A pool is a set of server configs the app rotates between by policy — say "Europe Pool" instead of pinning one server.

**Policies:** **Geo-Nearest** (your country → continent → any, random within the cohort) · **Round-Robin** (member-id cursor so the same exit isn't repeated back-to-back) · **Random**.

**Import:** Add Pool → pick a **`.zip`** archive from your provider (all configs inside become members) or individual files. Country is parsed from the `<cc>-<city3>-…` filename pattern.

**Rotation:** a foreground timer rotates at the interval; a `BGTaskScheduler` request covers the backgrounded case (best-effort — see limits). **Resilience:** up to 3 connect attempts per rotation, excluding members within a 30-minute unreachable window; a WireGuard/AmneziaWG member that doesn't pass traffic within a few seconds is marked unreachable and the next candidate is tried; if every member fails but the device clearly has internet, the marks are cleared so the pool never gets permanently stuck.

**Per-pool DNS override** and **split-tunnel bypass** (bypass-CIDRs + "also bypass private networks") are configurable on the pool detail screen.

---

## DNS Override

Settings → **DNS Override** (comma-separated IPs), with per-connection and per-pool overrides taking precedence over the global value. Applied to **WireGuard, AmneziaWG and OpenVPN** (the override replaces the config's DNS / drops server-pushed DNS).

> **IPSec/IKEv2:** DNS override is **not** available — Apple's `NEVPNProtocolIKEv2` exposes no per-tunnel DNS API. Use WireGuard/AmneziaWG/OpenVPN if you need a custom resolver.

---

## Encrypted Backup

Settings → **Backup & Restore**. Export an AES-256-GCM encrypted backup (PBKDF2-HMAC-SHA256, 100k iterations) of your connections, pools, network rules and settings, protected by a passphrase you choose. **Cross-platform:** a backup made on iOS restores on the Android and Desktop clients and vice-versa (identical envelope + key derivation). There is no recovery if you forget the passphrase.

---

## Privacy

No accounts, no analytics, no tracking, no ads. Your configs, keys, gateway URL/API key and rule lists stay in the app's encrypted on-device storage (iOS Keychain + App Group) and are never transmitted to us. Wi-Fi SSID/BSSID values are evaluated only locally for your rules. Full details: **[iOS App Privacy Policy](/docs/ios-client-privacy)**.

---

## Pro

A one-time in-app purchase (StoreKit) unlocks the Pro features, or redeem a cross-platform license key (the same ed25519 key works on Android, Desktop and iOS). Outside Pro features the client is gateway-agnostic — it imports and runs any standards-compliant `.conf` / `.ovpn` / `.sswan`.

---

## iOS platform limits (honest)

iOS is more locked-down than Android; these are real platform constraints, not missing work:

- **IPSec DNS override** — not possible (`NEVPNProtocolIKEv2` has no DNS API). WG/AWG/OVPN are fine.
- **Wi-Fi SSID/BSSID rules** — require the Access-Wi-Fi-Information capability and the app in the foreground to read the current network. Network-type/any rules always work.
- **Background pool rotation & tunnel-health** — best-effort. iOS runs background tasks opportunistically (no precise timer like Android's AlarmManager), so rotation/health are reliable in the foreground and "eventually" in the background.
- **Per-app VPN** — not offered (Apple restricts per-app VPN to MDM-managed devices). Use split-tunnel bypass CIDRs on pools instead.

Everything else — multi-protocol, pools, on-demand network rules, DNS override (WG/AWG/OVPN), encrypted cross-platform backup, IPv6 leak killswitch — is at parity with the Android and Desktop clients.

---

## Version Compatibility

| Privycs VPN iOS Version | Min iOS / iPadOS | Devices | Distribution |
|-------------------------|------------------|---------|--------------|
| v1.0.8-beta.x | iOS / iPadOS 17 | iPhone, iPad | TestFlight (App Store pending) |

Older iOS versions (below 17) are not supported due to the SwiftUI and NetworkExtension features the app relies on.

---

## What's New

The iOS client is in **TestFlight beta** (version space `v1.0.8-beta.x`). It shares its protocol engines, wire formats and encrypted-backup schema with the Android and Desktop clients, so configs and backups move between all three. Recent builds:

- **v1.0.8-beta.15** — **Reliability pass from device testing.** Tunnel-health no longer gets stuck on "Recovering": live tunnel traffic now counts as a healthy signal on its own, so an unreachable or mis-typed health-probe target can't drag an actively-working tunnel down; the ping target is saved as you type (not only on Return) and any accidental `:port` is stripped. **Backup restore** now re-applies everything in the file and reports the real totals — it previously said "Restored 0" when the data was already on the device. The **Connect-screen target picker** shows *all* of a connection's protocols (brand logos), not just the active one. The **Logs** screen loads instantly again (it now reads off the main thread and shows the newest entries with a "Load earlier entries" button) — a large log used to freeze the view. Each config on the **Configs** screen gets an explicit delete (×). The gateway-download list shows the interface **and** the assigned VPN IP, connection names are derived from the filename exactly like Android, and the "Gateway configured" status label is now translated.
- **v1.0.8-beta.14** — **Full localization — the app speaks 6 languages.** English, German, Spanish, French, Italian and Portuguese, switchable inside the app under *Settings → Language* and applied instantly across the whole UI **including the bottom tab bar**, no restart. Translations are taken from the Android app for a 1:1 match. Also: the **Configs** screen now shows one badge per config with its server (the ×N grouped count lives on the Connect screen), the Connect screen was tightened so the tunnel-health pill is visible without scrolling, and tunnel-health is now foreground-accurate (no false "degraded" after the app was in the background).
- **v1.0.8-beta.13** — **Connect-screen polish.** The connection/pool picker at the top is always usable — even while connected — and switching target now disconnects the old and connects the new in one step. Protocol pills appear only for a standard connection with more than one protocol (never for a pool, which has its own card). The endpoint detail reads `host:port`, pills are logo-only in their brand colour, the failover-order list shows the protocol logos, *Settings → Version* shows the build number, and the **Kill Switch** is now a real toggle.
- **v1.0.8-beta.12** — **Feature-parity batch.** Added the Tunnel-Health settings (mode / target / interval / threshold), a re-orderable Protocol-Failover order, manual **Pause / Resume**, a DNS preset picker with a "Test DNS" button (global / per-connection / per-pool), in-app Help that renders the live guide, and the **"Create a VPN pool"** entry in the Add tab.
- **v1.0.8-beta.11** — **First full-feature TestFlight build.** Multi-protocol (AmneziaWG / WireGuard / OpenVPN / IPSec) with AmneziaWG built in, VPN pools with rotation and geo-nearest selection, On-Demand & Network Rules with per-SSID/BSSID matching, encrypted cross-platform backup, the always-on IPv6-leak killswitch, gateway pull and QR import.
- **v1.0.8-beta.1** — Initial TestFlight beta: SwiftUI app on Apple's NetworkExtension, the four protocols, file/QR import and the connection/pool model.
