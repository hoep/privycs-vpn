# Android VPN Client

[Back to Index](README.md) | [Desktop Client](desktop-client.md) | [Connect Client](connect-guide.md)

The Privycs VPN Android Client is a native Android app (Kotlin + Jetpack Compose) that connects your mobile device to your Privycs VPN infrastructure. It supports **WireGuard**, **OpenVPN**, and **IPSec/IKEv2** protocols (with optional **post-quantum** RFC 8784 PPK on IPSec) in a single app, with a home-screen widget, Quick Settings Tile, Connect-on-Demand rules, per-network rules with BSSID-trust, a tunnel-health monitor with auto-recovery and multi-protocol failover, a real Kill Switch, Per-App VPN selection, QR code import, and encrypted local backup.

---

## What is the Android Client?

The Android Client replaces the need to install three separate VPN apps (WireGuard, OpenVPN Connect, strongSwan) with a single native app that:

- Manages multiple VPN connections from one place
- Switches between WireGuard / OpenVPN / IPSec without reinstalling apps
- Auto-connects based on WiFi SSID, mobile data, or per-network rules with BSSID-trust
- Monitors tunnel health and auto-recovers / fails over to a different protocol when the tunnel goes silent
- Blocks traffic with a sinkhole Kill Switch if the tunnel drops unexpectedly
- Lets you route only selected apps through the VPN (or exclude specific apps)
- Imports configs via QR code, config file, or paste-from-clipboard
- Runs as a home-screen widget with live traffic statistics
- Backs up all connections and settings encrypted on-device, including per-network rules

Minimum: Android 8.0 (API 26). Tested on Android 10+ devices. Compatible with Android 14.

---

## Installation

### APK Download

1. Visit the **Downloads** page on your Privycs gateway
2. Download **privycs-vpn-release.apk** (ARMv8, ARMv7, x86_64 combined APK)
3. On your Android device, enable **Install unknown apps** for your browser in *Settings → Apps → Special access*
4. Open the APK with your file manager and tap **Install**
5. Grant the VPN permission when prompted on the first connect

The APK is signed with the Privycs release keystore. The SHA-256 fingerprint is published on the downloads page so you can verify the signature with `apksigner verify --print-certs`.

### Direct from GitHub Releases

Each tagged release (`v0.9.X.Y`) also publishes the signed APK as a GitHub Actions artifact under *Actions → Android Release → [tag]*. Download the artifact ZIP, extract, and install the APK.

### Google Play Store

Not yet available. Play Store listing is planned but requires resolving OpenVPN's GPL-2 compatibility with Play Console terms. Watch the releases page for announcements.

---

## Getting Started

### Step 1: Create Your First Connection

On first launch, the **Connect** screen shows a large empty circle and "No connection yet". Tap the **+** button in the top-right corner of the **Configs** screen to add a connection.

Three import methods are available:

| Method | Use When |
|--------|----------|
| **Import file** (`.conf` / `.ovpn` / `.sswan`) | You downloaded a config from your gateway |
| **Scan QR code** | Your gateway generated a QR code for WireGuard |
| **Paste config text** | You have the config content in your clipboard |

The app auto-detects the protocol from the file extension or content. Enter a connection name (e.g. "Home VPN", "Office VPN") and save.

### Step 2: Grant VPN Permission

On the first connect tap, Android shows a system dialog: *"Privycs VPN wants to set up a VPN connection that allows it to monitor network traffic..."* — tap **OK**. This is a one-time per-install grant.

### Step 3: Connect

1. From the **Connect** screen, tap the large circle button
2. The button turns teal and shows "Connected" with an uptime clock (HH:MM:SS)
3. Download / upload cards show cumulative transfer and live speed with sparkline history
4. VPN IP and server endpoint appear at the bottom

### Step 4: Switch Protocols

If your connection has multiple protocol configs, three protocol pills appear below the uptime: **WireGuard**, **IPSec**, **OpenVPN**. Tap a pill to switch — the app disconnects and reconnects using the selected protocol. The active pill is tinted.

---

## Features

### Multi-Protocol VPN

Each connection can have multiple protocol configurations side-by-side. Upload a `.conf` file for WireGuard and a `.sswan` for IPSec under the same connection name — the app remembers both and lets you switch between them without reconfiguring.

| Protocol | Library | Kernel / Userspace |
|----------|---------|--------------------|
| WireGuard | wireguard-android (GoBackend) | Userspace (no root) |
| OpenVPN | ics-openvpn (vendored, v0.7.64) | Userspace + VpnService |
| IPSec / IKEv2 | strongSwan libcharon (JNI) | Userspace charon daemon |

All three protocols run without root access, sharing a single `VpnService` slot. Only one protocol is active at a time per connection.

#### Post-Quantum IPSec (RFC 8784 PPK)

When the gateway has post-quantum mode enabled (`interface.pq_safe = true`) on an IPSec config, the resulting `.sswan` profile carries an additional **Post-quantum Pre-shared Key (PPK)** alongside the regular IKEv2 credentials. The Android client unpacks it transparently and passes it to strongSwan via `VpnProfile.setPPKId()` / `setPPKPsk()`, hardening the IKE_AUTH exchange against future cryptanalytic attacks on D-H / ECDH key agreement.

| What you see | When |
|---|---|
| Nothing different in the UI | Always — PPK is a wire-protocol detail, not a user choice |
| `pq_safe=true` in the imported `.sswan` JSON | The gateway emitted post-quantum material |
| The same connection rotates PPK cleanly | When the gateway issues a new PPK (via re-import or pool rotation), the existing strongSwan profile is updated in-place rather than recreated |

PPK applies only to IPSec/IKEv2; WireGuard's own pre-shared-key field already serves a similar purpose, and OpenVPN does not currently support PPK.

#### Multi-Protocol Failover

When a connection has multiple protocol configs (e.g. one WireGuard `.conf` and one IPSec `.sswan` under the same connection name), Tunnel-Health-driven recovery rotates to the next protocol if the current one repeatedly fails. See **Tunnel Health & Auto-Recovery** below for full rotation semantics.

This means a single connection acts as its own redundancy: if your network blocks UDP (some carriers do), WireGuard's handshake will time out and the recovery loop falls back to IPSec or OpenVPN — without you re-tapping anything.

### Connect-on-Demand

Connect-on-Demand (COD) automatically brings the VPN up or down based on network conditions. Configure under **Settings → Connect on Demand**.

**Trigger types:**
- **WiFi only** — connect when on WiFi, disconnect on mobile
- **Mobile only** — connect on mobile data, disconnect on WiFi
- **WiFi & Mobile** (default) — connect whenever any network is available

**SSID filtering** (available when WiFi is part of the trigger):
- **All SSIDs** — connect on any WiFi
- **Only these** — connect only when on listed SSIDs (allow-list)
- **Except these** — connect on all WiFi except listed SSIDs (deny-list)

The app evaluates rules on every network change (WiFi switch, mobile handoff, airplane mode) and automatically connects or disconnects to match. Network detection uses a **VPN-aware callback** (`NET_CAPABILITY_NOT_VPN`) so that handoffs *under an active tunnel* (e.g. WiFi → mobile while the tunnel is up) are still seen and acted on — historically the underlying network was masked by the VPN's own callback and rules went stale until the next manual reconnect.

> **Note:** SSID filtering requires the *Precise Location* permission on Android 10+ (Google's privacy policy ties WiFi SSID visibility to location). The app prompts for this permission only if you select "Only these" or "Except these" modes.

#### Foreground Keepalive (opt-in)

When the screen is off, Android's Doze mode aggressively suspends app processes. If Connect-on-Demand needs to react *during* deep sleep — not just at the next wake-up — toggle **Settings → Connect on Demand → Keep monitor alive** on. This pins a **persistent low-priority foreground notification** that holds the VPN service alive across Doze, so the network callback continues firing and your COD rules apply within seconds of a network change.

Trade-off: a small permanent battery cost (typically <1% per day on most devices) in exchange for instant on-demand reaction in standby. Off by default; turn on only when you need it.

On aggressive OEM Android skins (Samsung One UI, Xiaomi MIUI, Huawei EMUI, Oppo, Vivo) the system's task-killer may still terminate the foreground service when you swipe the app away from Recent Apps. Privycs declares `android:stopWithTask="false"` and additionally schedules a Doze-aware self-restart alarm (`AlarmManager.setExactAndAllowWhileIdle`, ~5 s after task removal) so the monitor comes back even when the OEM kills it. For maximum reliability, also whitelist Privycs in **Settings → Battery → Battery optimization → Privycs VPN → Don't optimize** (and on Xiaomi/MIUI: enable Auto-start in the Security app).

### Kill Switch

A real Kill Switch that blocks ALL network traffic if the VPN tunnel drops unexpectedly. Configure under **Settings → Kill Switch**.

**State machine:**
- **IDLE** — disabled, or no successful connect yet this session
- **ARMED** — user has successfully connected; a sinkhole is ready to engage
- **SINKHOLE** — the tunnel dropped unexpectedly; a block-all `VpnService` fd now blackholes all non-app traffic

**How it works:** On a detected tunnel drop (via Android's `NetworkCallback` with `NET_CAPABILITY_NOT_VPN` filter), the app establishes a second `VpnService.Builder` tun fd that captures `0.0.0.0/0` + `::/0` but never reads packets — effectively a traffic blackhole. Our own package is added to the disallow-list so the Retry Connect notification works. The Connect screen circle turns red with a shield-bad icon; the widget does the same.

**Release conditions:**
- User taps **Retry Connect** → fresh tunnel replaces the sinkhole
- User disables the Kill Switch toggle → sinkhole closes immediately
- User manually disconnects → state returns to IDLE

### Tunnel Health & Auto-Recovery

A passive monitor watches a connected tunnel for silent-failure conditions — a tunnel that's marked "up" by the OS but no longer carries packets. Configure under **Settings → Tunnel Health**.

**Health states (visible as a pill on the Connect screen and Settings):**
- 🟢 **Healthy** — packets flowing in both directions
- 🟡 **Degraded** — handshake stale, RTT spiking, or one-way traffic only
- 🔴 **Dead** — no successful keepalive ack for the configured timeout

**Detection mechanics:**

| Mode | What's checked | When |
|---|---|---|
| **Off** | Nothing — pre-v0.9.12.0 behaviour | Tunnel-health pill hidden |
| **Auto** (default) | WireGuard handshake age, IPSec SA-INSTALLED, OpenVPN bytes-counter delta | Every 30 s while connected |
| **Active** (Auto + reachability) | Auto checks plus a tiny ICMP/UDP probe to a configurable target host | Every 30 s |

In **Active** mode, set **Tunnel Health Target** to a small reliable host that's reachable through the tunnel but unlikely to false-alarm — `1.1.1.1`, `9.9.9.9`, or your gateway's own LAN side all work well. The probe is a single packet every 30 s.

**Auto-recovery actions** (when the state stays `Dead` for 60 s, configurable):
- **Reconnect same protocol** — tear down and re-establish the same tunnel
- **Multi-protocol failover** (when the connection has more than one protocol config) — try the next protocol on the same connection (e.g. WireGuard dies → switch to IPSec → switch to OpenVPN). The active protocol is rotated each recovery attempt; if all configured protocols fail, the cycle pauses for 5 minutes before retrying.

The monitor is process-singleton: it runs only while a tunnel is connected (started in `VpnServiceManager.connect`, stopped on disconnect or process kill). It does NOT contribute to battery use when no tunnel is up.

> **Why this exists.** Stock Android shows a tunnel as "connected" as long as the `VpnService` fd exists, regardless of whether packets actually traverse it. A captive-portal page-grabber, a midstream MTU-blackhole, or a server crash can leave the tunnel "up" while every packet drops. v0.9.12.0 added Phase-1 detection; v0.9.14.45 wired auto-recovery for single connections; v0.9.14.66 added multi-protocol failover.

### Per-Network Rules (BSSID-trust)

Configure under **Settings → Network Rules**. Goes beyond Connect-on-Demand's coarse SSID allow/deny lists by letting you encode *per-network actions*: VPN-on, VPN-off, choose-this-connection, choose-this-pool, all keyed by network identity.

**Rule key types:**
- **SSID match** — by WiFi network name (case-sensitive)
- **BSSID match** — by access-point MAC address (defends against rogue/evil-twin SSID spoofing — same name, different AP)
- **Mobile match** — by mobile-data carrier or "any mobile network"

**Rule actions:**
- **Disconnect VPN here** — stop the tunnel on this network (e.g. trusted home WiFi)
- **Connect VPN here** — start the tunnel on this network (e.g. coffee-shop WiFi)
- **Use connection X** — switch to a specific saved connection on this network
- **Use pool Y** — switch to a specific pool on this network

Rules evaluate top-down on every network change; the first match wins. Connect-on-Demand still acts as the default fallback when no per-network rule applies. To capture the BSSID of the network you're currently on, tap the **(+) From current network** button — the app reads SSID + BSSID and pre-fills a new rule.

**Backup-aware.** Per-network rules ship in encrypted backup schema **v4** (introduced v0.9.14.0), so your rule set survives a device migration. Older v3 backups still restore cleanly with an empty rules set.

> **Privacy note.** BSSID-match shows up to the user's awareness only — Privycs does not transmit any SSID or BSSID off-device. The BSSID list lives in the same app-private storage as your connection configs.

**Resilience:** Three independent defense layers protect against edge cases:
- On service recreate (START_STICKY restart), sinkhole is re-established synchronously before anything else runs
- A 3-second poll-loop watchdog catches any "state=SINKHOLE but fd=null" condition
- On a new non-VPN network appearing (e.g. airplane-mode-off → WiFi returns), the sinkhole fd is refreshed to reassert its routes against the newly-shuffled kernel route table

### IPv6 Leak Killswitch

**Always-on, no setting.** Leaving IPv6 leakable through a v4-only tunnel is a critical security bug, not a user preference. On v0.9.14.96 and later, every connect injects an IPv6 sink into the tunnel config so that if the tunnel itself is IPv4-only, your dual-stack device's IPv6 traffic gets blackholed at the tun fd instead of leaking out the native IPv6 default route to its physical interface.

| Protocol | Mechanism |
|---|---|
| **WireGuard** | `::/0` appended to `[Peer].AllowedIPs`. The library installs an IPv6 default route to our tun. The peer has no v6 endpoint → packets dropped at write. |
| **OpenVPN** | `route-ipv6 ::/0` + `redirect-gateway ipv6 def1 bypass-dhcp` appended. ics-openvpn parses both directives and routes all v6 to the tun. |
| **IPSec** | `::/0` injected into the `.sswan` `remote_ts` traffic-selector list — best-effort. strongSwan negotiates traffic selectors with the gateway during IKE_AUTH, and a v4-only server may narrow the selector back to IPv4 only. **In that case** the post-connect detector catches it and shows a banner: *"IPv6 traffic may bypass the VPN — server didn't accept IPv6 traffic-selector. Switch to WireGuard for full v6 protection."* |

The injection is **idempotent** — configs that already cover `::/0` (e.g. dual-stack tunnels with explicit `AllowedIPs = 0.0.0.0/0, ::/0`) pass through unchanged. The auto-skip avoids over-rewriting when there's nothing to fix.

> **Why this matters.** A typical home network has IPv6 from the ISP. If your VPN provider has a v4-only profile (common for older configurations or budget tiers), without this killswitch your AAAA-resolved DNS lookups (Google, Cloudflare, Facebook, etc.) would resolve to v6 addresses and your traffic would exit via your home ISP's v6 — completely outside the VPN. Server-side enforcement is impossible because the leak happens client-side before any packet reaches the gateway.

### Per-App VPN

Route only specific apps through the VPN (Include-only), or exclude specific apps from the VPN (Exclude-selected). Configure under **Settings → Per-App VPN**.

| Mode | Behaviour |
|------|-----------|
| **Exclude Selected** | Selected apps bypass the VPN and use the direct network. All other apps go through the VPN. |
| **Include Only** | Only selected apps use the VPN. All other apps bypass. |

The app automatically includes its own package (`com.privycs.vpn`) in the Include-list so the VPN client's handshake traffic reaches the server even when only one other app is selected.

The picker shows **all installed apps that use the network**, including system apps like **Android Auto** (`com.google.android.projection.gearhead`), Google Maps, Play Store, and other Google-shipped apps that hold `INTERNET` permission. Earlier builds filtered system apps out and Android Auto was missing from the list — fixed in v0.9.14.75. To find a specific app fast, type its name (or its package) into the picker's search field.

Per-App VPN works across all three protocols: WireGuard, OpenVPN, and IPSec. For WireGuard, the selections are injected into the config as `IncludedApplications` / `ExcludedApplications` lines in the `[Interface]` section. For OpenVPN, it sets `mAllowedAppsVpn` on the profile. For IPSec, `VpnProfile.setSelectedApps()` on strongSwan's profile.

> Android 7+ requirement: Per-App VPN uses Android's `VpnService.Builder.addAllowedApplication` / `addDisallowedApplication`, which requires Android 7.0 (API 24) or higher. All Android 8+ devices are supported.

### DNS Override

Override the DNS resolvers used while the VPN is connected. Set under **Settings → DNS Override** as a comma- or space-separated list of IPs (e.g. `1.1.1.1, 9.9.9.9`), or pick one of the built-in **brand-coloured presets** (Cloudflare, Quad9, NextDNS, AdGuard, Google) from the dropdown next to the field. Presets fill the IP field with their public IPv4 + IPv6 endpoints in one tap. Empty value means the VPN config's own DNS line wins.

**Where DNS Override applies.** The setting exists at three levels of granularity, and each level overrides the level above it:

| Level | Where to set | Applies to |
|---|---|---|
| **Global** | Settings → DNS Override | Every connection and pool that doesn't have its own override |
| **Per-Pool** | Pool edit sheet → DNS Override | Every member of that pool, persists across rotation |
| **Per-Single-Connection** | Connection edit sheet → DNS Override | Just that one single connection |

The **Test** button next to the DNS Override field probes the entered resolver(s) over the active tunnel and reports `OK` / `timeout` / `unreachable` per server — useful for catching typos or filtered carriers before you connect to something that depends on it.

A small **Private DNS hint** banner appears if Android's *Private DNS* setting (Settings → Network → Private DNS) is set to a host other than "Off" or "Automatic" — Private DNS bypasses the VPN's DNS server in some Android versions, so your override may not take effect until you turn it off.

When configured, the override replaces both:

- the `DNS = ...` line a WireGuard config carries
- the DNS server an OpenVPN gateway pushes via `dhcp-option DNS`
- the DNS server in an IPSec strongSwan profile (`.sswan` `dns-servers` field)

**Why use it:** route lookups via your preferred resolver (e.g. NextDNS, AdGuard DNS, Quad9, your own Pi-hole), regardless of what the VPN provider configured. Useful for blocking ads/trackers at the resolver, enforcing DNSSEC, or preventing DNS leaks to the provider's logger.

**Per-protocol mechanics on Android:**

| Protocol | Implementation |
|----------|----------------|
| **WireGuard** | Config text patch before the GoBackend parser sees it: existing `DNS = ...` line in `[Interface]` is replaced; if absent, a new line is inserted. The patched config drives `VpnService.Builder.addDnsServer()` via the upstream config-parser code path. |
| **OpenVPN** | Config text patch: `pull-filter ignore "dhcp-option DNS"` plus one `dhcp-option DNS <ip>` directive per override server are prepended. ics-openvpn's parser recognises both directives and sets `mDns1` / `mDns2` on the profile from the override instead of the server's pushed value. |
| **IPSec/IKEv2** | strongSwan path: `IpSecTunnel.connect()` accepts an optional `dnsOverrideServers` parameter, populated from Settings, and forwards it to `VpnProfile.setDnsServers()`. The CharonVpnService propagates it through to the kernel via `VpnService.Builder.addDnsServer()` instead of the `.sswan` profile's value. |

The override applies uniformly across single connections AND pool members. Switching pool members during rotation does NOT reset the DNS override — the same override applies to whichever member is currently active.

**Verifying the override is in effect:** run a DNS lookup app (e.g. `Termux` with `nslookup something.com`, or any DNS test app from the Play Store) while connected. The reported DNS-server should match your override IP. If it shows the VPN provider's IP instead, the override field is empty (check Settings → DNS Override).

**No-op edge case:** if your VPN config has no `DNS = ...` line (WireGuard) and the server pushes no DNS (OpenVPN), the override is what you get — no fallback to system DNS. Set the override explicitly OR add a `DNS = ...` line to the imported config to cover both paths.

**Cross-platform parity:** the same Settings field exists on the Desktop Client. Linux desktop also supports IPSec DNS override (via privileged helper rewriting `/etc/resolv.conf`); macOS and Windows desktop IPSec do NOT — the OS NetworkExtension / `rasdial` paths there don't expose per-tunnel DNS APIs. Android has the cleanest implementation because `VpnService.Builder.addDnsServer()` is the canonical API for all three protocols.

### Connection Pools

A Connection Pool is a collection of VPN configs (members) the app rotates between by policy. Instead of "I'm connected to *server-fr-3*", you say "I'm connected to *Europe Pool*" and the app picks — and re-picks on a schedule — the actual server.

**Why use a Pool**

- **Privacy.** Rotate exit IPs so a single observable session stays correlatable for only a fraction of your VPN time.
- **Geo-availability.** Hold dozens to hundreds of countries in one configuration; the app selects per-policy without you re-importing.
- **Reliability.** If a member fails to come up, the app auto-tries the next one in the pool — no user intervention needed.

#### Pool Policies

| Policy | Behaviour | Use When |
|--------|-----------|----------|
| **Geo-Nearest** | Picks a member in your country first; falls back to the same continent; finally falls back to any. | You want lowest latency and minimal geo-routing surprises. |
| **Round-Robin** | Cycles through members deterministically with a per-region cursor that prevents the same exit IP being picked twice within N rotations. | You want privacy via rotation; rotation interval configurable (default 30 min). |
| **Random** | Uniform random pick on every connect / rotation. | You want variety and don't care about deterministic rotation order. |

User-country detection for Geo-Nearest works without any backend: the app probes its public IP via three plain-HTTPS endpoints (Cloudflare trace, ipify, ifconfig.me), then looks up the country in a bundled MMDB database (db-ip.com's CC BY 4.0 dataset, ~8 MB, refreshed monthly per release). No DNS over external resolvers, no telemetry.

#### Importing a Pool

From the **Configs** screen, tap the **+** button and choose **Add Pool**. The wizard walks you through three steps:

1. **Pick files.** Either a ZIP archive containing many `.conf` / `.ovpn` / `.sswan` configs (most common — your VPN provider distributes one), or pick individual files. ZIP entries are extracted in-memory; large archives are decoded incrementally so a 600-member archive does not need 600 × file-size of free RAM.
2. **Pool settings.** Name the pool, choose policy, and (for Round-Robin) set rotation interval. Defaults: Geo-Nearest, 30 min for Round-Robin.
3. **Import progress.** The wizard shows live progress through Extracting → Parsing → Resolving Locations → Done. Country resolution runs in parallel (8 concurrent DNS lookups) using each member's hostname or filename as a hint.

A soft warning appears for pools with more than 200 members ("imports may be slow on older devices"). There is no hard cap; pools with 600+ members import in ~30-60 seconds on a recent mid-range Android phone.

#### Connecting to a Pool

Two entry points:

- **Connect screen dropdown.** When at least one connection or pool exists, tap the connection name below the Connect button to open a picker that lists single Connections (top section) and Pools (bottom section, divided by a horizontal line). Tap a pool entry to set it active and start connecting.
- **Pool Detail screen.** Tap a pool in the Configs screen to open its detail view, then tap **Use this pool**.

Either path tears down any current tunnel, picks a member according to the pool's policy, and brings up that member's tunnel. The pool indicator card appears above the Connect button showing pool name, active member with country flag, and (for Round-Robin) a countdown to the next rotation.

#### Round-Robin Rotation

Round-Robin pools schedule rotations via `AlarmManager.setExactAndAllowWhileIdle`, the right Doze-aware primitive on Android. Two alarms fire per cycle:

1. **Pre-warm** — 60 s before rotation, the app picks the next member and runs a DNS-only probe. If the probe fails, another candidate is tried (up to 3 attempts). The successful pick is cached for the actual rotation tick.
2. **Rotate** — the actual disconnect-and-reconnect happens here. Re-uses the pre-warmed pick for instant rotation; falls back to a fresh policy pick if pre-warm produced no usable candidate.

**Battery-saver behaviour.** When Android's battery saver is on, rotation interval is automatically doubled (e.g. 5 min → 10 min). The Connect screen shows a small badge so you can tell the rotation is throttled. When battery saver toggles off, the next rotation re-arms at the full interval.

**Doze deep-sleep.** `setExactAndAllowWhileIdle` fires even in Doze, but with system-imposed minimum spacing (~9 min between fires per app). Round-Robin intervals shorter than 9 min may drift in deep sleep; the app's countdown UI reflects the actual scheduled time so you can tell when this is happening.

#### Pool Detail Screen

Tap a pool to see:

- **Pool summary** — name, policy, member count, region restrictions if any.
- **Coverage breakdown** — number of servers and unique countries per region.
- **Active member** + **pending member** (next pick if pre-warmed) badges.
- **Member list** with monospace IDs, country/region per member, an inline warning icon for currently-unreachable members.
- **Reset (N)** button — visible if the pool has unreachable members; one tap clears all flags so the next rotation tries every member afresh.
- **Use this pool** — set as active and connect.
- **Delete pool** — confirmation dialog; cancels rotation alarms if the deleted pool was active.
- **Settings (gear icon, top-right)** — opens an edit sheet to rename the pool, change policy, or change rotation interval. Edits are reflected immediately in the registry; if the pool was actively rotating, the next rotation uses the new interval.

#### Reliability Behaviour You Should Know About

- **Failed members are temporarily skipped.** When a member fails to come up (handshake timeout, DNS failure), the app marks it unreachable for 30 minutes and tries the next candidate. The flag clears automatically after the TTL — no manual intervention needed.
- **Recently-flapping members are deprioritised.** A member that failed in the last 5 minutes is temporarily moved to the back of the picker, even after its unreachable flag was cleared. This avoids repeatedly banging on a borderline server.
- **All-unreachable detection is offline-aware.** If every member is marked unreachable AND the device has no underlying internet (airplane mode, captive portal, dead WiFi), the app keeps the marks instead of resetting them — preventing the pool from churning through 30-min reset cycles during a global outage.
- **Kill Switch + Pool.** When the Kill Switch sinkhole is engaged, pool rotation is paused. Re-enabling the Kill Switch lets the next rotation tick (or a manual reconnect) resume — members are not falsely marked unreachable while KS is blocking them.
- **Manual disconnect cancels rotation.** Tapping disconnect on a pool tears down the tunnel AND cancels the rotation schedule. Tapping the pool again (in the dropdown or Pool Detail) re-arms it.
- **Crash recovery.** Active member, pending member, slot, and per-region cursors are persisted synchronously on every change so a process kill mid-rotation never leaves the UI showing one member while the disk says another.

#### Pool vs Single Connection — When to Use Which

| Use a Pool when… | Use a single Connection when… |
|------------------|-------------------------------|
| Your provider gives you many configs (e.g. a country bundle) | You only have one config |
| You want privacy via IP rotation | You want a stable, predictable IP |
| You want auto-failover if a server is down | You always want the same exit (e.g. Per-App VPN with a specific app whitelisted) |
| You want geo-routing without manual server picking | You're connecting to a personal home VPN gateway |

Both modes coexist: you can have several single connections AND multiple pools registered at the same time. The Connect-screen dropdown lets you switch between them.

### Per-Pool Split Tunnel (Bypass)

Each pool can carry a **bypass-CIDR list** that excludes specific IP ranges from the tunnel for the duration of any pool member's connection. Open the pool from the **Configs** tab → tap the pencil icon on the pool card → find the **Split tunnel (bypass)** section in the bottom sheet.

Two inputs:

- **Exclude private networks** checkbox — adds the standard private-network CIDRs in one tap: RFC1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), IPv4 link-local (`169.254.0.0/16`), IPv6 ULA (`fc00::/7`), and IPv6 link-local (`fe80::/10`).
- **Custom bypass CIDRs** textarea — one CIDR per line, IPv4 and IPv6 mixed freely. Plain IPs accepted as `/32` (v4) or `/128` (v6). The supporting text below counts valid lines and lists invalid ones with a sample.

**Per-protocol mechanics on Android:**

| Protocol | Implementation |
|----------|----------------|
| **WireGuard** | `AllowedIPs` is rewritten as the COMPLEMENT of the bypass set against the original AllowedIPs. The patched config drives `VpnService.Builder.addRoute()` via the upstream config-parser code path. If the original AllowedIPs is NOT full-tunnel (no `0.0.0.0/0` and no `::/0`), the bypass is disabled with a log warning so already-narrowed routes aren't unpredictably re-layered. |
| **OpenVPN** | `route X.X.X.X mask Y.Y.Y.Y net_gateway` and `route-ipv6 X::/n net_gateway_ipv6` directives prepended to the config. Plus `pull-filter ignore "route X"` lines drop matching server-pushed routes. ics-openvpn's parser recognises both directives. |
| **IPSec/IKEv2** | Not supported. IPSec traffic selectors require server-side cooperation that no commercial VPN provider exposes per-tunnel. The injector logs a warning and leaves IPSec pool members untouched. |

**Examples:**

| Use case | Bypass CIDRs to enter | Effect |
|---|---|---|
| Keep your home LAN reachable while connected | check "Exclude private networks" | All RFC1918 + IPv6 ULA traffic routes via your local network instead of the VPN. Cast to your TV, reach your `192.168.x.x` router admin page, share files with `10.0.x.x` NAS. |
| Bypass one specific service | `203.0.113.42` *(plain IP, becomes /32)* | DNS lookups to that IP still go through the VPN; the actual packets to it skip the tunnel. Useful when an app's backend geo-blocks your exit IP. |
| Keep work VPN subnets direct | `10.10.0.0/16`<br>`172.20.0.0/12` | Both ranges bypass the tunnel; the rest of `10/8` and `172/8` still tunnels (so private addresses you AREN'T using on those subnets remain protected). |
| Bypass IPv6-only services | `2001:db8::/32`<br>`2620:fe::/48` | IPv6 traffic to those ranges is direct; all IPv4 still tunneled. |
| Combo for a streaming app + LAN | check "Exclude private networks"<br>add `198.51.100.0/24` (the streamer's CIDR) | Streamer traffic and LAN traffic bypass; everything else uses the VPN. |

**Pool-aware preserve-on-rotation:** Round-Robin rotates to a new member every interval. The bypass set is re-applied to the new member's config automatically — no re-editing needed. The setting lives on the Pool, not the Member.

**Settings field vs per-pool field:** Android Settings does **not** have a global split-tunnel field — only per-pool. If you want the same bypass list across multiple pools, copy-paste the CIDRs into each pool's edit sheet. (The desktop client has a separate global Routing Mode for that purpose; Android intentionally keeps split tunnel per-pool to match the per-pool DNS Override design.)

**Verifying the bypass works:** with `1.1.1.1/32` in the bypass list and the pool active, run `curl https://1.1.1.1/cdn-cgi/trace` in Termux: the `ip=` line should match your phone's mobile or home IP, **not** the VPN exit. A regular site like `curl https://example.com/cdn-cgi/trace` should still show the VPN exit IP. The split is working when the two outputs differ.

### Home-Screen Widget

A 4×3 home-screen widget mirrors the Connect screen layout with live status. Since v0.9.14.76 the layout is a **two-column header**: a compact 96 dp toggle button on the left, and a vertical status stack (status text + uptime + connection name) on the right — leaving more room for the live-data section below without making the widget itself larger.

Contents:

- **Toggle button** (top-left, 96 dp circle) — tap to connect/disconnect
- **Status text + uptime + connection name** (top-right column)
- **Three protocol pills** — tap to switch protocols without opening the app; the active pill stays in sync with whatever protocol is actually running, including failover-driven changes
- **Two traffic cards** — Download and Upload totals + per-second speed + sparkline history (1 s refresh, ~half the older 2 s tick)
- **VPN-IP row** — your assigned tunnel inner IP (now populated for OpenVPN and IPSec too, not only WireGuard)
- **Endpoint row** — server hostname:port

The widget re-renders on every tunnel status tick (~1 s) via a broadcast-intent bridge from the foreground service. When the Kill Switch sinkhole is engaged, the circle turns red and displays a shield-bad icon with "Kill Switch Active" status.

**Pool-aware label.** When a pool is the active selection, the widget shows the pool name in the connection-name slot (`Pool: Europe Pool` while disconnected, `de-3.example.com · Europe Pool` when connected). Tapping the widget circle while a pool is active fires the pool's pick-and-connect path rather than the single-connection path — the same code that the Connect screen uses, so widget-triggered connects always honour the active pool.

### Quick Settings Tile

Add the **Privycs VPN** tile to Android's Quick Settings panel (swipe down from the top, tap Edit, drag the tile into your active tiles).

Tap the tile for a one-tap toggle without unlocking. The tile reflects the current tunnel state with a coloured icon: teal = connected, grey = disconnected, red = kill-switch active.

### Manual Pause with Auto-Reconnect

Tap **Pause** on the Connect screen to temporarily disconnect with an automatic reconnect timer. Choose from: 5 min, 15 min, 30 min, 1 h, 2 h, 8 h. The timer counts down visibly; tap **Resume Now** any time to cancel the pause.

Pausing works around Android's System Always-On VPN mode: normally the OS would re-establish the tunnel immediately, but the pause timer holds a flag that suppresses the reconnect for the countdown duration.

### QR Code Import

The app uses Google Play Services' Code Scanner — a Google-signed scanner UI runs in a separate process, so Privycs never requests the Camera permission. Your users won't see a "camera access" prompt.

Generate a QR code on your gateway (**Downloads → WireGuard Config → Show QR**) and scan it from the Android app's **Add Connection → Scan QR Code** flow. The config is imported and ready to connect in under 10 seconds.

### Encrypted Local Backup

Export all connections and settings into a single encrypted file for backup or migration between devices. Configure under **Settings → Backup & Restore**.

- **Encryption:** AES-256-GCM with PBKDF2 key derivation (100,000 iterations, SHA-256)
- **Password:** User-chosen, never stored anywhere — if you lose it the backup is unreadable
- **Content:** All VPN connections (configs included), pools (with per-pool DNS-override and split-tunnel settings), app settings (except derived state), and **per-network rules** (added in schema v4, v0.9.14.0)
- **Format:** A plain `.privycs-backup` file you can share via any method (cloud storage, AirDrop, USB)
- **Schema versioning:** v3 (legacy) and v4 (current) are both restorable. A v3 backup imports cleanly with an empty network-rules set; a v4 backup on an older client falls back gracefully and skips the rules block.

Restore on the target device via **Import Backup** and enter the same password. Existing connections with matching names are overwritten.

> Privycs does not sync backups to any cloud — the file is yours alone. For comparison, Android Auto Backup (Google-managed cloud backup) would also work, but it is not end-to-end encrypted and Google can access restored data.

### Autostart on Boot

Enable **System Always-On VPN** under **Settings → System Always-On VPN** to open Android's VPN settings sheet. In the system settings:

1. Tap the gear icon next to **Privycs VPN**
2. Toggle **Always-on VPN** on
3. Optionally enable **Block connections without VPN** for OS-level kill-switch enforcement

Once enabled, Android wakes Privycs with a null-intent on boot completion. Our service recognizes this path and auto-reconnects the most-recently-used connection. If Connect-on-Demand is configured with rules, those rules are evaluated after boot completes.

### Themes

Three theme options under **Settings → Appearance**:
- **System Default** — follows Android's dark-mode setting
- **Light** — always light theme
- **Dark** — always dark theme

All Material 3 colour tokens (primary, surface, error, outline) adapt per theme. The widget ships both light and dark color sets via `values-night/colors.xml`.

---

## Configuration File Formats

### WireGuard (.conf)

Standard WireGuard configuration with `[Interface]` and `[Peer]` sections.

```ini
[Interface]
PrivateKey = YOUR_PRIVATE_KEY
Address = 10.100.113.2/24, fd43:43:45::2/64
DNS = 10.100.10.150

[Peer]
PublicKey = SERVER_PUBLIC_KEY
Endpoint = zerotrust.privycs.com:51823
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
```

When Per-App VPN is configured, the app injects an additional line in the `[Interface]` block before parsing:

```ini
IncludedApplications = com.elba.banking, com.privycs.vpn
```
or
```ini
ExcludedApplications = com.yourbrowser, com.torrentclient
```

### OpenVPN (.ovpn)

Standard OpenVPN configuration as produced by your Privycs gateway. The inlined certificates (`<ca>`, `<cert>`, `<key>`) are stored in Android's credential store. Per-App VPN is applied via the strongSwan-compatible `mAllowedAppsVpn` on the profile.

### IPSec (.sswan)

strongSwan Android profile (JSON) with inline PKCS#12 certificate bundle. The app imports it into strongSwan's local profile store and creates a VpnProfile with:
- `selectedApps` populated from Per-App VPN selections
- `selectedAppsHandling` = `SELECTED_APPS_ONLY` (include) or `SELECTED_APPS_EXCLUDE` (exclude) or `SELECTED_APPS_DISABLE`

Alternatively, Apple `.mobileconfig` profiles are NOT supported on Android (iOS-only format). Use `.sswan` instead.

---

## Permissions Explained

The app requests only the minimum permissions required:

| Permission | Why | When Prompted |
|------------|-----|---------------|
| `BIND_VPN_SERVICE` | Required to create the tun interface | First connect |
| `POST_NOTIFICATIONS` | Foreground service notification required by Android 13+ | First launch on Android 13+ |
| `FOREGROUND_SERVICE` + `FOREGROUND_SERVICE_SPECIAL_USE` | Long-running tunnel service classification (Android 14+ requires the `specialUse` subtype for VPN clients) | Implicitly granted |
| `ACCESS_FINE_LOCATION` | Read current WiFi SSID/BSSID for Connect-on-Demand SSID filter and per-network rules | Only when enabling SSID mode "Only these" / "Except these" or adding a Network Rule |
| `ACCESS_WIFI_STATE` + `ACCESS_NETWORK_STATE` | Detect transport changes (WiFi ↔ mobile) for Connect-on-Demand and tunnel-health monitoring | Implicitly granted |
| `SCHEDULE_EXACT_ALARM` (user-revocable on Android 12+) and `USE_EXACT_ALARM` (Android 14+, no prompt) | Doze-aware exact alarms for pool round-robin rotation, COD self-restart on task-swipe, and tunnel-health timeouts | Implicitly granted on 13/14+; on 12 the user can revoke `SCHEDULE_EXACT_ALARM` in system settings |
| `RECEIVE_BOOT_COMPLETED` | Restart VPN on boot when Always-On is configured | Implicitly granted |

Notably NOT requested:
- **Camera** — QR code scanning uses Google Play Services' own scanner which runs in a separate GMS process
- **Contacts / Phone / Storage** — not needed; configs are imported via Storage Access Framework, no broad file-system access

---

## Troubleshooting

### Tunnel won't connect (WireGuard)

Check the **Logs** screen (Settings → View Logs). Common causes:
- Wrong server endpoint or firewall blocking UDP 51820
- Clock skew on the device > 10 minutes (WireGuard uses timestamp in handshake)
- Mobile carrier blocking UDP traffic (some carriers require TCP — use OpenVPN as alternative)

### Tunnel won't connect (IPSec)

Check logs for charon errors. Common causes:
- Certificate chain not trusted — re-import the `.sswan` file from your gateway
- IKEv2 blocked by corporate firewall — fallback to OpenVPN over TCP 443
- Time-of-day issues with certificates — verify device clock

### Per-App Include mode: tunnel doesn't come up

If your only included app is a single app and the tunnel never establishes, this is almost always the "VPN client not in the allow-list" bug — fixed in v0.9.10.0. The app now automatically adds `com.privycs.vpn` to every Include-list. If you're on an older version, add the Privycs VPN app itself to your include selection manually, or update to v0.9.10.0+.

### Kill Switch UI shows "Active" but traffic is flowing

Fixed in v0.9.10.0. The sinkhole fd now re-establishes when a fresh non-VPN network appears (e.g. airplane-mode-off → WiFi reconnect) to reassert its routes against the kernel's new default-route precedence.

### Widget shows stale traffic stats

Tap the widget circle once — it forces a status refresh. If it stays stale, the home launcher may have throttled the widget; remove and re-add it from the widget picker.

### OpenVPN binary missing on some devices

ics-openvpn ships the native OpenVPN binary as `libovpnexec.so` inside the APK. If the device's architecture isn't in the ABI list (ARMv7, ARMv8, x86_64), the OpenVPN tunnel won't start. The app auto-falls back to WireGuard or IPSec if configured on the same connection.

### Connection drops after screen-off

Some device vendors (Samsung One UI, Xiaomi MIUI, Huawei EMUI, Oppo, Vivo, OxygenOS pre-11) aggressively kill VPN services when the screen is off. Layered defenses to enable, in order of impact:

1. **Settings → Connect on Demand → Keep monitor alive** — pins a low-priority foreground notification and keeps the network callback firing during Doze (v0.9.14.75+).
2. **Settings → Battery → Battery optimization → Privycs VPN → Don't optimize** — exempts Privycs from Doze-driven CPU throttling.
3. **Xiaomi only:** open the Security app → Permissions → Auto-start → enable Privycs VPN. MIUI's auto-start gate is *separate* from battery optimization.
4. **System Always-On VPN** (Settings → Network → VPN → Privycs → gear icon → Always-on VPN).

Even without these, v0.9.14.77's `onTaskRemoved` self-restart alarm pulls the service back ~5 s after a swipe-from-recents kill — but the alarm depends on the same Doze-bypass primitive that battery-optimization-whitelisting helps along.

### On-Demand doesn't react to WiFi ↔ mobile handover

Fixed in v0.9.14.71 / v0.9.14.74. Older builds registered the network callback against the *system default network*, which becomes the VPN's own utun once the tunnel is up — masking the underlying transport change. The callback now requests `NET_CAPABILITY_NOT_VPN`, so the underlying WiFi-to-mobile (or mobile-to-WiFi) switch is seen and your COD rules are re-evaluated within seconds.

### Per-App VPN picker doesn't show Android Auto / Maps / Play Store

Fixed in v0.9.14.75. Earlier builds filtered out apps with the `FLAG_SYSTEM` bit, which excludes vendor-shipped apps like Android Auto. The picker now lists every installed app that holds `INTERNET` permission, system or not.

---

## Data Storage

The app stores all data in Android's sandboxed app-private directory. No data leaves the device except through the VPN tunnel itself.

| Location | Contents |
|----------|----------|
| `/data/data/com.privycs.vpn/databases/` | strongSwan profile SQLite DB (IPSec only) |
| `/data/data/com.privycs.vpn/files/datastore/settings.preferences_pb` | App settings (kill-switch, COD, theme) |
| `/data/data/com.privycs.vpn/shared_prefs/split_tunnel.xml` | Per-App VPN selection |
| `/data/data/com.privycs.vpn/files/connections.json` | All saved connections and protocol configs |
| Android KeyStore | WireGuard private keys, OpenVPN certificate passphrase |

All files use app-private permissions (read/write owner only, protected by Android's sandbox model). Uninstalling the app removes everything except the Android system's VPN-profile reference (cleared via *Settings → Network → VPN → Privycs VPN → Forget*).

---

## Privacy

- **No telemetry.** The app does not send crash reports, usage analytics, or telemetry to Privycs or any third party.
- **No cloud dependency.** All connection configs stay on your device unless you explicitly export a backup.
- **Open source.** The complete source code is at [github.com/hoep/privycs-vpn](https://github.com/hoep/privycs-vpn) (Apache 2.0 license for our code; GPL-2 / AGPL-3 for bundled OpenVPN and strongSwan).
- **No Google Play Services required.** The QR scanner uses GMS if available (handled cleanly when absent). WebGL, FCM, Analytics, Crashlytics — none used.

---

## Version Compatibility

| Privycs VPN Android Version | Min Android | Target Android | Architectures |
|-----------------------------|-------------|----------------|---------------|
| v0.9.10.0 – v0.9.15.78 | 8.0 (API 26) | 15 (API 35) | ARMv8, ARMv7, x86_64 |

Older Android versions (below 8.0) are not supported due to Jetpack Compose requirements and Android's VPN permission model evolution.

---

## What's New

The Android Client shares its version-space with the Desktop Client. Tags `v0.9.10.X` cover both — the Android side has been feature-stable since v0.9.10.0 while the Desktop client caught up on parity through v0.9.10.13–32.

Recent Android-client releases:

- **v1.0.5.25** — **Master toggle OFF is now strictly manual — four further leaks closed, plus DNS-test shows which resolver was used.** User-reported follow-up to v1.0.5.22: with the Auto-tunnel master toggle OFF, tapping Disconnect on the Connect screen still re-established the tunnel within roughly one second. v1.0.5.22 had gated the service-side reconnect paths (handleAlwaysOnReconnect / AutoTunnelWorker / PoolKeepaliveWatcher) but four additional client-side paths still bypassed the master toggle and produced visible auto-reconnects. v1.0.5.25 closes all four: **(1)** the Connect button's disconnect handler does a 400 ms-delayed re-evaluation and fires `vpnManager.connect()` if the rules still match — this was the exact path the user hit; gated only on `hasRules`, now also on `networkRulesEnabled`; **(2)** TunnelHealthMonitor.triggerRecovery() (auto-recovery on detected tunnel death) gets a new GATE 3 — was gated on USER-source skip and a 30 s manual-cooldown but no master-toggle, so a server-side disconnect with master OFF still ran a fresh requestConnect; **(3)** VpnPauseTimer expiry-resume — after a user pause expired the timer auto-fired a COD reconnect gated only on `hasRules`; **(4)** NetworkMonitor.runEvaluation() — the existing master-OFF early return bailed without updating `_networkState.value`, so every downstream reader (the Connect-button orchestration above, the pause-timer, the post-sinkhole COD resume) saw a stale `shouldConnect=true` from the last evaluation while master was ON. Now writes a fresh `shouldConnect=false / ruleMatch="Auto-tunnel master OFF"` on the OFF branch so the stale leak is impossible. Plus a **DNS-test UI enhancement** also requested by the user: the Test DNS button in Settings now appends the resolver in use to the success line — *"cloudflare.com → 104.16.132.229 (15 ms · via Quad9)"*. Label resolution: empty override + VPN connected → "VPN DNS"; empty override + VPN disconnected → "System DNS"; known preset → preset label; custom IP → "Custom &lt;first-ip&gt;". 4 new locale keys × 6 languages (en/de/es/fr/it/pt). Shipped in lockstep with Desktop v1.0.5.25 which fixes the parallel `handleSystemDidWake()` leak on macOS.
- **v1.0.7** — **Anonymous crash reporting via self-hosted Bugsink — opt-in, redacted, never to third parties.** Closes the "open test would be blind" gap from the 2026-05-21 production-readiness audit. Shipped in lockstep with Desktop v1.0.7. **Client side:** `io.sentry:sentry-android` v7.14 with NDK crash capture enabled (covers libcharon, libopenvpn3, wireguard-android native libs). Init in `PrivycsApp.onCreate` is gated on a new persistent `crashReportsEnabled` flag in `AppSettings` (DataStore booleanPreferencesKey) — default OFF. Wired to a Compose toggle in Settings → Diagnostics; description copy "*Help improve Privycs by sending anonymous crash reports. SSIDs, API keys, IPs and file paths are redacted on-device before upload to our self-hosted backend.*" An anonymous install UUID (persisted in a dedicated SharedPreferences file, regenerated only on app uninstall + reinstall) serves as `event.User.id` — never linked to API key, Pro license, Play Billing account, or any other identity. **PII redaction pipeline in `util/CrashReporter.kt` runs on every outgoing event** before it leaves the device: SSIDs stripped (catches WifiInfo `SSID=...` framework logs), 32+ char base64/hex tokens replaced, public IPv4 stripped (RFC1918 / loopback kept), global-unicast IPv6 stripped (ULA + link-local kept), Android user-data paths `/data/data/<pkg>/` and `/data/user/N/<pkg>/` replaced with `/data/<app>`, HTTP request bodies nil'd, modules list wiped, navigation breadcrumbs dropped. Session replay explicitly disabled. Toggle flip wirkt sofort: opt-out swaps the global Sentry hub for `NoOpHub` and any queued events are dropped. Live observer on `settingsFlow` so a user can opt in/out at runtime without restarting the app. AAB size +~600 KB even when OFF (transport + redaction code paths). Same English copy as the Desktop toggle. 2 new string resources × 6 languages (`settings_crash_reports_label` + `_desc`). Server side: self-hosted Bugsink 2.2.1 at `https://crashes.privycs.com` — Sentry-protocol compatible, single SQLite DB + 4 snappea workers under /opt/bugsink, ~150 MB RAM, systemd-managed.
- **v1.0.5.24** — **Pool rotation no longer snaps the traffic counter back to 0.** User-reported on Android: while a pool was active and a member rotation kicked in, the upload/download counter visibly dropped to "0 B / 0 B" before the new tunnel's counter started climbing. Root cause: `VpnServiceManager.update()` took the underlying tunnel poller's raw `rxBytes`/`txBytes` at face value, and on rotate-out the new member's fresh tunnel session starts at 0 — losing every byte the prior member transferred. v1.0.5.24 introduces a pool-baseline accumulator: any backwards jump in the raw counter while the same pool stays active adds the previous raw value to a baseline; the displayed counter is always raw + baseline, so it keeps climbing across rotations. Reset to 0 on pool teardown (`activePoolId` cleared) so a fresh pool session starts clean. Single-connection path (no pool active) is bypassed entirely — the tunnel poller remains authoritative there. The SpeedTracker sparkline also gets fed the post-baseline display values so the speed curve no longer registers a negative-delta clamp event at rotation. Shipped in lockstep with Desktop v1.0.5.24 which fixes two adjacent counter bugs on macOS-IPSec (sticky-max against transient zero reads, and session-baseline reset on connection/protocol switch).
- **v1.0.5.23** — **Live engine-decision card at the bottom of the On-Demand & Network Rules screen.** Replaces the prior static "default behaviour" explainer block with a dynamic card that, in the user's own language, narrates what the rule engine is deciding right now against the current network — e.g. *"Connected to Wi-Fi 'Hoep@Home' · Auto-tunnel: Off → Manual control only — engine takes no action"*. Three decision branches render: master toggle OFF surfaces "Manual control only" (matches the v1.0.5.22 manual-only semantics); master ON with a matching rule shows "A rule matches the current network — engine is acting"; master ON with no match shows "No rule matches — engine takes no action". The Composable observes `NetworkMonitor.networkState` (which the existing eval loop already publishes) plus the settings flow for the master-toggle bit — no engine surgery, fully reactive. 11 new locale keys × 6 languages (en/de/es/fr/it/pt). Same change shipped in lockstep with Desktop v1.0.5.23.
- **v1.0.5.22** — **Master toggle OFF = strictly manual.** User-reported that switching the Auto-tunnel master toggle to OFF didn't reliably yield manual-only control — silent reconnects still happened from the system Always-On restart path, the 15-minute WorkManager backstop tick, and the pool-keepalive watcher. The master toggle was honoured by the *live* rule evaluator but ignored by every *fallback* reconnect entry point, so OFF behaved more like "rules paused" than "manual-only". v1.0.5.22 inserts a single early-return master-OFF gate at the top of each of these paths (`handleAlwaysOnReconnect` stops the foreground service entirely; `AutoTunnelWorker.doWork` and `PoolKeepaliveWatcher.tryReconnectPool` skip the rule re-eval). Effect: with master OFF, the only thing that can change tunnel state is the user pressing Connect / Disconnect. Same gate shipped on Desktop in lockstep.
- **v1.0.5.17** — **Connections-list badges show the endpoint host instead of the protocol name.** The per-protocol chips under each profile entry on the Connections screen now show the server hostname (port stripped) instead of the static "WireGuard"/"AmneziaWG"/"OpenVPN"/"IPSec" text — the brand logo + colour already convey the protocol, so the chip text is now used for the more informative endpoint. Scope is connections-list only: Connect-screen badges and the gateway-browser panel deliberately keep the prior logo-only look. Same change shipped in lockstep with Desktop v1.0.5.17.
- **v1.0.5.10** — **Master toggle now applies immediately.** Flipping the Auto-tunnel master toggle (top of the On-Demand & Network Rules screen) from OFF to ON now triggers the rule engine to evaluate the current network on the spot, instead of waiting for the next spontaneous network event (up to 10 seconds via the backstop tick). Same for the other direction: any change to the master toggle, the rule list (add/delete/reorder/edit), or the rule-engine state immediately re-runs the rule evaluation. The user-perceived bug "I just turned Auto-tunnel on and nothing happened for several seconds" is gone.
- **v1.0.5.9** — **Sub-second reaction time for Wi-Fi rules — the 5-10s "VPN is still up on my home Wi-Fi" lag is gone.** A new `WifiManager.NETWORK_STATE_CHANGED_ACTION` broadcast receiver lands the Wi-Fi-association event ~100-300 ms after the OS attaches to a new SSID — well before the higher-level ConnectivityManager.NetworkCallback fires on throttling OEMs (Samsung One UI, Xiaomi MIUI, Oppo ColorOS routinely defer NetworkCallback delivery by 5-8 seconds for power-saving). The broadcast carries the SSID in `EXTRA_WIFI_INFO` so it can be latched immediately without waiting for the separate `onCapabilitiesChanged` callback. Effect on rule-driven disconnect: joining a Wi-Fi covered by an "except this SSID" rule (e.g. trusted home Wi-Fi) now tears the tunnel down within ~500-800 ms of association instead of the previous 5-10 s. The existing ConnectivityManager.NetworkCallback path remains as a complementary trigger for cellular / VPN / link-property events; the two paths are additive (whichever fires first wins, duplicates collapse via the conflated eval pipeline). No new permissions — the existing ACCESS_WIFI_STATE declaration is sufficient.
- **v1.0.5.8** — Build hotfix: an unescaped apostrophe in the English `settings_perm_battery_body` string from v1.0.5.6 broke the resource compiler with a misleading "Invalid unicode escape sequence" error and aborted three consecutive CI runs (v1.0.5.6, v1.0.5.7, v1.0.5.8). Fixed by rewording "don't" to "do not" — eliminates the escape syntax entirely. Audited all six locale files for similar issues with a char-by-char Python check; zero further problems found. Follow-up: a pre-commit hook running `./gradlew mergeDebugResources` locally will be added in v1.0.6 so this can't recur.
- **v1.0.5.7** — Build hotfix: escaped the inner double quotes around the system-dialog option name ("Allow all the time") in `settings_perm_location_bg_body` across all six locales. The Android resource compiler treats unescaped `"` inside a `<string>` content as the start of a quoted span, which broke parsing.
- **v1.0.5.6** — **In-app permission recovery.** Closes the UX hole where a user who declined a permission on first launch had no in-app way to retry — the only path was navigating manually to the OS-level Settings → Apps → Privycs → Permissions screen. A new "Permissions" section now appears at the top of the in-app Settings screen and shows one card per missing runtime permission (notifications, fine + coarse location, Nearby Wi-Fi devices on Android 13+, background location on Android 10+, battery-optimisation exemption). Each card has a clear "Grant permission" button that re-launches the appropriate runtime request, with the Android 11+ background-location case routing to the system app-info screen as that OS no longer surfaces a runtime dialog for it. Cards disappear as soon as the corresponding permission is granted, and the whole section hides entirely when nothing is missing. Visibility is conditioned per permission (e.g. SSID-relevant cards only show when network rules are enabled), so users who don't use Auto-tunnel features aren't shown irrelevant prompts. All copy translated into the six supported in-app languages.
- **v1.0.5.5** — **Closed-Testing preparation: full-feature build with `ACCESS_BACKGROUND_LOCATION` re-enabled.** The open-test build (v0.9.15.75 and onwards) shipped without background location to defer Google Play's background-location declaration form and demo-video review gate. That trade-off silently degraded SSID-based Connect-on-Demand rules to foreground-only — once the app was backgrounded, the OS redacted the Wi-Fi name and rules with SSID conditions could no longer match, so the tunnel stayed in whatever state the user had last left it. v1.0.5.5 brings background location back so SSID rules react reliably even when the app is closed or the screen is off, restoring the full designed behaviour. The runtime permission flow walks through foreground-grant first, then a separate background-grant dialog after a prominent in-app rationale — both are decline-able and the engine continues to function (degraded) without them. Closed Testing promotion follows once Google's three sensitive-permission declarations (background location + foreground-service-special-use vpn + QUERY_ALL_PACKAGES for split-tunnel) clear review, submitted together in one demo-video bundle.
- **v1.0.5.4** — **Closed-Testing readiness: Play Billing UI hidden until launch.** In preparation for promoting the app from internal testing to a closed test, the "Unlock Pro — €9.99" and "Restore purchase" buttons on the Pro upgrade screen are now hidden behind a compile-time flag and replaced with a short notice that direct purchase opens with the public launch. Why: the managed product `privycs_pro_lifetime` is not yet configured in Play Console, so the buttons would otherwise fire onto a non-existent product and the Play Billing client would return ITEM_UNAVAILABLE. License-key activation (PRVC-… bundle redemption from email or `.privycs-license` file) stays fully visible and usable so cross-platform-bundle holders can still activate Pro on Android during the test phase. All Pro features remain unlocked for testers as before (GATING_ENABLED still off).
- **v1.0.5.3** — **Connect-then-disconnect flicker on every app open: fixed at the source.** v1.0.5.2 gated the four backstop reconnect paths but missed the live rule evaluator itself. The actual symptom on app open was a different bug: on cold launch the system delivers the current Wi-Fi name (SSID) asynchronously through a callback that lands ~1-3 seconds after the rules engine first runs. The rules engine, seeing an empty SSID, did not match an *except-this-Wi-Fi* rule and instead matched a broader *connect-on-WiFi* rule, briefly bringing the tunnel up — then the SSID arrived, the except-rule started matching, and the tunnel went down again. The live evaluator now skips entirely whenever the current network is Wi-Fi, the SSID is not yet resolved, and at least one SSID-matching rule exists; the next callback (1-3 s later) re-evaluates with the resolved SSID and the right rule wins on the first try. No more flicker.
- **v1.0.5.2** — **No more connect-then-disconnect flicker after an app update.** A handful of users reported that the VPN would briefly come up on its own after a Play-Store update, then drop again 3-5 seconds later — visible "connecting → connected → disconnecting" cycling without any user action. Root cause: Android's system-level Always-On VPN feature force-restarts the app's VPN service after every update, and the post-restart reconnect path used the last-known active connection without first asking the rule engine whether the current network actually permits an auto-connect. Three internal backstop reconnect paths (system-restart, the 15-minute WorkManager backstop tick, and the network-restore callback) now all consult the same "should I connect right now?" check that BootReceiver already used — when Auto-tunnel is on and the current network matches an *except* rule (e.g. trusted home Wi-Fi), the reconnect is skipped instead of fired-and-immediately-undone.
- **v1.0.5.1** — Visual fix: the subtitle text on the Auto-tunnel rules engine master toggle was rendered in light gray on the primary-tinted card background and was effectively unreadable. The subtitle now uses the Material-standard pairing — the container's "on" colour at 75 % alpha — so it stays readable in both the on (tinted) and off (neutral) states. No functional changes; pairs with the same fix on the desktop client.
- **v1.0.5** — Two changes. **(1) On-Demand & Network Rules screen — prominent master toggle.** Opens with a primary-coloured master toggle pinned at the top: turn off to disable all auto-tunnel behaviour at once (rules below are short-circuited; you control the tunnel manually from the Connect screen), turn on to let the rule list below run as before. A small static "default behaviour" card at the bottom explains that when no rule matches the current network the engine takes no action. Mirrors the Desktop client's layout one-to-one. **(2) Hardened in-app translations.** The red IPv6-leak warning that occasionally appears under the Connect button is now translated into all six in-app languages (was English-only). Same fix applied to the six other in-app red banners that the connect coordinator can raise (no connection selected, no config for protocol, connection rejected, pool connect rejected, warning prefix, kill switch active) — every red-banner string now resolves through the locale resources instead of being a hard-coded English literal in the service layer.

- **v1.0.4** — No Android-side code change in this tag; the Android build of v1.0.4 reuses the v1.0.3 Android APK/AAB. The release exists to ship a Windows IPSec fix on the desktop side; see the desktop release notes if you also run Privycs on Windows.
- **v1.0.3** — Two small fixes. **(1) Play-Store compliance.** Removed the standalone "available at privycs.com" mention from the cross-platform-bundle hint on the Pro screen, since Google Play's anti-steering rule forbids pointing app users to an external storefront. The bundle remains exactly as functional as before — you still activate it inside the app under *Settings → Pro → "I already have a license key"* by pasting your PRVC-… key or loading the `.privycs-license` file from the purchase email. **(2) Bundled with the desktop side's Windows IPSec hardening** in the same tag, no Android-specific behaviour change.
- **v1.0.1** — **Cross-platform bundle activation now works on Android.** If you bought the Privycs Pro cross-platform bundle (Android + Desktop + iOS) you can now redeem it on Android. *Settings → Privycs Pro → "I already have a license key"* opens a dialog that accepts either a pasted `PRVC-…` key or a `.privycs-license` file picked from your device. The signature is verified offline against a public key baked into the app — no account, no phone-home, no online activation. The Play-Billing single-Android purchase path is unchanged and remains the primary way to buy Pro just for Android. Cross-redeem is symmetric: the same bundle key activates Pro on Privycs Desktop (v1.0.0.1+) and will activate on Privycs iOS when that ships.
- **v1.0.0** — **First 1.0 milestone — two additions on Android.** **(1) Italian and Portuguese translations.** The app's in-app language switcher (added in v0.9.15.78) now offers two more languages — Italian and Portuguese — beyond the existing English / German / Spanish / French. Every screen, dialog and notification is translated; bottom-navigation labels were already pinned to a single line and the Italian / Portuguese labels were chosen to fit without wrapping. As before, the language can be set inside the app under *Settings → Appearance → Language*, independently of your device language; on Android 13+ it stays in sync with the system per-app language picker. **(2) Version unification with the desktop client.** The shared version-space jumps to v1.0.0 to align with the new desktop release. Android-specific functionality in v1.0.0 is otherwise identical to v0.9.15.78 — see the desktop release notes for the headline encryption-at-rest, multi-profile fixes and Pro-tier scaffolding work landing on the desktop side of the same tag.
- **v0.9.15.78** — **In-app language switcher.** You can now choose the app's language — English, German, Spanish or French — directly inside the app under *Settings → Appearance → Language*, independently of your device language. It works on every supported Android version (8.0+); on Android 13+ it stays in sync with the system per-app language picker. The bottom-navigation labels were also shortened and pinned to a single line so the translated labels never wrap onto two rows.
- **v0.9.15.77** — **The app is now available in German, Spanish and French.** Every screen, dialog and notification was translated. The language is chosen automatically from your device language; on Android 13 and newer you can additionally set a per-app language — independent of the device language — in *System Settings → Apps → Privycs VPN → Language*.
- **v0.9.15.76** — **Connect-on-Demand reworked into a single rules engine.** The simple Connect-on-Demand settings — the WiFi/Mobile trigger and the only/except SSID list — were converted into real network rules, so there is now one auto-tunnel engine instead of two. Auto-connect reacts continuously to Wi-Fi and mobile-network changes and rule edits take effect immediately, where earlier the detection could need the On-Demand & Network Rules screen to be opened first. Your existing Connect-on-Demand configuration is migrated automatically into equivalent rules on first launch — and again if you restore an older backup — so nothing is lost. With the legacy path gone, every network decision is now an explicit, visible rule in one list.
- **v0.9.15.75** — **Second production-readiness pass — fixes from a deeper code re-audit, plus open-test launch preparation.** **(1) At-rest encryption hardened:** the connection store is now saved atomically (written to a temp file, then renamed into place) so an interrupted write can never leave it half-written, and the encryption key introduced in v0.9.15.74 is now created race-free. **(2) The app can no longer be locked out by a corrupt settings file** — a damaged settings or network-rules store is replaced with defaults instead of crashing the app on every launch. **(3) IPSec traffic counter** no longer stays stuck at 0 / 0 on devices whose ROM does not report per-app traffic statistics; it falls back to the tunnel interface's own byte counters. **(4) Restricted alarm permissions removed:** pool rotation and the service self-restart now use battery-friendly inexact alarms and need no special alarm permission. **(5) Sturdier gateway import:** a single malformed entry in a gateway config list no longer aborts the whole download. **(6) Connect-on-Demand by Wi-Fi name** runs while the app is in the foreground for this open-test build; automatic background connect on joining a known Wi-Fi network returns in a later release.
- **v0.9.15.74** — **Production-readiness pass ahead of open testing — security, privacy and stability hardening.** **(1) Kill Switch** is now honest and more resilient: if Android denies the block-all tunnel, the notification says so plainly instead of falsely claiming "traffic blocked", and the Kill Switch re-engages itself after an aggressive battery-killer terminates the app instead of silently switching off. **(2) No more main-thread stalls (ANR risk):** settings are served from an in-memory cache and the network-rules + pool screens no longer block the UI thread on disk reads. **(3) Open-source licenses:** a new Settings → About → "Open-Source Licenses" screen lists every bundled component with its license and the full GPL/Apache/MIT texts, and links the public source code — Privycs VPN is free software under the GNU GPL v3. **(4) Privacy:** Wi-Fi network names are no longer written to the app's log file; the gateway API key and your saved connection configs (which contain VPN private keys) are now **encrypted at rest** with a hardware-backed Android Keystore key; a Privacy Policy link was added to Settings. **(5) Hardening:** the home-screen widget can no longer be driven by other apps on the device. **(6)** A connection that silently stops passing traffic now raises a notification, so you are never left "connected" but offline without knowing.
- **v0.9.15.73** — **Connect-on-Demand and Network Rules are now one screen.** The simple on-demand settings (WiFi/Mobile trigger + the except/only SSID list) and the per-network Rules engine used to live on two separate screens with no indication of which one wins — in fact Rules are always checked first and the simple settings act as the fallback. They are now unified into a single **"On-Demand & Network Rules"** screen that states the precedence outright: the rule list is checked top to bottom, the first rule that matches the current network wins, and any network no rule matched falls through to a pinned **"Default behaviour"** card at the bottom (the former Connect-on-Demand config, opened with its Edit button). Nothing about how on-demand decisions are made changed — this is purely a UI reorganisation so the rules-first → default-fallback order is visible instead of implied. Cross-platform: the same unification ships on the desktop client in v0.9.15.73.
- **v0.9.15.72** — Fixes the Protocol Failover Order settings shipped in v0.9.15.70/.71: **(1) reordering now actually persists.** The data class held the new list and the UI built the reordered version, but the settings store writes one explicit key per field and that key was missing for protocol failover order — every up/down click silently dropped on save and the screen snapped back to the default. The store now writes and reads `protocol_failover_order` properly (as a comma-separated list of protocol names), and a partial list is completed by appending any protocol the user did not place so future protocol additions still produce a total order. **(2) The protocol icons in this settings section are now brand-coloured** — using the same mono drawable + brand-colour tint as the Connect screen pill row: AmneziaWG in its indigo/purple (#6366F1), WireGuard in dark red, OpenVPN in orange, IPSec in blue. No more washed-out silhouettes.
- **v0.9.15.71** — Build fix for v0.9.15.70 (no behaviour change). The new "Protocol failover order" Settings section referenced the settings repository under the wrong local name in one click handler and the CI build failed before producing an APK; v0.9.15.71 is the same release with that single-identifier rename so the build goes through. All v0.9.15.70 features (callback-driven Wi-Fi name detection + user-configurable protocol failover order) are in this release.
- **v0.9.15.70** — Two improvements to Connect-on-Demand and the connection switcher: **(1) Reliable Wi-Fi name detection.** On Android 12+ the OS withholds the Wi-Fi name from every "polling" API (the methods the app used to call once per check) when the app is in the background — even with all the right location permissions granted. The only reliable source is a system *callback* that delivers the name when the network changes. The on-demand engine now follows exactly that model: it tracks which network is the current Wi-Fi, listens for the system's notification that brings the unredacted name, and *forgets the name the moment the Wi-Fi goes away*. This removes the "SSID detection returned empty, using cached …" noise from the logs and, more importantly, eliminates the case where a stale, previously-cached Wi-Fi name could briefly drive a wrong on-demand decision after you left a network. **(2) User-configurable protocol failover order.** When a single Privycs "connection" holds multiple protocols (e.g. AmneziaWG + WireGuard + an OpenVPN fallback), automatic recovery used to walk them in a fixed order. A new "Protocol failover order" section in Settings now lets you reorder the four protocol classes (AmneziaWG / WireGuard / OpenVPN / IPSec / IKEv2) with up/down arrows; the chosen order drives both health-monitor recovery and the multi-config connect loop, with the default preserving the previous (DPI-evasion-first) behaviour.
- **v0.9.15.68** — A focused reliability pass on Wi-Fi detection for Connect-on-Demand, fixing four root causes found in a full audit of the detection path: **(1)** while a VPN tunnel was up, a freshly-joined Wi-Fi was misclassified as "mobile/none" for the entire 5–30 s captive-portal-validation window, so trusted-Wi-Fi rules reacted late or not at all — the network classifier now keys off the physical transport, not Internet reachability, matching the callback design and reacting at Wi-Fi association time; **(2)** during a Wi-Fi→Wi-Fi or Wi-Fi off→on switch the OS briefly exposes both the old and new network and the code picked "the first" in an unspecified order — it could read or cache the network you just *left*; detection is now deterministic and the remembered-network cache is only refreshed from an unambiguous environment; **(3)** the seven different events that trigger a rule re-evaluation each spawned their own coroutine and could interleave and race on shared state — all triggers now feed a single, ordered, debounced evaluation pipeline (also lighter on battery); **(4)** at device boot, with the Wi-Fi name not yet resolvable, an exception/allow-list rule could briefly auto-connect on a possibly-trusted Wi-Fi — boot is now conservative until the network is identified. Net effect: trusted-Wi-Fi connect/disconnect reacts faster and far more consistently, and the correct network is identified during transitions.
- **v0.9.15.67** — Fixed a display-only glitch in the on-demand event notification. When the Wi-Fi name had to be read from the short-lived cached value (Android briefly hides the live Wi-Fi name from a backgrounded app right at the moment of a Wi-Fi reconnect, even with background-location granted), the one-shot "Auto-disconnected/Auto-connected" notification could show the *previously seen* Wi-Fi network's name instead of the current one — while the live status banner, which refreshes reactively, showed the correct name. The on-demand decision itself was always correct; only the frozen notification text was stale. The notification now labels such a value as the *last-known* Wi-Fi network instead of asserting it as the current one, so it is honest about the uncertainty.
- **v0.9.15.66** — Completes the trusted-Wi-Fi on-demand fix. The underlying reason Wi-Fi-name rules couldn't react while the app was closed is that Android 10+ hides the Wi-Fi network name from a backgrounded app unless it has background-location access. The app now has a first-run setup flow that, after a clear in-app explanation, requests notifications and location/nearby-Wi-Fi, then offers the "Allow all the time" background-location option so Connect-on-Demand Wi-Fi rules keep working with the screen off. Everything is optional and declinable (you can still grant later in Settings); the permission is used **only** to let Android reveal the Wi-Fi name locally for your rules — never for geolocation, and nothing is sent off the device (see the [privacy policy](/docs/android-client-privacy)).
- **v0.9.15.65** — Fixed a long-standing on-demand bug: with a Wi-Fi network in the on-demand *exception* list (do-not-connect / trusted), turning Wi-Fi off (→ mobile, VPN connects correctly) and back on did **not** disconnect the VPN — it stayed connected on the trusted home network. Cause: the remembered Wi-Fi name was discarded the moment the device switched from Wi-Fi to mobile, so on Wi-Fi return the app could no longer tell it was on the trusted network (Android hides the Wi-Fi name from background apps), defaulted to "stay connected", and never applied the exception. The remembered network name is now kept across the mobile leg and only cleared when connectivity is genuinely lost, so the trusted-network exception applies again on return. (Full background reliability — reading the Wi-Fi name while the app is in the background — requires a background-location grant and is being added as a follow-up.)
- **v0.9.15.64** — Notifications are far less intrusive: the event notifications added in v0.9.15.61 could surface up to three notifications for a single Wi-Fi/mobile change (status + a verbose diagnostics one + occasionally a security alert). The verbose per-network diagnostic notification is removed (now strictly opt-in and never auto-shown), and the "Connection events" channel is silent/shade-only by default — so you now get at most one notification per actual on-demand connect/disconnect/failover, plus the rare kill-switch alert. Also fixed the "Location settings" link (and the two App-settings links) in Settings → SSID permissions that could fail to open on some devices/ROMs: they now fall back to the top-level system settings instead of dead-ending. Internally, the first automated unit tests were added (config parsing + the v0.9.15.63 refresh fix) to guard against regressions.
- **v0.9.15.63** — Fixed the connection screen not refreshing after deleting a config: a removed config (and likewise remove-protocol / rename / add changes) stayed on screen until you switched pages and came back. The connection store edits its lists in place and then re-publishes the registry to force a UI refresh, but because the published value was structurally identical to the previous one, the reactive stream conflated it and the screen was never told to redraw. The registry now carries a monotonically-increasing in-memory revision (not persisted) that makes every change a genuinely distinct update, so config add/delete/rename and protocol removal reflect immediately.
- **v0.9.15.61** — Added event notifications, separate from the ongoing status notification. Three channels you configure in Android's per-app notification settings (Settings → Diagnostics → "Notification settings" deep-links there): **Security** (high priority — kill-switch active / traffic blocked), **Connection events** (on-demand auto connect/disconnect and protocol failover), and **On-demand diagnostics** (a low-priority, opt-out verbose log of why a rule did or didn't trigger — useful for diagnosing on-demand behaviour). Notifications never stack or spam: each channel keeps a single, self-replacing entry, so a Wi-Fi/mobile flap can't flood the shade. All posting is permission-safe — denying the notification permission or disabling a channel simply suppresses it.
- **v0.9.15.60** — Fixed an on-demand regression where, after returning from mobile data to Wi-Fi while the tunnel was up, the status stayed on "connected via Mobile" and Wi-Fi/SSID on-demand rules never fired — only toggling Connect-on-Demand off and on recovered it. Root cause: the network-type detection, while a VPN is the system default, fell back to "the first non-VPN network" in an unspecified list order. On a Mobile→Wi-Fi handover the cellular network lingers briefly as the VPN's backup transport and was picked first, so the connection was classified as mobile and the Wi-Fi/SSID rule was never evaluated. The toggle only appeared to fix it because by then the lingering cellular network had been torn down. Detection now collects every non-VPN internet transport currently present and ranks them deterministically (Ethernet → Wi-Fi → mobile), so a present Wi-Fi correctly wins over a lingering cellular link.
- **v0.9.15.58** — Fixed a config-loss bug present since the multi-config refactor (all versions). A connection that already held one WireGuard config silently lost it when a second WireGuard config was added — the user's repro was scanning two QR codes, which both hardcoded the filename `scanned.conf`. The import "update vs. append" decision matched on `(protocol, filename)` alone, so two genuinely different configs sharing a filename (two QR scans, or two manually-imported `wg0.conf` files) were treated as one slot and the second overwrote the first. The filename-fallback now also requires byte-identical config content: same name + different content appends as a new config (the intended "multiple endpoints per connection" behaviour); re-importing the exact same config still updates in place with no duplicate build-up. QR-scanned configs now get a per-endpoint filename instead of the constant `scanned.conf`, and the Connections-screen QR scan attaches the config to the active connection instead of always creating a new one. Same-named additional configs get an auto disambiguating label in the protocol-pill switcher.
- **v0.9.15.57** — Home-screen widgets: the circular connect button is now a byte-for-byte mirror of the in-app Connect button for all four protocols (AmneziaWG, WireGuard, OpenVPN, IPSec) in both connected and disconnected states. AmneziaWG previously used a widget-only full-colour brand badge that skipped the tint pass — it now uses the same tintable monochrome icon and state-driven colouring as the other three protocols, exactly like the Connect screen. The disconnected circle adopts the app's surface fill + outline stroke (was transparent with a different grey), the off-state icon and label use the app's exact `onSurfaceVariant` tone, the label reads "Connect" (was "Disconnected"), and the connected label uses the app's 90 %-white. Both widget sizes (4×3 full and 2×2 compact) are affected.
- **v0.9.15.55** — Final root-cause fix for "OpenVPN → WireGuard/AmneziaWG pill-switch leaves the spinner, nothing happens". The pill-switch is `requestDisconnect` then `requestConnect` on the same VPN service instance. `handleDisconnect` (OpenVPN teardown) ended with an unconditional `stopSelf()`; Android delivered that stop LATE — ~30 ms AFTER the subsequent WireGuard connect had already brought the tunnel up — destroying the live service. The new tunnel's status never reached the coordinator, the UI stayed on the spinner, and the 90 s connect-watchdog eventually fired. Fix: before the terminal `stopSelf`, apply the same guard the VPN-revoke handler already uses — if the coordinator is in `Connecting` (a fresh connect already accepted on this service, i.e. the pill-switch's second half), keep the service alive and let the in-flight connect own the lifecycle. Earlier fixes each peeled a layer (v0.9.15.46 single WG/AWG backend, v0.9.15.47 disconnect-await, v0.9.15.52 ICS-OpenVPN auto-restart-zombie removal); this is the last one — the stopSelf-after-reconnect race, only visible once the zombie that masked it was gone.
- **v0.9.15.52** — Pill-switch OpenVPN → WireGuard/AmneziaWG silently failed on Wi-Fi with "Connection Failed Job Cancelled". Root cause: our own `handleDisconnect` sent a redundant `startService(DISCONNECT_VPN)` intent after the proper AIDL `stopVPN` teardown — ICS-OpenVPN's `OpenVPNService.onStartCommand` has no handler for that action, fell through to "Building configuration..." and returned `START_STICKY`. The service restarted itself and briefly occupied the system VPN slot in "Assuming always on" limbo. When our WireGuard backend brought up its own VpnService within that 100–200 ms window, Android destroyed `PrivycsVpnService` as the "redundant" VpnService and every in-flight coroutine threw `JobCancellationException`. Fix: drop the redundant DISCONNECT_VPN startService — `OpenVpnTunnel.disconnect()` already does AIDL stopVPN + state-callback wait + explicit `stopService()` and that is sufficient. *(v0.9.15.51 carried the same fix but a missing closing brace broke the Kotlin compile — no .51 APK was ever produced; .52 is the first build that actually ships it.)*
- **v0.9.15.50** — Experimental Android-side defense for the post-OpenVPN-disconnect zombie: overlay-patched ICS-OpenVPN's `OpenVPNService` to exit cleanly when Android auto-restarts it with a null intent and no last-connected profile. New `syncOpenvpnJava` Gradle Sync task in `openvpn-lib` so the vendored submodule stays at its upstream pin (the override file lives in our own source tree). The actual fix for the symptom landed in v0.9.15.51.
- **v0.9.15.47** — `OpenVpnTunnel.disconnect()` now waits for the authoritative teardown signal — the ICS-OpenVPN state callback emitting `LEVEL_NOTCONNECTED` — instead of returning the moment the AIDL `stopVPN(false)` call returned. Pre-fix, disconnect returned ~50 ms after AIDL bind while the actual native-process exit, tun-fd close, and `OpenVPNService.onDestroy` then ran asynchronously over the next 500–1500 ms. The race produced the "OpenVPN → IPSec full crash" and "OpenVPN → WireGuard hangs 90 s" symptoms users reported on v0.9.15.46. 8 s timeout, belt-and-braces explicit `stopService` afterwards.
- **v0.9.15.46** — Dropped the `com.wireguard.android:tunnel` Maven dependency. Both vanilla WireGuard and AmneziaWG now route through the vendored `amneziawg-android` GoBackend — AmneziaWG-go is a strict superset of wireguard-go and accepts vanilla configs unchanged (all AWG-specific keys default to `Optional.empty()`). Eliminates the "switching to WireGuard hangs 90 s after another protocol" bug: the Maven AAR's internal `GoBackend$VpnService` manifest entry did not always survive merger, the library's static `CompletableFuture<VpnService>` then never completed and WireGuard bring-up hung. v0.9.15.39 added an explicit manifest declaration but the failure persisted on switch-to-WireGuard after another protocol's lifecycle destroyed the cached future. One backend, one internal VpnService class — asymmetry eliminated structurally.
- **v0.9.15.44** — `awg_off` widget icon: stripped the inner-circle dark fill to match the established Privycs widget icon convention (silhouette opaque, everything else transparent). The widget's own circular background gradient supplies the disconnected-state visual cue.
- **v0.9.15.43** — Pill-switch sequencing in `VpnServiceManager.performSwitch` now waits for the authoritative `ConnectCoordinator.state → Idle` signal instead of `_status.connected` flipping false (our own optimistic-update sets that to false BEFORE native teardown completes, so the wait returned immediately on a still-running teardown). The blind 1500 ms pad we used to need is replaced by the real wait + a 200 ms breather. Plus widget AmneziaWG icons polished.
- **v0.9.15.39** — Explicit manifest declaration for `com.wireguard.android.backend.GoBackend$VpnService` — the Maven AAR ships this service in its own manifest but the manifest-merger did not always propagate it into the final APK on a fresh `PrivycsVpnService` instance (after another protocol's lifecycle destroyed the previous instance). Without the declaration, the WireGuard library's static future never completed and the tunnel appeared to hang at "Requesting to start VpnService". The AmneziaWG vendored submodule's equivalent service entry always merged correctly — which is why AmneziaWG always worked. (Eventually superseded by v0.9.15.46, which routes both protocols through the AmneziaWG submodule entirely.)
- **v0.9.15.38** — Nuclear-teardown stop-intent gating: `handleDisconnect` now sends the OpenVPN / IPSec stop intents only when the respective protocol's tunnel singleton is actually non-null. v0.9.15.33's blanket teardown was sending the OpenVPN stop intent during a WireGuard-only disconnect, accidentally waking `OpenVPNService` via Android's auto-restart of `LAST_CONNECTED_PROFILE` and creating a parallel zombie tunnel. Also clears ics-openvpn's `LAST_CONNECTED_PROFILE` preference before the disconnect intent so the service can't auto-reconnect on its next start-cycle.
- **v0.9.15.36** — Diagnostic-only release for the Windows-AmneziaWG investigation (no Android behavior change). Android-side: failover-on-first-connect gating now respects user-cancellation between attempts. Per the standing "stop after 3 failed guess-fixes" rule — this iteration was specifically for collecting a clean trace to identify the AmneziaWG-on-Windows blocker root cause, which then landed in v0.9.15.42 on the desktop side.
- **v0.9.15.34** — Android CI fix that v0.9.15.31's `SsidPermissionsHelper` introduced (Lint: `LocationManager.isLocationEnabled` requires API 28; our `minSdk` is 26). Gated behind `SDK_INT >= P` with a fallback to `isProviderEnabled` on older Android.
- **v0.9.15.33** — `tunnelMutex` released around the verify-tunnel-traffic phase so a concurrent disconnect doesn't deadlock against verify. Plus blanket "nuclear teardown" on disconnect (later refined in v0.9.15.38) to ensure no zombie tunnel from a prior protocol lingers when the user disconnects via the UI.
- **v0.9.15.31** — On-disk WireGuard/AmneziaWG conf-file paths now use the slot stable-ID (`gw-<protocol>-<configId>`) instead of the sanitized connection name. Pre-fix, multiple gateway-downloaded WireGuard slots on the same connection clobbered each other's on-disk file. Plus a new Settings panel explaining the SSID permissions Privycs needs (`ACCESS_FINE_LOCATION` is required by Android to read the connected SSID for per-network rules) with a proper user-facing rationale.
- **v0.9.15.30** — UUID-based gateway-stable identifier in `ProtocolConfig.id` AND on-disk filename — both now use `gw-<protocol>-<configId>` derived from the gateway's UUID, so config re-imports map cleanly to existing slots. Plus tunnel-health-probe failover threshold tightened from 3×30 s to 3×10 s — a dead tunnel now fails over to the next protocol within ~30 s instead of ~3 min.
- **v0.9.15.29** — Pill-switch teardown reliability: `ConnectCoordinator.markDisconnected` now guards against spurious init-state replays from ics-openvpn during a connecting phase. Plus per-config verify-phase tolerance for slow IKE_SA negotiation (was 1×500 ms poll which killed every IPSec attempt mid-IKE, now ~30 s budget). Plus disambiguation in multi-config protocol-pill labels — when a connection holds two configs of the same protocol type, both pills show their distinguishing nickname / filename.
- **v0.9.15.27** — Widget circular status button re-implemented to match the in-app `ConnectButton` exactly (instead of the flat-tinted approximation v0.9.15.25 shipped). Both the 4×3 main widget and the 2×2 compact widget share the same visual construction now.
- **v0.9.15.25** — Polished AmneziaWG widget icon set, widget protocol-pill stays in sync after pool rotations and failover.
- **v0.9.15.24** — Connect-on-Demand reliability and pool ↔ connection switch mutex. Three fixes: (1) connect/disconnect loop when COD was enabled while connected and the matching rule said "VPN off"; (2) pool→connection switch could race with an in-flight pool rotation alarm; (3) auto-tunnel worker's network-event delivery improved on Doze-deferred OEMs.
- **v0.9.15.6** — Tunnel-state mutation serialised behind a new `tunnelMutex`. Race shapes covered: user-disconnect during `proto.Up`'s long-running native call; rapid Connect re-taps; NetworkMonitor recovery + user-disconnect concurrency; `SelectConfig` mid-connect. All paths that touch a tunnel singleton (`handleConnect`, `handleDisconnect`, `forceTeardownAfterSinkhole`, pool failure teardowns) now hold the mutex during the long-running native operation.
- **v0.9.15.4** — **AmneziaWG promoted to a first-class 4th protocol** (was a runtime variant of WireGuard). Separate import-time content detection — `Jc`, `Jmin`, `Jmax`, `S1-S4`, `H1-H4`, `I1-I5` obfuscation keys flag a config as AmneziaWG at save time, no more runtime detection per connect. Separate brand icon in pills and lists, slot in the protocol enum ahead of WireGuard, and entry in the failover preference order. Plus **multi-config-per-protocol-per-connection**: a single connection can now hold any number of `ProtocolConfig` entries, including multiples of the same protocol type (e.g. WireGuard-UDP + WireGuard-TCP endpoints to the same server). Failover walks the full ordered-configs list, not just the protocol-type list — a restrictive-network user can configure AWG + WG-UDP + WG-TCP + OVPN-UDP + OVPN-TCP + IPSec on one "Home Server" and recovery tries all six in sequence. Pools remain separate from this — Pools are for VPN providers (many endpoints per provider); multi-config is for one own server with multiple ways to reach it.
- **v0.9.14.96** — **IPv6 leak killswitch** (always-on, no setting). On a v4-only tunnel, the OS native IPv6 default route would normally route AAAA-resolved traffic OUT of the VPN — a critical security bug. v0.9.14.96 fixes it for all three protocols: WireGuard appends `::/0` to AllowedIPs, OpenVPN gets `route-ipv6 ::/0 + redirect-gateway ipv6`, IPSec gets `::/0` injected into the `.sswan` `remote_ts` (best-effort — server may narrow during IKE_AUTH). Idempotent — configs already covering v6 pass through unchanged. **Plus**: Android-IPSec-specific post-connect leak-detection runs 5 s after connect and surfaces a UI banner if the server narrowed our v6 selector and traffic would still leak. **Plus**: on-demand desync defense — when ConnectCoordinator's state machine desyncs from VpnServiceManager (e.g. spurious markDisconnected during status glitch), `requestDisconnect` now detects the desync and forces a real disconnect instead of short-circuiting to AlreadyIdle. **Plus**: NetworkMonitor in-process backstop tick 30 s → 10 s, TunnelHealthMonitor ICMP ping 60 s → 20 s — Doze-deferred callback fallback now reacts within ≤10 s, dead-tunnel detection within ~60 s instead of ~3 min.
- **v0.9.14.91** — On-demand desync defense (initial pass) + scroll-area trim on Settings/Add-Connection screens (~40 dp wasted bottom space reclaimed) + macOS autostart de-dup (eliminates ghost Login-Items entries with cached stale exec paths).
- **v0.9.14.87** — Backup-import "Unexpected JSON Token" recovery: `openOutputStream(uri, "wt")` on every export forces SAF-truncate (some providers overwrote without truncating, leaving trailing bytes from prior exports). Plus a brace-counting recovery path on import that trims trailing junk from already-corrupted backup files. Pre-fix corrupt files now load cleanly on .87+.
- **v0.9.14.86** — targetSdk 34 → 35 + native debug symbols bundled (NDK debugSymbolLevel=FULL). Required by Play Console for new submissions since 2025-08-31.
- **v0.9.14.84** — Signed AAB build added to CI alongside APK. Both share the same `privycs-upload.keystore` so Play registers the upload-key cert correctly at first AAB upload.
- **v0.9.14.82** — `markwon:ext-strikethrough` dependency added so R8 release-build doesn't fail on the `StrikeHandler` missing-class reference. v0.9.14.78 had introduced the in-app Help screen with Markwon but the APK release-build failed silently — v0.9.14.82 is the first Android release that actually has the Help screen working.
- **v0.9.14.78** — In-app Help screen via 5th bottom-bar icon. Live-fetches the markdown doc from `www.privycs.com/docs/android-client.md`, renders with Markwon (TextView in AndroidView). Plus tunnel-health pill connected-gate (was sometimes visible after disconnect when the singleton monitor's state-flow had a stale value).
- **v0.9.14.77** — Survive task-swipe-from-recents on aggressive OEMs (Samsung One UI, Xiaomi MIUI, Huawei EMUI, Oppo, Vivo). Manifest now declares `android:stopWithTask="false"` and the VPN service overrides `onTaskRemoved` to schedule a Doze-aware self-restart alarm (`AlarmManager.setExactAndAllowWhileIdle`, +5 s) when the user has Keep-Monitor-Alive on or a tunnel is currently up. Combined with v0.9.14.75's foreground keepalive, this is the closest an unprivileged VPN app can get to "always reactive" without forcing battery-optimization whitelisting.
- **v0.9.14.76** — Widget v6: two-column header layout (compact 96 dp button left, status / uptime / connection-name stack right), bigger live-data section. Triple-layer "{" defense for IPSec endpoint display: parser fix, persisted heal on disk, and a render-time sanitizer in the UI so older corrupt entries can never re-surface.
- **v0.9.14.75** — Per-App VPN picker now includes network-using **system apps** (Android Auto, Google Maps, Play Store, etc.) — earlier `FLAG_SYSTEM` filter dropped them. Opt-in **Foreground Keepalive** (Settings → Connect on Demand → Keep monitor alive) keeps the COD callback firing through Doze in exchange for a small permanent battery line — off by default, persists across encrypted backups.
- **v0.9.14.74** — On-demand WiFi ↔ mobile handover under an active tunnel: replaced `registerSystemDefaultNetworkCallback` (a hidden `@SystemApi` only callable from system apps despite being in compileSdk-stub) with `registerNetworkCallback(NetworkRequest)` filtered on `NET_CAPABILITY_NOT_VPN`, walking `cm.allNetworks` to ignore VPN transports.
- **v0.9.14.71** — On-demand WiFi-to-mobile handover under VPN: five fixes (VPN-aware default-network callback, BSSID/SSID pull on `onCapabilitiesChanged`, COD-rule re-eval on every transport change, race-free fast-path for re-entering a known-trusted network, Recents-survival for the monitor coroutine).
- **v0.9.14.70** — IPSec ConfigParser: line-based extraction was eating the leading `{` of the `.sswan` JSON and crashing later display logic. Switched to proper `kotlinx.serialization` JSON parsing. Widget gained a bigger toggle button + tighter traffic cards.
- **v0.9.14.69** — Traffic readout halved its poll interval (1 s) and dropped the 0-B/s glitch where the first sample after a reconnect briefly showed zero.
- **v0.9.14.68** — VPN inner-IP now displayed for OpenVPN and IPSec on the Configs page (was WireGuard-only). Widget protocol-pill stays in sync with whatever protocol is actually running, including failover rotations.
- **v0.9.14.66** — Multi-protocol failover: when a connection has more than one protocol config, the tunnel-health monitor rotates protocols on persistent failure (e.g. WireGuard → IPSec → OpenVPN). Plus parallel-tunnel zombie cleanup so a stuck old protocol can't block the new one.
- **v0.9.14.60** — IPSec traffic-counter @Volatile fix: a memory-visibility race was leaking the per-UID lifetime byte counter as the session counter, which is why some users saw "Traffic: 1.2 GB" the moment they connected to a fresh tunnel.
- **v0.9.14.55** — Connection name preserved when adding additional protocol configs to an existing connection (previously it silently took the new file's stem name).
- **v0.9.14.46** — Post-quantum IPSec wired up: RFC 8784 PPK plumbing (`VpnProfile.setPPKId` / `setPPKPsk`) consumed transparently when the gateway emits `pq_safe=true` in the `.sswan`. Plus a build-time strongSwan vendor-patch pipeline so PPK support survives strongSwan upstream rebases.
- **v0.9.14.45** — Tunnel-health pill + auto-recovery for **single connections** (was pool-only before). Surface visible on Connect screen + Settings; recovery loop reconnects the same tunnel on `Dead` state.
- **v0.9.14.4 – v0.9.14.14** — DNS preset picker (Cloudflare, Quad9, NextDNS, AdGuard, Google) with brand-coloured badges in the dropdown. Available globally (Settings), per-pool, and per-single-connection. Test button probes the selected resolver over the tunnel.
- **v0.9.14.0** — Backup schema **v4**: per-network rules now ride along in encrypted backups. v3 backups still restore cleanly with an empty rules set.
- **v0.9.13.0 – v0.9.13.1** — **Per-Network Rules** engine (Settings → Network Rules) with SSID-, BSSID-, and Mobile-key matching and four action types (disconnect / connect / use-connection / use-pool). Includes "From current network" prefill button and BSSID-trust to defend against rogue/evil-twin SSID spoofing. Tunnel-health pill made visible on Settings + Connect screens.
- **v0.9.12.0** — **Tunnel Health Monitor** (Phase 1): WorkManager-backed auto-tunnel backstop and passive health detection (Off / Auto / Active modes) keyed off WireGuard handshake age, IPSec SA-INSTALLED, and OpenVPN counter delta. Ticks every 30 s while connected.
- **v0.9.11.70** — Per-single-connection DNS Override (was global + per-pool only).
- **v0.9.11.68** — DNS-Override Tier 1+2: validation, presets, Test button, per-pool override, Private-DNS hint banner.
- **v0.9.11.55** — Per-pool client-side split tunnel (WireGuard + OpenVPN, IPv4 + IPv6).
- **v0.9.11.51 – v0.9.11.52** — Pool detail screen: scrollable, bigger member rows with flag + city + country.
- **v0.9.11.46** — Pool implementation hardening: 18 audit findings closed across the pool data, service, and UI layers. Critical writes (active member, pending member, region cursors) are now persisted synchronously to survive crash mid-rotation. Rotation alarms carry a sequence counter so manual + scheduled rotations can no longer race. Kill-Switch + pool no longer falsely marks members unreachable. Recently-flapping members are soft-deprioritised. Battery-saver state changes re-arm the schedule live. SelfIpDetector invalidates its country cache on network change so Geo-Nearest follows your real location across WiFi-mobile handoffs. Many smaller fixes (bigger country-region map, stricter IP literal check, lower DNS concurrency on import, safer cursor wrap on member deletion).
- **v0.9.11.45** — Pool connect actually works end-to-end. Four bugs blocking the connect path: `PoolTunnelOps.bringUp()` raced `handleConnect`'s tunnel setup; `VpnServiceManager.connect()` ignored pool selection (Connect button connected the wrong target); the Connect-screen dropdown skipped re-tap on the same pool (no retry path); `handleDisconnect()` didn't cancel pool alarms (rotation undid disconnect 30 min later).
- **v0.9.11.40 – v0.9.11.44** — Initial Pool feature port from Desktop with full UI wiring: Add Pool wizard with ZIP import, Pool Detail screen with edit sheet, Connect-screen pool indicator card with rotation countdown, dropdown selector listing pools alongside connections, pool-aware widget. Bundled MMDB country database (db-ip CC BY 4.0) for offline Geo-Nearest. Round-Robin scheduler via `AlarmManager.setExactAndAllowWhileIdle`.
- **v0.9.10.32** — Version bump aligning with Desktop (no Android-side functional changes; Desktop gained a Connect-on-Demand pre-connect warning dialog, see [Desktop Client](desktop-client.md))
- **v0.9.10.13–31** — Desktop catch-up on Android-parity (hardcore Kill Switch ported, Connect-on-Demand overhaul, manual Pause). Android-side: bug fixes only — no new feature surfaces
- **v0.9.10.0** — Widget redesign mirroring the Connect screen with Kill-Switch state; Per-App VPN now works for WireGuard; Include-mode safely self-includes the VPN client so the tunnel always comes up; Kill Switch sinkhole re-establishes after airplane-mode recovery; Settings persistence survives composition-scope cancellation; Routing Mode and redundant Always-On toggles removed (Connect-on-Demand is the single source of auto-connect truth)
- **v0.9.9.0** — Real Kill Switch via sinkhole `VpnService` — blocks traffic at the kernel route level if the tunnel drops
- **v0.9.8.0** — Split Tunnel renamed to Per-App VPN and extended to IPSec
- **v0.9.7.0** — Manual pause-with-auto-reconnect feature
- **v0.9.3.12** — Widget and Quick-Settings-Tile overhaul for Android 12+
- **v0.9.3.0** — QR code scanner on Android

Complete changelog and release notes on the [GitHub Releases page](https://github.com/hoep/privycs-vpn/releases).
