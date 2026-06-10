# Desktop VPN Client

[Back to Index](README.md) | [Android Client](android-client.md) | [Connect Client](connect-guide.md)

The Privycs VPN Desktop Client is a cross-platform application for Windows, macOS, and Linux that connects to your Privycs VPN infrastructure. It supports WireGuard, OpenVPN, and IPSec/IKEv2 protocols with a single unified interface.

---

## What is the Desktop Client?

The Desktop Client replaces standalone VPN tools (WireGuard app, OpenVPN Connect, strongSwan) with a single application that:

- Imports `.conf` (WireGuard), `.ovpn` (OpenVPN), and `.sswan` (IPSec) configuration files
- Switches between protocols without reconnecting, and switches between saved connections with one click (auto-reconnect)
- Manages multiple VPN connections (home, office, travel)
- Shows real-time transfer statistics with live download / upload sparkline history
- Scans QR codes to import WireGuard configs from your gateway
- Runs in the system tray for always-on connectivity
- Auto-connects on app start or based on network rules (Connect-on-Demand with SSID filtering)
- Warns before connect when Connect-on-Demand rules would tear the tunnel back down
- Blocks all traffic with a Hardcore Kill Switch when the tunnel drops — cross-platform sinkhole driver (Linux iptables / macOS pf / Windows WFP) ported from the Android client
- Manual Pause with auto-reconnect (1 min / 3 min / 5 min / 15 min / 1 h / 4 h) for short interruptions without losing your VPN selection
- Exports all connections + settings as an encrypted local backup (AES-256-GCM + PBKDF2)
- Runs privileged operations through a background helper service — the UI never requires sudo or admin prompts after first install
- Supports dark, light, and system-default themes with per-protocol brand-colour pills

---

## Installation

### Windows

1. Download **privycs-vpn-windows-amd64-setup.exe** from your Privycs gateway downloads page
2. Run the installer (requires Administrator privileges for the VPN driver)
3. Follow the setup wizard -- it installs the application and required WebView2 runtime
4. Launch **Privycs VPN** from the Start Menu

The standalone **privycs-vpn-windows-amd64.exe** is also available if you prefer a portable version without installation.

### macOS

**Apple Silicon (M1/M2/M3):**

1. Download **privycs-vpn-darwin-arm64.dmg**
2. Open the DMG and drag **Privycs VPN** to your Applications folder
3. On first launch, right-click the app and select **Open** to bypass Gatekeeper (the app is not yet notarized)

**Intel Mac:**

1. Download **privycs-vpn-darwin-amd64.dmg** (or the `.zip` archive)
2. Same process as above

### Linux

**Debian/Ubuntu (.deb package):**

```bash
sudo dpkg -i privycs-vpn-linux-amd64.deb
sudo apt-get install -f   # install dependencies if needed
```

The package installs to `/usr/local/bin/privycs-vpn` with a desktop entry and application icon.

**Binary (any distribution):**

```bash
chmod +x privycs-vpn-linux-amd64
sudo mv privycs-vpn-linux-amd64 /usr/local/bin/privycs-vpn
```

### Dependencies

| Platform | Required Software |
|----------|-------------------|
| Windows | WebView2 Runtime (installed automatically by setup) |
| macOS | macOS 10.13 (High Sierra) or later |
| Linux | GTK 3, WebKit2GTK 4.1, libayatana-appindicator3 |

**Linux prerequisite install (Debian/Ubuntu/Zorin/Mint):**

```bash
sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0 libayatana-appindicator3-1
```

On older distributions (Ubuntu 20.04 or earlier), use `libwebkit2gtk-4.0-37` instead of `libwebkit2gtk-4.1-0`.

### AmneziaWG protocol on Linux (extra userland required)

The **WireGuard, OpenVPN and IPSec/IKEv2** protocols work on Linux out of the box. The **AmneziaWG** protocol (DPI-resistant WireGuard with obfuscation) is the one exception:

| Platform | AmneziaWG support | Extra install needed? |
|----------|-------------------|-----------------------|
| **macOS** | Built in — `amneziawg-go` is statically linked into the app | **No** — works out of the box |
| **Windows** | Built in — runs in-process via the privileged helper | **No** |
| **Linux** | Delegates to the system `awg-quick` userland (mirrors how the client uses `wg-quick` for vanilla WireGuard) | **Yes** — see below |

On Linux the client looks for `awg-quick` in `/usr/bin`, `/sbin` and on your `PATH`. If it is not found, AmneziaWG configs cannot connect (vanilla WireGuard/OpenVPN/IPSec are unaffected). You need **two** things:

1. **`amneziawg-tools`** — provides the `awg` and `awg-quick` commands.
2. **A backend** — *either* the AmneziaWG kernel module (best performance) *or* the `amneziawg-go` userspace implementation (no kernel module / DKMS needed; `awg-quick` falls back to it just like `wg-quick` falls back to `wireguard-go`).

**Arch Linux / Manjaro (AUR):**

```bash
# Kernel-module path (recommended):
yay -S amneziawg-dkms amneziawg-tools
# …or userspace-only (no DKMS):
yay -S amneziawg-go amneziawg-tools
```

**Debian / Ubuntu / Fedora / other distros** — install from the official Amnezia repositories (no distro package in the default repos yet):

```bash
# Tooling (installs awg + awg-quick into /usr/bin):
git clone https://github.com/amnezia-vpn/amneziawg-tools
cd amneziawg-tools/src && make && sudo make install

# Backend, pick ONE:
#  a) kernel module via DKMS — see the README of:
#     https://github.com/amnezia-vpn/amneziawg-linux-kernel-module
#  b) userspace (no kernel module):
git clone https://github.com/amnezia-vpn/amneziawg-go
cd amneziawg-go && make && sudo cp amneziawg-go /usr/bin/
```

If you install to a non-standard prefix, make sure `awg-quick` is reachable on your `PATH` (or symlink it into `/usr/bin`). After installing, re-import or reconnect the AmneziaWG config — no client restart is required.

> **Why the asymmetry:** on macOS and Windows the AmneziaWG engine is compiled into the Privycs binary, so there is nothing to install. On Linux the client intentionally reuses the system WireGuard/AmneziaWG userland (the same model as vanilla `wg-quick`), which keeps the client small and lets the OS manage the kernel module — at the cost of this one-time setup for AmneziaWG specifically.

---

## Getting Started

### Step 1: Import a Configuration

1. Open the Desktop Client and navigate to the **Add** tab
2. Click the drop zone or drag a configuration file onto it
3. The client auto-detects the protocol from the file extension:
   - `.conf` -- WireGuard
   - `.ovpn` -- OpenVPN
   - `.sswan` -- IPSec/IKEv2
4. Enter a connection name (e.g. "Office VPN") and click **Import Config**

You can also add a configuration file to an existing connection to enable protocol switching. Select the connection from the dropdown before importing.

### Step 2: Connect

1. Go to the **Connect** tab
2. Press the large shield button to connect
3. The button turns teal and shows **Connected** with an uptime counter
4. Transfer statistics (download/upload) appear below the button

### Step 3: Switch Protocols

If a connection has multiple protocol configs (e.g. WireGuard + OpenVPN), protocol badges appear below the connection name. Tap a badge to switch -- the client disconnects and reconnects with the selected protocol automatically.

---

## Features

### Multiple Connections

The **Configs** tab shows all saved connections. Each connection can have one or more protocol configurations. Click a connection to select it, then use the **Connect** tab to connect.

- **Rename** -- double-click the connection name or use the pencil icon
- **Add protocol** -- click the + icon to import another config for the same connection
- **Delete** -- click the trash icon to remove a connection

### System Tray

On Windows and Linux, the application runs in the system tray when minimized. Right-click the tray icon for:

- **Show** -- bring the window to front
- **Connect / Disconnect** -- toggle the VPN connection
- **Quit** -- disconnect the tunnel and close the application

On macOS, the tray icon is not available (system limitation). Use the Dock icon instead.

### Gateway Sync (API Key)

Instead of manually importing configuration files, you can connect the Desktop Client directly to your Privycs gateway and download configs automatically.

**Setup:**

1. In the Privycs web UI, go to **Settings > API Keys** and create an API key with `read` scope for the user
2. In the Desktop Client, go to **Settings > Privycs Gateway**
3. Enter the Gateway URL (e.g. `https://app.privycs.com`) and the API Key (`pvcs_...`)
4. Click **Verify & Sync** -- the app validates the key and shows your username and config count

**Downloading Configs:**

1. Go to the **Configs** tab and click the **Gateway** button
2. A panel shows all your available VPN configurations from the gateway
3. Each config shows: protocol icon, peer name, interface, and VPN IP
4. Click **Import** next to a config to download it with full secrets (private key, PSK, certificates)
5. The config is imported as a local connection, ready to connect

The API key only grants access to configurations assigned to your user account. Private keys are decrypted server-side and transmitted over TLS. No admin privileges required.

### Settings

| Setting | Description | Default |
|---------|-------------|---------|
| Gateway URL | Privycs gateway address for API-key based config sync | Empty |
| API Key | API key (`pvcs_...`) for gateway authentication | Empty |
| Kill Switch | Block all traffic when VPN disconnects. Cross-platform sinkhole: iptables (Linux), pf (macOS), WFP via privileged helper (Windows). State machine IDLE → ARMED → SINKHOLE | Off |
| Auto-connect on start | Connect automatically to the last active VPN when the app launches | Off |
| Connect-on-Demand | Network-aware auto-connect based on WiFi SSID or mobile-data rules | Off |
| Start at login | Launch the app automatically when you log into your OS | Off |
| Minimize to tray | Minimize to system tray instead of closing | On |
| DNS Override | Override DNS server pushed by the VPN config | Empty (use server DNS) |
| Routing Mode | Full tunnel, or CIDR-based split tunnel (specific subnets through VPN, rest direct) | Full |
| Theme | Dark, Light, or System Default | System |

### DNS Override

Override the DNS resolvers used while the VPN is connected. Set under **Settings → DNS Override** as a comma- or space-separated list of IPs (e.g. `1.1.1.1, 9.9.9.9`). Empty value means the VPN config's own DNS line wins.

When configured, the override replaces both:

- the `DNS = ...` line a WireGuard config carries
- the DNS server an OpenVPN gateway pushes via `dhcp-option DNS`
- the DNS server in an IPSec profile (Linux only — see compatibility table below)

**Why use it:** route lookups via your preferred resolver (e.g. NextDNS, AdGuard DNS, Quad9, your own Pi-hole), regardless of what the VPN provider configured. Useful for blocking ads/trackers at the resolver, enforcing DNSSEC, or preventing DNS leaks to the provider's logger.

**Per-protocol mechanics:**

| Protocol | Implementation |
|----------|----------------|
| **WireGuard** | Config text patch before parse: existing `DNS = ...` line in `[Interface]` is replaced; if absent, a new line is inserted. Works on all OSes via the same path. |
| **OpenVPN** | Config text patch: `pull-filter ignore "dhcp-option DNS"` plus one `dhcp-option DNS <ip>` directive per override server are prepended. The pull-filter drops the server-pushed value so our explicit lines win. |
| **IPSec/IKEv2** | Linux only: privileged helper backs up `/etc/resolv.conf` to `/etc/resolv.conf.privycs-bak` on connect, writes a fresh resolv.conf with the override, restores from backup on disconnect. The backup file persists across helper crashes so a missed disconnect still recovers on the next disconnect or manual restore. |

**Platform compatibility:**

| OS | WireGuard | OpenVPN | IPSec |
|----|:--:|:--:|:--:|
| **Linux** | ✓ | ✓ | ✓ |
| **macOS** | ✓ | ✓ | not supported |
| **Windows** | ✓ | ✓ | not supported |

**Why IPSec is not supported on macOS / Windows:**

- **macOS** runs IPSec via `.mobileconfig` profiles which embed DNS servers at install time. The OS NetworkExtension manages DNS through `configd` / `mDNSResponder`; per-tunnel override would require re-installing the profile with a fresh DNS list and a new user-trust prompt on every Settings change.
- **Windows** runs IPSec via the built-in `rasdial` CLI which has no per-tunnel DNS API. Override would need WMI `Set-DnsClientServerAddress` against the correct adapter, which is OS-version-specific and not currently implemented.

If you set a DNS override on macOS / Windows and connect via IPSec, the override is silently ignored for that tunnel; switching to WireGuard or OpenVPN on the same connection picks up the override correctly.

**Verifying the override is in effect:** run `nslookup something.com` while connected. The reported `Server:` line should match your override IP. If it shows the VPN provider's IP instead, either the override is empty (check Settings) or your protocol is in the "not supported" intersection above.

### Hardcore Kill Switch

The Kill Switch blocks all network traffic when the VPN tunnel drops unexpectedly. Unlike a simple "disconnect on drop" mechanism, it engages at the OS firewall level so no packets leak during the moment the tunnel goes down. The implementation is the same state-machine that ships in the Android client, ported to all three desktop OSes.

**State machine:**
- **IDLE** — disabled, or no successful connect yet this session
- **ARMED** — user has successfully connected; the sinkhole is ready to engage on the next drop
- **SINKHOLE** — the tunnel dropped or the user disconnected with KS enabled; OS firewall is now blocking all non-helper traffic

| Platform | Mechanism |
|----------|-----------|
| Linux | `iptables` `PRIVYCS_SINKHOLE` chain on INPUT/OUTPUT/FORWARD with snapshot+rollback |
| macOS | `pf` anchor `com.privycs/sinkhole` loaded via `pfctl -a` |
| Windows | WFP (Windows Filtering Platform) filters installed via the privileged helper service |

When the sinkhole is engaged, the Connect button on the main screen turns red with a shield-bad icon and the label **Kill Switch Active**. Tapping it does NOT fire a connect intent — the only release is toggling Kill Switch off in **Settings**, the same hardcore-lock semantics as the Android client. A toast surfaces this so the affordance is not just non-responsive.

On user-initiated disconnect with Kill Switch armed, the sinkhole engages with a 3-second delay to let the kernel's NDIS stack settle (Windows Wintun / TAP race avoidance). On macOS and Linux the engagement is immediate — only Windows needs the settle window.

Our helper process is exempted from the block so the helper IPC stays reachable. Toggling the Kill Switch off releases the firewall rules immediately and restores full connectivity. On Status() polls, if the tunnel is up but the KS state diverged (e.g. helper restart), the manager defensively re-arms.

### IPv6 Leak Killswitch

**Always-on, no setting.** Leaving IPv6 leakable through a v4-only tunnel is a critical security bug, not a user preference. On v0.9.14.96 and later, every successful connect on a v4-only tunnel triggers privileged-helper firewall rules that block all IPv6 outbound traffic (loopback `::1` exempted) for the duration of the session. Cleared on disconnect — success path AND error path.

| OS | Mechanism |
|---|---|
| **Linux** | ip6tables custom chain `PRIVYCS_V6_KS` hooked into OUTPUT, allow `lo` → drop everything else |
| **macOS** | pfctl anchor `privycs-v6-killswitch` with `pass quick out on lo0 inet6 all` + `block quick out inet6 all` |
| **Windows** | Two named Windows Firewall rules — Allow `::1/128` outbound, Block `::/0` outbound |

The decision to enable consults two automatic correctness checks (no UI toggle exists):

1. **Tunnel is IPv4-only?** Walks the tun interface's address list. If a non-link-local IPv6 address is present, the tunnel is dual-stack — no leak risk, skip block.
2. **OS has IPv6 connectivity?** Walks all non-tun, non-loopback up-state interfaces for non-link-local IPv6. If none, there's no leak vector — skip block.

Both must pass for the block to engage. If either fails, no firewall rules touched.

**Mitigation against orphan rules**: every app startup unconditionally calls the helper's `ipv6_unblock` — if the previous session crashed mid-tunnel with rules in place (failsafe direction is "fail closed"), those rules get cleared at next boot. Idempotent: no-op when nothing is there.

**Mitigation against silent failure**: helper-RPC failures (helper unreachable, action returned error) emit a `vpn:ipv6_leak_warning` Wails event + system error notification, so the user sees an explicit warning if the killswitch couldn't be installed. Without this surface, a user could see "Connected" while their IPv6 traffic still leaked.

> **Why this matters.** A typical home network with ISP-provided IPv6 + a budget VPN provider's v4-only profile would normally route AAAA-resolved DNS lookups (Google, Cloudflare, Facebook, etc.) out via the home ISP's v6 — completely outside the VPN. Server-side enforcement is impossible because the leak happens client-side before any packet reaches the gateway. The firewall block is the only reliable client-side fix.

### Connect-on-Demand

The Connect-on-Demand engine watches network changes and auto-connects or disconnects the VPN based on rules you configure. Supports:

- **Trigger type**: WiFi only / Mobile (cellular) only / Any network
- **SSID allow-list**: connect only when on listed SSIDs (press **Enter** in the SSID field to commit entries — the enter-key commit lets you add multiple SSIDs without leaving the input field, mirroring the Android chip-input UX)
- **SSID deny-list**: connect on all WiFi except listed SSIDs

On platform-native network events (netlink on Linux, SCNetworkReachability on macOS, `NotifyIpInterfaceChange` on Windows + `WlanRegisterNotification` for SSID-roam) the rules are re-evaluated. Settings writes persist through the privileged helper, so SSIDs are remembered across reboots.

On Windows, Connect-on-Demand subprocess invocations run with `CREATE_NO_WINDOW` so no console windows flash when the VPN silently reconnects on a WiFi change.

**SSID detection on locked-down Windows:** Some enterprise installs deny the user-facing WLAN APIs via Group Policy (Location services blocked or restricted). The client uses a four-path lookup chain: privileged-helper IPC → user-mode `WlanQueryInterface` → registry → reading the WLAN profile XML files at `%PROGRAMDATA%\Microsoft\Wlansvc\Profiles\Interfaces\{adapter-guid}\` directly. The XML-file path bypasses the Location GPO entirely because it reads disk-stored profile metadata that is not gated by the same permission check, so SSID-based COD rules work even on heavily-managed corporate machines. Results are cached for 500 ms with cache-invalidation on WLAN events to keep the syscall load low.

**COD pre-connect warning:** If you click **Connect** while Connect-on-Demand is enabled but no rule matches the current network, a confirmation dialog appears explaining that the engine will tear the tunnel down again within seconds. **Cancel** aborts the connect intent; **Connect anyway** proceeds with the connect knowing COD will react. The dialog hints that you can either disable Connect-on-Demand in Settings or add a matching rule for the current network. The warning never fires when COD is disabled, when a rule already matches, or when the backend status poll fails (fail-open).

### Manual Pause with Auto-Reconnect

Tap the blue **Pause** pill on the Connect screen to temporarily disconnect the VPN with an automatic reconnect timer. Choose from: **1 min, 3 min, 5 min, 15 min, 1 h, 4 h**. The Connect screen surfaces a countdown banner with a **Resume Now** button — tap it any time to cancel the pause and reconnect immediately.

The pause holds a flag inside the network-monitor loop, so Connect-on-Demand evaluates rules but skips its disconnect/connect actions while the timer runs. This stops COD from instantly overriding your pause when the trigger is "Any network" (a regression that was visible up to v0.9.10.30 and is fixed since v0.9.10.31). When the pause expires:

- If you connected manually before pausing, the client automatically reconnects after the timer expires.
- If COD was driving the connection, COD takes back control and decides based on the live network state.
- The public **Disconnect** button always cancels an active pause first, so manual disconnect is final and never wakes up later.

### Connection Switching with Auto-Reconnect

When you have more than one saved VPN connection, the connection-name area on the Connect screen turns into a picker (chevron icon). Tapping a different connection in the picker switches the active selection: if the tunnel was already up (or Connect-on-Demand says it should be), the client tears it down and reconnects on the newly-selected connection. If you are currently disconnected, the picker only updates the selection without bringing the tunnel up — you still drive the connect via the main button.

When Kill Switch is armed and a switch would otherwise reconnect, the upcoming reconnect is refused by the hardcore-lock guard and a toast surfaces the situation so you know to release Kill Switch first.

### Connection Pools

A **Connection Pool** is a virtual connection that wraps many VPN endpoints and picks one of them at connect time using a policy you choose. Instead of importing 600 individual configs from a commercial VPN provider and clicking through them one at a time, you import the whole archive once as a Pool and let the client pick the right server for each connect.

Each Pool keeps the full member list locally; nothing is sent back to a server, no third party sees which member you connected to. Pools coexist with regular saved connections — you can have a single corporate WireGuard connection AND a 600-member Pool side by side.

**Three policies determine how a member is picked:**

| Policy | What it does |
|--------|--------------|
| **Geo-Nearest** | Picks the member whose country matches yours, falls back to same continent, then random. The client detects your country once via DoH (DNS-over-HTTPS) and caches it. Good for streaming or low-latency browsing where a closer exit is worth more than rotation. |
| **Random** | Picks a uniformly random member every connect. Useful when you want each session to come from a different location without a periodic timer. |
| **Round-Robin** | Picks a different region than last connect, on a configurable timer. The default rotation interval is 30 minutes; presets cover 5 / 10 / 15 / 30 / 60 / 120 minutes plus a Custom field for arbitrary values. Use this for ongoing privacy where no single exit-IP should accumulate more than a slice of your traffic. |

**Importing a Pool:**

1. Go to the **Add** tab and pick **Add Pool**
2. Drop a single config, multiple configs, or a `.zip` archive containing many configs onto the drop zone
3. Choose a policy. For Round-Robin, set the rotation interval (preset or custom)
4. Click **Create Pool**

The import pipeline extracts archive entries in memory, detects each protocol from the file extension or content, resolves each member's endpoint hostname against the bundled GeoLite2 country database, and stores the assembled Pool to `pools.json`. A progress toast surfaces resolution status during the import — useful for archives with hundreds of members where DNS lookups dominate the time budget.

**The Pool indicator on the Connect screen:**

When a Pool is active, a card above the Connect button shows the Pool name, policy, the currently-active member ("Currently: <member-name> (CC)"), and — for Round-Robin only — a live countdown to the next rotation with a progress ring. The country flag for the active member is rendered as an SVG before the IP.

**Pre-warm rotation (Round-Robin):**

Sixty seconds before each scheduled rotation, the client picks the next member, runs a reachability probe against it (DNS resolve + TCP-Dial against common service ports), and writes the new `.conf` to disk under a different filename slot. The Pool indicator switches to amber and shows "Next: <upcoming-member>" so you know what is coming. When the rotation tick fires, the disconnect-then-reconnect runs against an already-prepared config — the file write that used to live in the critical path is gone, and the rotation completes faster than a manual click.

The slot alternation pattern (A and B) means each rotation writes to a different filename and installs a different OS service entry, which avoids a class of races where overwriting the active config would corrupt the running tunnel.

**Region restriction:**

Round-Robin auto-pins to the user's home region by default — without this, alphabetical rotation would send a user in Vienna to Africa, Asia, then North America before coming back to Europe, with the consequent latency penalty on each tick. You can override the restriction in the Pool Edit modal to allow rotation across all regions or pick a custom subset (e.g. "Europe + North America only").

**Reliable rotation against dead servers:**

Pool rotation no longer dies silently when the picked server is offline. Three layers of resilience run in sequence per rotation:

1. **Pre-warm probe** — DNS plus TCP-Dial against the upcoming member 60 seconds ahead. Failures mark the member unreachable and re-pick another, up to three picks before giving up on this rotation cycle. Most genuinely dead endpoints get filtered before they ever reach the rotation tick.
2. **Connect-retry loop** — at the rotation tick, up to three members are tried in sequence. Each `Up()` failure marks the member unreachable and the next-best candidate is picked.
3. **Handshake health-check** — for WireGuard members, after the OS-level tunnel comes up, the client polls `latest-handshake` every 500 ms for up to five seconds. A non-zero handshake means the remote peer responded; zero after timeout means the local tunnel installed but the server is dead, so the tunnel is torn down and the next member is tried.

The unreachable flag carries a 30-minute TTL — after that, the member is silently re-eligible. Members that were marked unreachable during a network blip (laptop sleep, WiFi switch) get reset all-at-once if the entire pool would otherwise be empty. The pool stays usable when you wake from sleep or switch to a hotel WiFi.

**Unreachable badge in the Pool detail view:**

Open any Pool and the member list shows an amber **Unreachable** badge next to flagged members, with the row tinted amber for at-a-glance scanning. A header counter shows the total ("12 unreachable"), and a **Reset all** link clears every flag in one persisted write — useful when you reconnect to a known-good network and want immediate full availability rather than waiting for the TTL.

Each badge tooltip carries the failure reason (no handshake, probe DNS error, etc.) and the timestamp, so you can tell whether a member is genuinely problematic or just temporarily caught.

**Pool Detail editing:**

The cog icon in the Pool detail view opens an Edit modal with name, policy, country override (Geo-Nearest), rotation interval (custom values supported), and region restriction list. Changes propagate to the rotator immediately — if you switch a 30-min Pool to 5-min while it is active, the next rotation fires within the new interval without restarting the tunnel.

**Stable per-Pool tunnel name:**

Each Pool uses a deterministic interface name derived from its ID — `privycs-pool-<id8>-A` and `privycs-pool-<id8>-B` for the two slots. Multiple Pools running simultaneously do not collide. Old per-member tunnel names are no longer created, so the OS service registry stays clean across hundreds of rotations.

**Auto-restore at app start:**

The active Pool ID and active member persist across app restarts — when you reopen the client, the same Pool is selected and the Connect button is ready to fire. The Pool card renders on the very first frame instead of waiting for the four backend IPC calls, so cold-start no longer flashes a generic "no connection selected" state when you genuinely have one selected.

If your imported Pool was created with a pre-v0.9.11.9 client, the country fields on each member may be empty. The client backfills them in the background using the bundled GeoLite2 database; a toast shows "Loading exit-point countries 142 / 600" while it works. No re-import needed.

### Per-Pool Split Tunnel (Bypass)

Each Connection Pool can carry a **bypass-CIDR list** that excludes specific IP ranges from the tunnel for the duration of any pool member's connection. Open the pool's **Edit settings** modal (gear icon on the pool card) and find the **Split tunnel (bypass)** section.

Two inputs:

- **Exclude private networks** toggle — adds the standard private-network CIDRs in one click: RFC1918 (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), IPv4 link-local (`169.254.0.0/16`), IPv6 ULA (`fc00::/7`), and IPv6 link-local (`fe80::/10`).
- **Custom bypass CIDRs** textarea — one CIDR per line, IPv4 and IPv6 mixed freely. Plain IPs are accepted as `/32` (v4) or `/128` (v6) for convenience. The hint below the textarea counts valid lines and lists invalid ones with a sample.

**Per-protocol mechanics:**

| Protocol | Implementation |
|----------|----------------|
| **WireGuard** | `AllowedIPs` is rewritten as the COMPLEMENT of the bypass set against the original AllowedIPs. So `0.0.0.0/0` minus `10.0.0.0/8` expands to ~13 IPv4 ranges covering everything outside `10/8`. Same algorithm `wg-quick` uses for its `exclude` syntax. If the original AllowedIPs is NOT full-tunnel (no `0.0.0.0/0` and no `::/0`), the bypass is silently disabled with a log warning — layering bypass over an already-narrowed AllowedIPs would unpredictably re-route the provider's specific routes. |
| **OpenVPN** | `route X.X.X.X mask Y.Y.Y.Y net_gateway` (IPv4) and `route-ipv6 X::/n net_gateway_ipv6` (IPv6) directives are prepended to the config so the bypass CIDRs land on the local default gateway. Plus `pull-filter ignore "route X"` lines drop any matching server-pushed routes which would otherwise compete with our bypass. |
| **IPSec/IKEv2** | Not supported. IPSec traffic selectors are negotiated with the server during IKE_AUTH; client-side narrowing isn't reliable across providers. The patcher logs a warning and leaves IPSec pool members untouched. |

**Examples:**

| Use case | Bypass CIDRs to enter | Effect |
|---|---|---|
| Keep your home LAN reachable while connected | toggle "Exclude private networks" on | All RFC1918 + IPv6 ULA traffic routes via your local network instead of the VPN. Lets you reach `192.168.x.x` printers, `10.0.x.x` NAS, `fe80::...` neighbours. |
| Bypass one specific service (e.g. local mail server) | `192.0.2.50` *(plain IP, becomes /32)* | DNS lookups to that IP go through the VPN; packets to it skip the tunnel. Useful when a service geo-blocks your VPN exit. |
| Keep two corporate /16 networks direct | `10.10.0.0/16`<br>`10.20.0.0/16` | Only those two ranges bypass the tunnel; everything else (including the rest of `10/8`) still tunnels. Useful with multiple LAN segments. |
| Bypass IPv6 only | `2001:db8::/32` | IPv6 traffic to that range is direct, all other IPv6 + all IPv4 still tunneled. |
| Combo: home LAN + a public DNS | toggle "Exclude private networks" on<br>and add `1.1.1.1` | Cloudflare DNS at `1.1.1.1` resolves direct (so the VPN's DNS-leak protection doesn't intercept it) and your LAN is reachable. |

**Pool-aware preserve-on-rotation:** When the pool rotates to a new member (Round-Robin), the bypass set is re-applied to the new member's config automatically. You don't have to re-edit anything; rotation honours the per-pool setting.

**Pre-warm interaction:** A pool with split tunnel active bypasses the desktop's pre-warm shortcut (where the next-rotation `.conf` is written 60 s ahead). The pre-write does not include the bypass patch, so we re-do the write at rotation time. Cost: ~1 s extra rotation latency vs the alternative of leaking full-tunnel routes for the bypass CIDRs.

**Verifying the bypass works:** With the bypass active and `1.1.1.1/32` in the list, `curl https://1.1.1.1/cdn-cgi/trace` should show `ip=<your-home-IP>`, while `curl https://example.com/cdn-cgi/trace` (non-bypass) shows `ip=<vpn-exit-IP>`.

### Per-app split tunnel — Desktop status

Per-app split tunnel (route only specific applications through the VPN, or exclude specific applications from it) is **not currently available on desktop**, only on Android. The desktop OSes lack a uniform per-process VPN-routing API:

| OS | Mechanism that would be needed |
|---|---|
| Linux | cgroup v2 `net_cls` + iptables CONNMARK rules; or per-process network namespaces (root required) |
| Windows | Windows Filtering Platform (WFP) process-level filters via a signed kernel-mode driver (WHQL signing required) |
| macOS | `NEAppPushManager` per-app VPN provider with `app-proxy-provider` entitlement (Apple approval required) |

Each is a substantial native implementation; together they would total ~9–13 weeks of development plus signing-hardware and account costs. Tracked as a future Pro-tier candidate; not on the v0.9.x roadmap.

In the meantime, two CIDR-based split-tunnel mechanisms cover most "exclude my LAN / exclude my work subnets" use cases on Desktop:

- **Per-pool Bypass-CIDR list** (above) — the recommended path. Per-pool CIDR exclusions, applied to every member, persisting across rotation.
- **Per-connection Bypass-CIDR list** — same UI on the connection edit sheet for single connections.

### Encrypted Local Backup

Under **Settings → Backup & Restore**, export all saved connections and app settings as a single `.privycs-backup` file encrypted with AES-256-GCM. PBKDF2 key derivation (100,000 iterations, SHA-256) protects against brute-force password attacks; the password is never stored anywhere on your device.

Restore on any device by importing the backup file and entering the same password. Existing connections with matching names are overwritten; new connections are added.

### QR Code Import

WireGuard configs can be imported by scanning QR codes generated on your gateway's Downloads page. Click **Scan QR Code** in the Add Connection dialog — your device camera activates via the OS camera permission and the scan completes in under 5 seconds. No camera access is needed outside this workflow.

### Live Traffic Sparklines

The Connect screen shows live download and upload speed as **sparkline charts** below the transfer-byte counters. On Windows, the stats come from the native Win32 `GetIfEntry2` API (falls back to WMI for older OpenVPN-DCO adapters); on Linux and macOS, from `/proc/net/dev` and `netstat -I`. Sampling runs at ~1 Hz so spikes and idle periods are visible in real time.

### Privileged Helper

Administrative actions (writing firewall rules, loading VPN configurations, setting up tun devices) run through a small privileged helper process that the main application installs once on first-launch with admin elevation:

| Platform | Helper Location | IPC |
|----------|-----------------|-----|
| Linux | systemd unit at `/etc/systemd/system/privycs-vpn-helper.service` | Unix socket `/var/run/privycs-vpn.sock` (0666 permissions so any login user can connect without sudo) |
| macOS | launchd plist at `/Library/LaunchDaemons/com.privycs.vpn.helper.plist` | Same Unix socket model |
| Windows | Windows Service installed via `privycs-vpn-helper.exe install` | Named pipe `\\.\pipe\privycs-vpn` |

Once installed, subsequent launches of the main UI connect to the helper silently — **no sudo prompt, no UAC flash on every connect**. The helper writes to its own log file so administrative errors can be diagnosed separately from UI-level logs.

### Logs

The **View Logs** button in Settings opens the last 100 log entries from the application log. Use this for troubleshooting connection issues.

| Platform | UI Log | Helper Log |
|----------|--------|------------|
| Linux | `~/.local/share/privycs-vpn/privycs-vpn.log` | `/var/log/privycs-vpn-helper.log` |
| macOS | `~/Library/Application Support/privycs-vpn/privycs-vpn.log` | `/Library/Logs/privycs-vpn-helper.log` |
| Windows | `%LOCALAPPDATA%\privycs-vpn\privycs-vpn.log` | `%PROGRAMDATA%\privycs-vpn\helper.log` |

---

## Configuration File Formats

### WireGuard (.conf)

Standard WireGuard configuration with `[Interface]` and `[Peer]` sections. The client extracts the server endpoint and local address for display.

```ini
[Interface]
PrivateKey = YOUR_PRIVATE_KEY
Address = 10.100.110.2/24
DNS = 10.100.10.150

[Peer]
PublicKey = SERVER_PUBLIC_KEY
Endpoint = vpn.company.com:51820
AllowedIPs = 0.0.0.0/0
```

### OpenVPN (.ovpn)

Standard OpenVPN configuration. The client locates the `openvpn` binary automatically on Windows (checks `C:\Program Files\OpenVPN\bin\`) and via `PATH` on Linux/macOS.

### IPSec (.sswan)

strongSwan Android profile format (JSON). The client extracts the PKCS#12 certificate bundle and creates a native IKEv2 connection:

- **Windows** -- creates a VPN connection via `Add-VpnConnection` with machine certificate authentication
- **Linux** -- writes `swanctl.conf` and loads certificates into `/etc/swanctl/`
- **macOS** -- use `.mobileconfig` profiles instead (native IKEv2 support)

---

## Troubleshooting

### Connection fails immediately

- **WireGuard**: Ensure `wg-quick` is installed (`sudo apt install wireguard-tools`)
- **OpenVPN**: Ensure OpenVPN is installed and in PATH (`openvpn --version`)
- **IPSec**: Ensure strongSwan is installed (`swanctl --version`) or use native OS IKEv2

### Windows: "Access Denied" on connect

The VPN tunnel requires Administrator privileges. The client uses UAC elevation (`Start-Process -Verb RunAs`) automatically. If elevation is blocked by Group Policy, run the client as Administrator.

### macOS: App won't open

Right-click the app and select **Open** instead of double-clicking. This bypasses Gatekeeper for unsigned applications.

### Linux: No system tray icon

Install `libayatana-appindicator3-1` and ensure your desktop environment supports AppIndicator (GNOME requires the AppIndicator extension).

### Slow connection or frequent disconnects

Check the **Logs** in Settings. Common causes:

- MTU mismatch -- the server may need `tun-mtu` adjustment
- DNS resolution timeout -- try setting a DNS Override in Settings
- NAT timeout -- ensure the server has keepalive configured

---

## Data Storage

The client stores all data locally in the platform-specific app data directory:

| Platform | Path |
|----------|------|
| Windows | `%LOCALAPPDATA%\privycs-vpn\` |
| macOS | `~/Library/Application Support/privycs-vpn/` |
| Linux | `~/.local/share/privycs-vpn/` |

Files stored:

| File | Description |
|------|-------------|
| `connections.json` | All saved connections and protocol configs |
| `settings.json` | Application settings (theme, kill switch, etc.) |
| `privycs-vpn.log` | Application log file |
| `*.conf` / `*.ovpn` | Imported VPN configuration files |

All files are stored with restricted permissions (owner read/write only).

---

## What's New

The Desktop Client shares its version-space with the Android Client. Tags are unified (`v0.9.10.X` covers both clients).

- **v1.1.3.23** *(cross-platform; all desktop OSes rebuilt)* — **Deleting a connection now cleans up after itself.** Removing a connection previously left its tunnel config files behind in the app-data folder (WireGuard `.conf`, OpenVPN `.ovpn`/`.log`/`.pid`), and — if you deleted a connection while it was still connected — left the OS-level tunnel running with no app record to control it. Delete now first disconnects a live tunnel for that connection (so its routes and teardown run properly), then removes the leftover config files. macOS IPSec additionally clears its strongSwan config and certificates via the privileged helper.
- **v1.1.2** *(cross-platform; all desktop OSes rebuilt)* — **Smart Decision Engine: adaptive scoring + roaming awareness.** When Automatic protocol selection is on, the engine now learns from outcomes on each network — a protocol that repeatedly fails to come up on your current Wi-Fi or wired link is quietly demoted on the next attempt (an in-memory per-network success/fail score, reset when you change networks), and on a metered/cellular uplink IPSec is promoted so the tunnel rides through network switches via MOBIKE. Manual protocol selection (engine off) is unchanged — the existing failover order still applies. Shipped in lockstep with Android and iOS v1.1.2.
- **v1.0.8.2** *(cross-platform; all desktop OSes rebuilt)* — **Localized server-location country names.** The Connect-screen location label now renders country names in your selected UI language (via `Intl.DisplayNames` over the OS locale data) instead of a fixed English table; one stray English DNS-provider accessibility label was also moved into the locale files. No behavioural change. Shipped alongside Android v1.0.8.2 and the iOS v1.0.8.3 home-screen-widgets release.
- **v1.0.8** *(cross-platform; all desktop OSes rebuilt)* — **In-app Help links fixed; version aligned to 1.0.8.** The Help screen renders the live guide, which cross-links to sibling docs with relative `.md` paths — clicking those did nothing before (a relative link can't be opened in the embedded webview); they now open the correct page in your default browser. Version bumped to 1.0.8 alongside the coordinated iOS / Android release (no other desktop-side functional changes).

- **v1.0.7** *(cross-platform feature; all desktop OSes rebuilt)* — **Anonymous crash reporting via self-hosted Bugsink — opt-in, redacted, never to third parties.** Closes the "open test would be blind" gap from the 2026-05-21 production-readiness audit. Shipped in lockstep with Android v1.0.7. **Server side:** self-hosted Bugsink 2.2.1 on the Privycs gateway box at `https://crashes.privycs.com` (Sentry-protocol compatible, single-SQLite + snappea workers, ~150 MB RAM, systemd-managed, nginx-fronted with TLS via the wildcard *.privycs.com cert, rate-limited ingestion at 100 r/s + 200 burst). **Client side:** sentry-go v0.46 on the Go side + @sentry/vue v10.55 on the Vue side. Init is gated on a new persistent `CrashReportsEnabled` field in AppSettings — default OFF on every platform — wired to a toggle in Settings → Diagnostics; description copy "*Help improve Privycs by sending anonymous crash reports. SSIDs, API keys, IPs and file paths are redacted on-device before upload to our self-hosted backend.*" An anonymous 16-byte hex install UUID (generated on first run, persisted in settings.json) serves as the only identifier — never linked to API key, license key, account email, or any other identity. **PII redaction pipeline runs on every outgoing event** before it leaves the device: SSIDs stripped, 32+ char base64/hex tokens replaced (catches API keys, license-key signatures, JWT segments), public IPv4 stripped (RFC1918 / loopback / link-local kept for local-network debug), global-unicast IPv6 stripped (ULA + link-local kept), file paths with the user's home segment replaced (`/Users/<name>/` → `/Users/<user>/`, same for `C:\Users\` and `/home/`), HTTP request bodies + headers nil'd unconditionally, modules list (loaded library versions) wiped, navigation breadcrumbs dropped. Session replay explicitly disabled with a hard guard so a future SDK default flip can't accidentally turn it on. Toggle flip wirkt sofort: opt-out swaps in a disabled transport and any in-flight events are discarded. App shutdown flushes pending events with a 2 s deadline. 6 new i18n entries × 6 languages (en/de/es/fr/it/pt). Build clean across all three OSes (go build + go vet + npm run build all exit 0). Sentry SDK adds ~3 MB to the Go binary and ~250 KB to the Vue bundle; Android AAB grows by ~600 KB.
- **v1.0.6** *(SECURITY — cross-platform; all desktop OSes rebuilt)* — **Closes the local-RCE-as-root attack surface flagged in the 2026-05-21 production-readiness audit + clears every cross-OS `go vet` warning on the desktop tree.** **(1) Peer-UID enforcement on the helper IPC.** Pre-v1.0.6 the helper's AF_UNIX socket was world-writable (mode 0666) with no peer-credential check on the IPC layer — any unprivileged local user on the same machine could open the socket and invoke any whitelisted action (including ones that write privileged files like `ipsec_configure` writing `/etc/swanctl/conf.d/*` as root, or that spawn privileged subprocesses like `ipsec_install_windows_profile` running an attacker-controlled `.cmd` script as SYSTEM). v1.0.6 enforces peer-UID at the IPC accept layer: Linux uses `SO_PEERCRED` via `syscall.GetsockoptUcred`, macOS uses `LOCAL_PEERCRED` via `unix.GetsockoptXucred(SOL_LOCAL, LOCAL_PEERCRED)`, Windows AF_UNIX has no peer-cred equivalent on this transport (scheduled v1.0.7: transition to named pipes for `GetNamedPipeClientProcessId` → `OpenProcessToken` → `GetTokenInformation` gating; meanwhile the existing `icacls Authenticated-Users` ACL on the socket file is the gate). Whitelist persisted at `/etc/privycs-vpn/allowed-uids` (root:root, 0644); UID 0 is implicit-allow; Trust-On-First-Use enrolment records the first non-root caller's UID and rejects any subsequent enrolment attempt from a different UID. The app initiates an `enroll_uid` IPC immediately after the first successful Connect-to-helper, shrinking the TOFU race window to milliseconds between helper-install and first app launch. Every accepted command's audit log line now carries the peer UID for forensic correlation; every rejected connect is logged with the offending UID + attempted action. Pre-v1.0.6 upgrade path: file is absent on first run after upgrade → first peer-cred-verified caller is enrolled, the helper logs the enrolment prominently, the file now drives enforcement. macOS / Linux users may need to remove `/etc/privycs-vpn/allowed-uids` (as root) once if a stale UID got enrolled by accident in the upgrade race. **(2) Cross-OS `go vet` zero-warning.** Five `unsafe.Pointer` misuse occurrences in the WLAN-API call sites refactored from manual `uintptr+offset` arithmetic to typed `*wlanInterfaceInfoList` / `*byte` pointers + `unsafe.Add` for offset access. Cgo-tagged darwin files (`power_macos.go`, `network_monitor_darwin.go`) flipped to `darwin && cgo` build tags + nocgo stubs so Linux-host `GOOS=darwin go vet` passes cleanly while the CI macOS runner stays on the real cgo path. CLAUDE.md's earlier "pre-existing cross-compile setup, not something to fix" line no longer applies. Behaviour-neutral refactor; vet exits 0 on all three OSes.
- **v1.0.5.29** *(cross-platform UI fix; all desktop OSes rebuilt)* — **Horizontal scrollbar appearing across the bottom of the Connect screen when a multi-config protocol pill (typically OpenVPN) was tapped.** User-reported screenshot showed a faint lighter horizontal bar sitting just above the bottom navigation, observed exclusively for the OpenVPN pill. Diagnosis: the 256 px-wide picker dropdown rendered as `absolute left-0 mt-1 w-64`, anchored to the left edge of its pill. When the pill sits in the right half of the flex-wrap protocol row (OpenVPN is at index 2 of *AmneziaWG / WireGuard / OpenVPN / IPSec*; IPSec wraps to row 2 leaving OpenVPN right-most in row 1), the dropdown overflows the viewport's right edge — and the OS draws a horizontal scrollbar across the bottom of the window. v1.0.5.29 fixes both root and symptom: the dropdown now measures its host pill's `getBoundingClientRect()` at click time, decides whether `left-anchored` would overflow `window.innerWidth - 12 px gutter`, and flips to `right-anchored` (`right-0 left-auto`) when needed — no content clipping, full content visible on either side. Plus a defence-in-depth `overflow-x-hidden` on the app's `<main>` element so any future overlay that overflows the right edge gets silently clipped rather than triggering a scrollbar. Android is a bystander — Compose uses `ModalBottomSheet` for the multi-config picker, which is viewport-aware by design. Same release also bundles a TypeScript-error cleanup: regenerated stale Wails bindings (caught up with the Go App-method additions from v1.0.5.x), fixed six adjacent type-mismatches across `pool.ts`, `AddPoolView.vue` (vue-i18n@9 `$t`/`$tc` plural-form syntax), `ConnectionsView.vue` (`ImportConfig` arity), `HelpView.vue` (`markdown-it` module declaration), `LogsView.vue` (reload-button onClick PointerEvent leak), and `main.ts`. `vue-tsc --noEmit` now exits clean.
- **v1.0.5.28** *(cross-platform behaviour fix; all desktop OSes rebuilt)* — **Two desktop bugs bundled: "Clear logs" → Permission denied (all OSes), and macOS IPSec "ständig down" (charon died on every disconnect).** **(1) Clear-logs permission-denied (all 3 desktop OSes).** User-reported on macOS: tapping "Clear logs" in Settings fails with *"permission denied: openvpn.log"*. Same class of bug hits Linux + Windows for the same reason — the privileged helper (root/SYSTEM) spawns the OpenVPN daemon which creates `<connection-name>.log` files mode 0644: world-readable for the user-app, but owner stays root/SYSTEM; `os.Truncate` needs WRITE permission and EACCESes. Plus two adjacent bugs in the same code path: `knownLogSources` was hard-coded to two static paths — one (`"openvpn.log"`) never existed because OpenVPN now writes per-profile `<connection-name>.log` files, so both LogsView and Clear-logs were operating on a phantom file; and the "merged" log view wasn't actually merged in time order — it concatenated source A then source B instead of interleaving by timestamp. Fix: new helper IPC `clear_logs` with strict path-gating (must end in `.log`, must contain `privycs-vpn` as a path segment, must pass the existing `safePathPattern`); user-app `clearLogs()` tries direct Truncate first and only routes EACCES files through the helper. Plus dynamic `*.log` discovery so every per-profile log shows up in the merged view, with chronological merge that parses three timestamp formats (Go log-package default, OpenVPN default, strongSwan default) and assigns unparseable continuation lines the previous-line timestamp. **(2) macOS IPSec: charon died on every disconnect.** User-reported follow-up to v1.0.5.27: even with the auto-restart-and-retry path, IPSec felt "ständig down — muss von Hand starten". Helper log diagnosis showed 168 `Connection refused` events since first connect, three distinct charon PIDs in one hour, and an `ipsec stop (post-disconnect) OK` after every single Disconnect. Root cause: the v0.9.14.93 "bulletproof teardown" path called `ipsec stop` on every disconnect to guarantee no userspace charon state survived between sessions — and the cure was worse than the disease: charon was non-running outside of an active session, every next Connect tried to load against a dead vici socket, got Connection refused, and triggered the v1.0.5.27 auto-restart-and-retry detour for ~5-7 s of pure user-visible "Connecting…". Fix: trust `swanctl --terminate` (3 s timeout, already there) PLUS the unconditional `setkey -F` + `setkey -FP` kernel SADB/SPD flush to guarantee no SA survives in the kernel even if charon's userspace IKE_SA descriptor lingers — but DROP the unconditional `ipsec stop` on every disconnect. charon stays alive between sessions, vici socket stays bound, next Connect is one `--load-all` + `--initiate` away. The v0.9.14.93 wedged-charon safety net is preserved: if `--terminate` failed AND a 1 s `swanctl --list-sas` probe also fails, charon is genuinely wedged → fall back to `ipsec stop`. Healthy disconnect now does nothing extra; only the wedged-state path still hard-stops. Cross-compile clean.
- **v1.0.5.27** *(macOS-only behaviour fix; macOS rebuild only)* — **Two macOS IPSec robustness fixes bundled.** **(1) Auto-restart-and-retry when `swanctl --load-all` hits a stuck vici socket.** User-reported: even with the v1.0.5.20 fresh helper that centralises `--uri` pass-through, Profile-Import (gateway-pull) AND Connect-attempt intermittently fail with `connecting to 'unix:///var/run/charon.vici' failed: Connection refused`. Pattern: charon is up (`pgrep` finds the process) but the vici socket refuses connections — classic zombie/stuck state. Manual recovery is `sudo ipsec stop && sudo ipsec start`; afterwards charon auto-loads `/etc/swanctl/conf.d/*` and the profile is available. v1.0.5.27 wires the helper to detect this exact signature ("Connection refused" / "Connection reset" / "No such file or directory" from the vici socket) and auto-run the full hard-restart sequence (`ipsec stop` → wait for socket gone → `setkey -F` + `-FP` to flush the kernel SADB/SPD → `ipsec start` → wait for socket back) and retry `--load-all` once. Only kicks in on the vici-down signature; non-vici failures (malformed swanctl.conf, missing certs, charon startup error) fall through to the normal error path. Restart cost ~5-7 s end-to-end, only on failure. Covers both pain points: Profile-Import now self-heals; Connect-attempt failover (which the App previously did to WireGuard on this error) no longer fires for the recoverable stuck-vici case. **(2) Install default `::/0` v6 route via the utun so IPv6 actually leaves the tunnel.** User-reported follow-up: even after the v1.0.5.21 `/128 → /64` vip rebind fixed RFC 6724 source-address-selection, IPv6 still didn't work — `netstat -rn -f inet6` confirmed zero default v6 route via the utun. Root cause: charon-libipsec on macOS brings the SA up (vip assigned, kernel SPD populated) but does NOT install any routing entry for v6 — it's a userspace TUN-based ESP that bypasses NE entirely, so no `route(8)` calls happen client-side. WireGuard and OpenVPN install their v6 default routes themselves; charon-libipsec is the outlier. Fix: a new privileged-helper IPC `ipsec_install_macos_v6_default_route` that runs `/sbin/route -n add -inet6 default -interface <utun>`, called from the App AFTER `installMacOSSplitTunnelRoutes()`. **Ordering is critical** (user explicitly flagged it): bypass routes go in first as more-specific entries, then `::/0` lands as the catch-all — BSD's longest-prefix-match honors the bypass over the default. Reversing the order would leak bypass-destined v6 traffic through the tunnel for the install window. Idempotent — `File exists` from route(8) is treated as success. Best-effort: missing helper / no v6 vip / non-File-exists route failure all log and fall through, the tunnel stays up. Disconnect path is unchanged — BSD auto-removes routes pointing at a vanished iface so no explicit removal IPC is needed. Linux + Windows are bystanders for both fixes (different IPSec stacks); their version files stay at v1.0.5.25.
- **v1.0.5.25** *(cross-platform behaviour fix; all desktop OSes rebuilt)* — **macOS wake-recovery now respects the Auto-tunnel master toggle.** User-reported follow-up to v1.0.5.22: with the master toggle OFF, closing the laptop lid and reopening it still re-established the VPN tunnel. v1.0.5.22 had gated the Desktop startup auto-connect path but missed the `handleSystemDidWake()` path used by the macOS NSWorkspaceDidWake notification handler — that path checked `ReconnectOnSystemWakeEnabled` (default ON) but not `NetworkRulesEnabled` (master toggle). v1.0.5.25 adds the same master-OFF early-return the v1.0.5.22 startup gate uses; with master OFF the wake event now logs the skip and takes no action. Linux + Windows are bystanders here (no equivalent NSWorkspaceDidWake; the OS-suspend recovery on those platforms goes through other paths already gated in v1.0.5.22). Shipped in lockstep with Android v1.0.5.25 which closes four parallel client-side leaks on that platform (Connect-button reconnect-after-disconnect, TunnelHealthMonitor recovery, VpnPauseTimer expiry, NetworkMonitor stale-state propagation) — see the Android changelog. No behavioural change for users with master ON; the wake-recovery path remains as it was for them.
- **v1.0.5.24** *(cross-platform behaviour fix; all desktop OSes rebuilt)* — **Traffic counter robustness — three surgical fixes for "counter jumps back to 0 mid-session" and "non-zero state carries across fresh sessions".** **(1)** Desktop session-byte baseline (the wake-restart bridge introduced in v0.9.14.89 for macOS IPSec) leaked across two user-initiated session-change paths: switching to a different connection via `ActivateConnection`, and switching protocol inside `connectInternal`. After a wake-recovery, a subsequent connection/protocol switch would carry the accumulated bridge bytes into the new session's display total. v1.0.5.24 resets the baseline at both call sites right after the disconnect / pre-switch Down(). Wake-recovery continuity intact — the baseline snapshot happens in the PowerEvents goroutine after the user-facing Connect() returns. **(2)** macOS IPSec `parseSwanctlBytes` can transiently return 0 in three cases (gap between the `--ike`-filtered query and the unfiltered fallback, fresh CHILD SA right after rekey, post-charon-restart settle window) — making the user-visible counter drop to 0 mid-session before climbing back. v1.0.5.24 adds a per-handler sticky-max: every successful non-zero read advances the max, every lower read is replaced by the max. Reset to 0 on Up() so a fresh connect starts clean. **(3)** Android pool rotation snapped the counter to 0 every rotation. While a pool was active, `VpnServiceManager.update()` displayed the underlying tunnel poller's raw rxBytes/txBytes — and on rotate-out the new member's fresh tunnel starts at 0, losing every byte the prior member transferred. v1.0.5.24 introduces a pool-baseline accumulator: any backwards jump in raw rxBytes/txBytes while the same pool stays active adds the previous raw value to the baseline; the displayed counter is always raw + baseline. Reset to 0 on pool teardown. Single-connection path bypassed (tunnel poller remains authoritative there). No behavioural change to actual connectivity — only the user-visible byte total is affected.
- **v1.0.5.23** *(cross-platform UI improvement; all desktop OSes rebuilt)* — **Live engine-decision card at the bottom of the On-Demand & Network Rules screen.** Replaces the prior static "default behaviour" explainer block with a dynamic card that, in the user's own language, narrates what the rule engine is deciding right now against the current network — e.g. *"Connected to Wi-Fi 'Hoep@Home' · Auto-tunnel: Off → Manual control only — engine takes no action"*. Three decision branches render: master toggle OFF surfaces "Manual control only" (matches the v1.0.5.22 manual-only semantics); master ON with a matching rule shows "A rule matches the current network — engine is acting"; master ON with no match shows "No rule matches — engine takes no action". A new `GetCurrentNetworkRulesEval` Wails method exposes a small snapshot (network type, SSID, master-enabled, has-rules, engine-active, rule-matching); the Vue view polls it every 2 s and renders three computed text lines from the i18n catalog. 11 new locale keys × 6 languages (en/de/es/fr/it/pt) on both Desktop and Android. Same change shipped in lockstep with Android v1.0.5.23.
- **v1.0.5.22** *(cross-platform behaviour fix; all desktop OSes rebuilt)* — **Master toggle OFF = strictly manual.** User-reported that switching the Auto-tunnel master toggle to OFF didn't reliably yield manual-only control — silent reconnects still happened from the system-restart path, the WorkManager backstop tick, and the pool-keepalive watcher on Android, and from the startup auto-connect path on Desktop. The master toggle was honoured by the *live* rule evaluator but ignored by every *fallback* reconnect entry point, so OFF behaved like "rules paused" rather than "manual-only". v1.0.5.22 inserts a single early-return master-OFF gate at the top of each of these paths (`handleAlwaysOnReconnect`, `AutoTunnelWorker.doWork`, `PoolKeepaliveWatcher.tryReconnectPool` on Android; the Wails `startup` auto-connect branch on Desktop). When the master is OFF the service either logs the skip and returns, or — in the always-on case — stops the foreground service entirely. Effect: with master OFF, the only thing that can change tunnel state is the user pressing Connect / Disconnect.
- **v1.0.5.21** *(macOS-only behaviour fix; all desktop OSes rebuilt)* — **macOS IPSec: IPv6 traffic now actually leaves the tunnel.** User-verified bug after v1.0.5.20 unblocked the IPSec import: the dual-stack SA established correctly (both v4 and v6 vips assigned, gateway-side NAT66 functional, `ping6 -S <vip> ipv6.google.com` returned 70 ms RTTs), but outbound IPv6 connections without an explicit source ended up with source `::1` and `sendmsg: No route to host`. Root cause: strongSwan's `charon-libipsec` plugin (the macOS userspace IPSec backend) installs the v6 vip on the utun adapter as a `/128` Point-to-Point binding (`inet6 fd63:43:45::3 --> fd63:43:45::3 prefixlen 128`). macOS's RFC 6724 source-address-selection refuses to auto-pick a `/128` P2P binding as the source for global IPv6 destinations — Rule 5 ("prefer outgoing interface") doesn't apply. WireGuard, AmneziaWG and OpenVPN don't hit this because they install their v6 vip with a real `/64` prefix from the start. v1.0.5.21 has the helper, after `swanctl --initiate` succeeds on Darwin, parse the local v6 traffic-selector from the swanctl output, find the utun that has it bound, and re-bind the same address with `prefixlen 64` instead of `/128` P2P. macOS source-selection then picks the vip automatically and outbound v6 connections without `-S` work normally. Best-effort: any failure is non-fatal — the tunnel is up, the user just loses auto-source-selection (workaround: explicitly bind the vip). Linux + Windows are gated out (different IPSec stacks, no `/128` P2P issue).
- **v1.0.5.20** *(macOS-only behaviour fix; all desktop OSes rebuilt)* — **macOS IPSec import was failing with "Connection refused" against the wrong VICI socket path.** User-reported on importing an IPSec profile via gateway-pull on macOS: `Gateway-Import fehlgeschlagen: invalid config: ipsec configure via swanctl: swanctl --load-all: connecting to 'unix:///var/run/charon.vici' failed: Connection refused`. Homebrew strongSwan 6.0.6 ships swanctl with a compile-time default VICI URI of `unix:///var/run/charon.vici` but the same Homebrew install actually runs charon and binds its socket at `unix:///opt/homebrew/var/run/charon.vici`. Without an explicit `--uri` flag, swanctl connects to the wrong default path and fails even though charon is running fine on the real socket. The helper's own charon-running check was already correctly locating the actual socket among the three candidate Homebrew/local/system paths, but the swanctl invocations downstream did not pass `--uri`. Fix: a new `helperMacOSSwanctlArgs(args)` wrapper auto-appends `--uri unix://<detected-path>` to every swanctl invocation on Darwin — applied to all four call sites in the helper (`--load-all`, `--initiate`, `--terminate`, `--list-sas`). Linux and Windows are unaffected (Linux uses a matching default; Windows does not use swanctl).
- **v1.0.5.19** *(Windows-only behaviour change; all desktop OSes rebuilt)* — **One-click Windows IPSec setup at gateway-pull time.** The Privycs Gateway already serves a per-IPSec-connection Windows setup script — a polyglot `.cmd/.ps1` that fully provisions the Windows RAS VPN: PKCS#12 client cert and CA imported to the LocalMachine cert store, `Add-VpnConnection` with `MachineCertificate` + `-SplitTunneling`, roughly 300 `Add-VpnConnectionRoute` entries computed server-side as the complement of the peer's bypass-network set (Windows IKEv2 has no native ExcludedRoutes; the complement pattern restores bypass semantics), IPv6 split-tunnel via the `2000::/3` complement, `rasphone.pbk` patching for `DisableClassBasedDefaultRoute`/`IpInterfaceMetric`/DNS-pin, and `/32` host-routes for the VPN DNS servers. v1.0.5.16 already fetched this script but only parsed out the route directives and ran them post-rasdial — cert install + `Add-VpnConnection` + `rasphone.pbk` tweaks still went through the legacy client-side path with all its IKE-AUTH and sibling-profile race-condition pain. v1.0.5.19 hands the whole setup off to the script: during a gateway-pull of an IPSec profile on Windows, the privileged helper writes the script to `%ProgramData%\PrivycsVPN\setup-<name>-<ts>.cmd` with SYSTEM+Administrators ACL, invokes `cmd.exe /c <path>` so the polyglot detects its mode and self-loads as PowerShell with `-ExecutionPolicy Bypass`, captures stdout/stderr with PKCS#12-base64 regex-redaction before logging, and `defer`-cleans the file on every exit path (the script contains cert material so cleanup is mandatory). After install the Windows RAS VPN is fully configured — the user clicks Connect, the existing skip-check in `configureWindows` sees the connection already exists, `rasdial` connects, and the v1.0.5.16 post-rasdial route install becomes an idempotent no-op. Failure is non-fatal: the legacy client-side path remains as fallback and the connection still works (just without bypass / IPv6 routing for that session). Scope is narrow and explicit — only during `DownloadAndImportConfig`, only for IPSec, only on Windows. macOS / Linux helpers reject the IPC with a "Windows-only" structured error. No gateway change.
- **v1.0.5.18** *(Windows-only behaviour fix; all desktop OSes rebuilt)* — **Three Windows bugs surfaced by a v1.0.5.17 IPSec connect test.** **(1) IPv6 killswitch failed with "Mindestens ein Adresspräfix ist ungültig".** v1.0.5.15 kept a single block rule on `-RemoteAddress '::/0'`; the cmdlet's validator refuses any range that mathematically includes loopback addresses (same restriction that earlier rejected `::1` in v1.0.5.14). Fixed by enumerating the four non-loopback IPv6 prefixes that together cover every routable destination address except `::1/128` and `::/128`: `2000::/3` (Global Unicast) + `fc00::/7` (Unique Local) + `fe80::/10` (Link-Local) + `ff00::/8` (Multicast), comma-separated in a single rule, each prefix validated independently. **(2) IPSec routes batch failed with "The filename or extension is too long".** v1.0.5.16 inlined all 300+ `Add-VpnConnectionRoute` calls into one PowerShell `-Command` invocation; the rendered command line was ~24 KB which exceeds Windows' CreateProcess command-line length limit. Fixed by writing the script to a temp `.ps1` (UTF-8 with BOM) and invoking via `powershell -File <path>` instead of `-Command <inline>`. **(3) Connection-card pill bled across the screen on long endpoint hostnames.** v1.0.5.17 wrapped the badge text in `<span class="truncate">` inside an `inline-flex` button capped at `max-w-[220px]`, but Tailwind's `truncate` utility emits `overflow:hidden + text-overflow:ellipsis + white-space:nowrap` and NOT `min-width:0`. Flex items default to `min-width:auto` (= intrinsic content size), so the span refused to shrink below its full text width and visually overflowed the button into the bottom-nav menu. Fixed with `min-w-0` on the span, `overflow-hidden` on the button as a backstop, and `shrink-0` on the protocol icon.
- **v1.0.5.17** *(cross-platform UI polish)* — **Connections-list badges show the endpoint host instead of the protocol name.** The per-protocol pill badges under each profile entry on the Connections screen now show the server hostname (port stripped — a tooltip on Desktop reveals the full `host:port`) instead of the static text "WireGuard"/"AmneziaWG"/"OpenVPN"/"IPSec" — the logo + colour already convey the protocol, so the text slot is now used for the more informative endpoint. IPv6 literals in `[…]` form are kept intact (port stripped after the closing bracket). Empty server-address (rare — happens before the first Configure run for hand-picked file imports) falls back to the previous protocol-name label on Desktop and to logo-only on Android. Scope is connections-list only: Connect-screen badges and the gateway-browser panel deliberately keep the prior look. Same change shipped to both Desktop and Android.
- **v1.0.5.16** *(Windows-only behaviour fix; all desktop OSes rebuilt)* — **Two Windows-IPSec fixes bundled.** **(1) Bypass + IPv6 routes from the gateway companion .cmd.** User-reported on Windows: an IPSec profile connected via MachineCertificate authentication routed IPv4 through the VPN correctly but lost IPv6 entirely and ignored excluded-networks (home/LAN subnets that should bypass the tunnel). Root cause: Windows IKEv2 with MachineCertificate honours only the FIRST traffic-selector returned by the server (a 0.0.0.0/0 default), discarding the second TS and every ExcludedRoutes/IncludedRoutes block in the .mobileconfig (Apple-NE-specific fields Windows does not parse). The gateway already exposes a parallel Windows-companion endpoint that returns a PowerShell .cmd with 300+ explicit `Add-VpnConnectionRoute` directives — 121 IPv4 complement-CIDRs (the bypass set inverted) + 183 IPv6 CIDRs + DNS-host routes. v1.0.5.16 has the client fetch this .cmd at gateway-pull import time, store it on the connection record, then at connect time parse out the CIDRs (Option B — never execute the .cmd directly because it also re-imports certificates) and ask the privileged helper to install them via a new IPC command that enables split-tunneling and adds each route (~5-8 s for 300 routes; runs in the background so the connect UI returns immediately). Best-effort: failures here never roll back the connect — the tunnel stays up, just without bypass/IPv6 routing for that session if anything goes wrong. **(2) Connect-detection speedup.** User-reported: "Windows verbindet IPSec rasend schnell — die App braucht ewig". rasdial blocks until the IKE_AUTH SA is fully established, so when our Windows IPSec Up() returns the tunnel is genuinely up — but the app's connect path polled `proto.Status().Connected` every 250 ms until it saw true, and Status() on Windows IPSec spawns a `Get-VpnConnection` PowerShell each call (200-500 ms PS startup). The poll loop accumulated 1-5 s of user-visible "Connecting…" latency. Fix: upWindows now opens a 5-second "trust window" on success during which Status() short-circuits to `Connected=true` without spawning PowerShell. The poll loop exits on the very next 250 ms tick — UI transitions to Connected within ~1 s end-to-end of user click. After 5 s the field expires and Status() resumes the live PS query so a silent tunnel drop is still caught by the status emitter and tunnel-health monitor.
- **v1.0.5.15** *(Windows-only behaviour fix; all desktop OSes rebuilt)* — IPv6 killswitch: dropped the allow-loopback rule entirely. v1.0.5.14 switched the rule installation from netsh to PowerShell to avoid the legacy netsh parser rejecting bare IPv6 literals; users then reported the PowerShell variant fails with a different error ("Es wurde eine nicht definierte Multicast-, Broadcast- oder Loopback-IPv6-Adresse angegeben", HRESULT 0x80070057) because the New-NetFirewallRule cmdlet's own validator refuses loopback addresses (`::1`, `127.0.0.1`) as `-RemoteAddress` by-design. Per Microsoft's own documentation Windows Defender Firewall does not filter loopback traffic anyway — the kernel routes ::1 internally before WFP sees the packet, so a "block ::/0 outbound" rule does NOT block loopback connections to `::1` despite the address mathematically falling inside the range. The allow-loopback rule was therefore redundant on Windows; v1.0.5.15 drops it entirely. The killswitch remains effective (external IPv6 traffic blocked while a v4-only tunnel is up; localhost-bound IPv6 services keep working).
- **v1.0.5.14** *(Windows-only behaviour fix; all desktop OSes rebuilt)* — **IPv6 killswitch on Windows: switched from netsh advfirewall to PowerShell `New-NetFirewallRule`.** User-reported on German-locale Windows that every IPSec connect surfaced "IPv6-Leak-Schutz fehlgeschlagen: netsh add allow-loopback ... Eine angegebene IP-Adresse oder ein angegebenes Adressschlüsselwort ist ungültig". v1.0.5.13 already simplified the rule-name (single-token ASCII, no parens, no spaces) and the IP parameters (dropped `/128` prefix, dropped redundant defaults) but the user upgraded and saw the identical error — ruling out the cosmetic-rule-name hypothesis. The genuine cause is that the legacy netsh advfirewall parser on some Windows builds rejects bare IPv6 literals (`::1`, `::/0`) at the `remoteip=` keyword regardless of how the rest of the rule is shaped. v1.0.5.14 sidesteps this entirely by driving the modern Windows Defender Firewall via PowerShell's `New-NetFirewallRule` cmdlet, which calls the COM-backed Firewall API where IPv6 is a first-class citizen. PowerShell 5.1 is OS-bundled on every Windows 10+ install, so no new dependency. `ipv6UnblockWindows` cleans up all four legacy rule-name shapes (pre-v1.0.5.13 long names, v1.0.5.13 short names, v1.0.5.14 short names) so the upgrade leaves no orphan rules behind.
- **v1.0.5.13** *(superseded by v1.0.5.14; all desktop OSes rebuilt)* — First attempt at the IPv6-killswitch netsh-error fix: simplified the rule-name to single-token ASCII and stripped redundant defaults from the netsh argument list. Did not solve the user-reported error; superseded by v1.0.5.14 which switches to PowerShell entirely. See v1.0.5.14 for the working fix.
- **v1.0.5.12** *(CI hotfix; all desktop OSes rebuilt)* — Build-system fix. v1.0.5.9's bump of `actions/setup-go` v5 → v6 introduced an unannounced behaviour change: setup-go v6 sets `GOTOOLCHAIN=local` by default whereas v5 left it unset and let Go auto-download the toolchain version declared in go.mod. `desktop/go.mod` has required `go 1.25.0` since v0.9.15.42 — under v5 the build runtime auto-downloaded 1.25; under v6 with `GOTOOLCHAIN=local`, that auto-download is disabled and the build aborted at the first `go run` step. v1.0.5.10 didn't surface the regression because it had no `desktop/**` changes (path-gate skipped). v1.0.5.11 was the first desktop-affecting release after the v6 bump and broke all three OS builds. v1.0.5.12 bumps the workflow `GO_VERSION` env from `'1.23'` to `'1.25'` to match `go.mod`'s stated requirement. No source code change in v1.0.5.12 — only CI config + version-file bumps so the desktop-release workflow rebuilds v1.0.5.11's source rather than propagating stale v1.0.5.1 artefacts.
- **v1.0.5.11** *(Windows BSOD class fix; all desktop OSes rebuilt)* — **Critical Windows fix: WireGuard disconnect no longer races the kernel.** A user reported the Desktop client BSOD'ing Windows during multi-protocol failover; investigation reproduced the long-known v0.9.10.29 race that was previously fixed for the kill-switch sinkhole path but had crept back into the TunnelHealth failover path with an insufficient 2-second settle delay, and was missing entirely from several other Disconnect callers (Connect's pre-switch teardown, tray quit, pool member switch, network-rule disconnect). The fix moves the guarantee into the helper's WireGuard disconnect path itself: after `wireguard.exe /uninstalltunnelservice` (or the AmneziaWG SCM stop+delete) returns, the helper now actively polls the Service Control Manager until the per-tunnel service entry is fully dropped — the downstream signal that the wintun.sys driver cleanup (NDIS unbind, WFP filter removal, adapter ref-count drop, service-process exit) has completed. Cap at 3 seconds; 250 ms driver-async grace after service-gone. Typical Windows disconnect now takes ~500-800 ms (vs ~50 ms previously); the trade-off is half a second of latency for not BSOD'ing the user's machine. Linux + macOS get an identical-signature no-op stub (the cleanup race is a Windows-specific concern) and are rebuilt only for version-file consistency since `app.go` is cross-platform.
- **v1.0.5.1** *(all desktop OSes)* — Visual fix: the master toggle's subtitle on the Network Rules screen was rendered in a generic gray that turned unreadable on the primary-tinted card background (user-reported "light gray text on green background"). The subtitle now pairs to the card's own colour — Material's `on<container>` colour at 75 % alpha when the engine is on, neutral gray when the engine is off and the card switches back to its disabled-grey background. Same fix applied to both desktop and Android clients. No functional changes.
- **v1.0.5** *(all desktop OSes)* — Two cross-platform parity changes. **(1) On-Demand & Network Rules screen — prominent master toggle.** The Network Rules screen now opens with a prominent primary-coloured master toggle pinned at the top. When OFF, the auto-tunnel engine no-ops — no rule below can fire — and the VPN only connects/disconnects when you click Connect manually. When ON, the rule list runs as before. The legacy configurable Connect-on-Demand fallback was removed from this screen in favour of a static "default behaviour" info card at the bottom, matching the Android client's layout exactly. Both clients now have a single consistent on-demand UI: master toggle → rules → static "no rule matched → manual control only" note. **(2) IPv6-leak warning banner on the Connect screen.** A red banner explaining that IPv6 traffic may bypass the VPN was already being computed by the helper when IPv6-killswitch enforcement failed, but no UI subscriber existed on the desktop side, so the warning silently vanished. The banner now renders under the connect-state widget on every desktop OS, in all six languages — the same banner the Android client has shown since v1.0.0. Backend ↔ frontend was migrated to a stable-key + translate-on-render pattern so future warnings can be added without each one being a separate hard-coded English string.
- **v1.0.4** *(Windows-affecting; all desktop OSes rebuilt)* — Two fixes. **(1) Windows IPSec multi-profile, definitive fix.** v1.0.2 added a cert-store sweep before each IKEv2 dial so Windows would not pick the wrong machine certificate when two profiles share an issuing CA; v1.0.3 widened the sweep to also catch pre-v1.0.2 certs by Issuer DN. v1.0.4 fixes the PowerShell that drove the sweep: `Import-PfxCertificate` on a `.p12` containing a leaf + intermediate CA returns an *array* of certificate objects, and the v1.0.3 `Thumbprint -ne $myThumb` test silently devolved into a scalar-vs-array comparison that PowerShell evaluates as "the remainder of the array elements that differ" — non-empty and therefore truthy, which made every cert in the store (including the just-imported leaf) pass the filter and get swept. After the sweep, the store had no Privycs cert at all and Windows fell back to whatever stale credential it could find → IKE-Auth error 13801. The sweep now uses `-notin @($thumbs)` for proper containment, identifies the leaf certificate (`Issuer ≠ Subject`) so the issuer-pick targets the right cert, and emits diagnostic lines into the privileged-helper log so a future failure has actionable trace. **(2) Bottom-navigation language reactivity.** The bottom tab bar (Connect / Configs / Add / Settings / Help) was driven by a hard-coded English label array, so a language switch in Settings updated every screen *except* that bar. The labels now come from a reactive `useI18n()` binding and re-render the moment a different language is picked. New short-form translations land in all six languages.
- **v1.0.3** *(Windows-affecting; all desktop OSes rebuilt)* — Multi-profile IPSec hardening on Windows. The v1.0.2 cert-store sweep only matched certificates that *we* had tagged with `FriendlyName = "Privycs IPSec - <slot>"`, so leftover certs from pre-v1.0.2 installs went unnoticed and Windows kept picking them at IKE-Auth time. The sweep now additionally removes any cert in `LocalMachine\My` whose Issuer DN matches the just-imported leaf, which catches the legacy untagged certs and any concurrent sibling-profile certs from the same issuing CA. The unhelpful "configuration cached, skipping" short-circuit was also removed on Windows: the Windows cert store is global state that sibling profiles mutate between our sessions, so our singleton cache cannot model it — every connect now re-runs the helper cleanup. Cross-platform note: only Windows is affected, because Linux + macOS bind the IPSec cert by an explicit file path (swanctl) or by an explicit `PayloadCertificateUUID` (`.mobileconfig`) — neither platform has Windows' "auto-pick by EKU+chain" behaviour. v1.0.4 hardened the PowerShell that drives the sweep itself; see above.
- **v1.0.2** *(Windows-only behaviour change; all desktop OSes rebuilt)* — First attempt at the Windows multi-profile IPSec fix. The Windows `Add-VpnConnection` cmdlet's `-AuthenticationMethod MachineCertificate` does not let the caller pin a specific certificate — Windows scans `LocalMachine\My` at dial time and picks the first cert matching the EKU and trust chain. When two Privycs IPSec profiles shared an issuing CA, the second profile's dial would use the first profile's cert, the gateway rejected the IKE_AUTH and the user saw the multi-protocol failover jump to WireGuard. v1.0.2 added a per-connect "cert sweep" that removes prior Privycs-managed certs from `LocalMachine\My` before importing the new one, and tags the new cert with a recognisable FriendlyName for the next sweep. v1.0.3 + v1.0.4 close two gaps in this sweep that v1.0.2 left open — see above.
- **v1.0.0.1** *(all desktop OSes)* — Build-time fix: the v1.0.0 binaries shipped without the ed25519 public key that the Pro license-key verifier needs, so any "Activate license" attempt returned "This app cannot validate license keys". The build pipeline now reads the public key from a CI secret and bakes it into every desktop release. When the secret is unset the verifier still fails closed; nothing else about the v1.0.0 behaviour changed.
- **v1.0.0** *(all desktop OSes)* — **First 1.0 milestone, four headline changes.** **(1) Encryption-at-rest.** Every Privycs-managed file in your app data folder that can contain a VPN private key is now stored on disk as AES-256-GCM ciphertext: settings, the connection registry, your pool configurations, and the protocol config files (`*.conf`, `*.ovpn`, `*.sswan`, `*.p12`). The 32-byte master key is protected by your operating system's secret store — macOS Keychain on macOS, the Data Protection API (DPAPI) on Windows, GNOME Keyring / KWallet via libsecret on Linux (with a file fallback on headless systems) — so a stolen disk image, an unattended backup, or another user on the same machine cannot read your VPN keys. On first run after the update an automatic migration encrypts existing plaintext files in place with a `.bak` rollback if anything goes wrong; no user action is required. Files that external binaries must read directly (OpenVPN's `.ovpn`) are transparently decrypted into a short-lived plaintext runtime copy that is removed at disconnect, so the persistent-on-disk state stays encrypted. **(2) Multi-profile reliability fixes.** Switching between two IPSec configs on Windows no longer collides on a single shared phonebook entry — each config now uses its own derived connection name. OpenVPN log files and PID files are likewise now per-profile, so switching between profiles cannot misread or kill the wrong daemon. **(3) Full in-app language switcher across six languages.** Beyond the existing English, German, Spanish and French, the desktop app now ships **Italian and Portuguese**, with a Language selector in Settings that takes effect immediately without restart. Localisation spans every screen including the new ones described above. **(4) Pro tier scaffolding.** A new "Privycs Pro" entry under Settings opens the upgrade screen with a one-time-purchase / cross-platform-bundle option and a license-key activation flow. All six Pro-only features (multi-protocol per connection, multi-config per slot, network rules, gateway download, connection pools, per-pool split tunnel) are wired with the gate-on-Pro logic in place — but **gating is globally off in v1.0.0**, so every feature remains free in this release. We will flip the switch in a later release once the LemonSqueezy purchase flow is publicly active. License verification is offline ed25519 — once gating is on, your one-time-purchase key works without any phone-home.
- **v0.9.15.73** *(all desktop OSes)* — **Connect-on-Demand and Network Rules are now one screen.** The simple on-demand settings (network trigger + the except/only SSID list) and the per-network Rules engine used to live in two separate places with no indication of which one wins — Rules are always checked first and the simple settings act as the fallback. They are now unified into a single **"On-Demand & Network Rules"** screen that states the precedence outright: a header explains the order, the rules-engine enable toggle moved here from Settings, the rule list is checked top to bottom (first match wins), and a **"Default behaviour"** card at the bottom holds the former Connect-on-Demand config inline for any network no rule matched. The evaluation engine is unchanged — this is purely a UI reorganisation so the rules-first → default-fallback order is visible. Settings now has a single "On-Demand & Network Rules" entry instead of two separate cards. Cross-platform: the same unification ships on Android in v0.9.15.73.
- **v0.9.15.70** *(all desktop OSes)* — **User-configurable protocol failover order.** When a connection holds multiple protocols (e.g. AmneziaWG + WireGuard + OpenVPN fallback), failover used to walk them in a fixed order (AmneziaWG → WireGuard → OpenVPN → IPSec). A new "Protocol Failover Order" card in Settings lets you reorder the four protocol classes with up/down arrows; the chosen order is read by the multi-protocol failover path on every recovery, so the next try after a tunnel failure follows your preference. The default preserves the previous behaviour, and any protocol you don't move stays in its enum position. Cross-platform: the same setting is also new on Android in v0.9.15.70.
- **v0.9.15.69** *(all desktop OSes)* — Connect-on-Demand reliability hardening, ported from the Android audit. **(1) Concurrency:** the network monitor's evaluation could run from several triggers at once (an immediate platform-event check plus its deliberate 2-second follow-up, the 60-second safety poll, and settings-change re-evaluations), with no serialization — a data race on the rule-transition tracker and unserialized connect/disconnect calls that could intermittently make the wrong decision. All triggers now feed a single, ordered, debounced evaluator goroutine, so exactly one evaluation runs at a time (the deliberate 2-second follow-up is preserved). **(2) Startup grace:** in "except"-mode Wi-Fi rules an unresolved Wi-Fi name is treated as "untrusted → connect" by design, but in the first seconds after launch the name simply hasn't populated yet (D-Bus/WlanAPI lag), so the app could briefly auto-connect on a Wi-Fi that is actually on your trusted list; the first few seconds are now conservative and defer until the network is identified, after which the documented steady-state behaviour resumes. The Wi-Fi *classification* and SSID-read paths on desktop use native OS APIs and were already correct (the Android-specific classification/redaction bugs from the same audit do not apply here).
- **v0.9.15.62** *(Windows-affecting; all desktop OSes rebuilt)* — Fixed an AmneziaWG connection failure where reconnecting to the already-active config produced a tunnel that sent traffic but received none (the WireGuard handshake never completed and the health monitor failed it over). A *different* AmneziaWG config and plain WireGuard worked in the same session, and the same config worked fine on Android — so it was specific to the desktop "reconnect current config" path. Root cause: the AmneziaWG-specific config file (and its obfuscation parameters) is only produced when a config is *selected*; the reconnect-current-config path skipped that step and launched a stale/wrong config file, so the server rejected the handshake. The connect path now always re-renders the active config from its current stored content (and resolves the correct AmneziaWG file) immediately before bringing the tunnel up — matching the behaviour of the selection, failover and startup paths (and the Android client, which always uses live config content). Pool members are unaffected (they use a separate adopt-existing path).
- **v0.9.15.58** — Fixed a config-loss bug present since the multi-config refactor (all platforms). A connection that already held one WireGuard config silently lost it when a second WireGuard config was added: the import "update vs. append" decision matched on `(protocol, filename)` alone, so two genuinely different configs that merely shared a filename (e.g. two manually-imported `wg0.conf` files, or two QR scans) were treated as the same slot and the second overwrote the first. The filename-fallback now additionally requires byte-identical config content — same name + different content appends as a new config (the intended "multiple endpoints per connection" behaviour), while re-importing the exact same file still updates in place with no duplicate build-up. Same-named additional configs get an auto disambiguating label in the protocol-pill switcher.
- **v0.9.15.56** *(Windows-only)* — Completes the v0.9.15.54 Interactive Service work. The client connected to `\\.\pipe\openvpn\service` but the service refused the start with `code=0x00000543 OpenThreadToken` (ERROR_CANT_OPEN_ANONYMOUS). The OpenVPN Interactive Service impersonates the calling pipe client to decide which user to spawn openvpn.exe as; the go-winio pipe dial defaulted to anonymous impersonation level, so the service's `ImpersonateNamedPipeClient` + `OpenThreadToken` failed and it rejected the request — the helper then fell back to the legacy direct spawn and hit the DCO netsh IPv6 fatal again. Fix: dial the pipe at SecurityImpersonation level (`DialPipeAccessImpLevel`), exactly as openvpn-gui / OpenVPN Connect do. With this, the Interactive Service accepts the start and openvpn runs with `msg_channel != 0`, routing all netsh/route ops through the service's working implementation. Environment confirmed: the OpenVPN Interactive Service is installed and running (standard MSI default); only the pipe impersonation level was wrong.
- **v0.9.15.54** *(Windows-affecting; Linux + macOS functionally unchanged)* — Structural fix for the entire OpenVPN-2.7.1-DCO-on-Windows-26200 netsh failure class. Spawning openvpn.exe directly gave `msg_channel=0`, so OpenVPN ran every privileged operation (DNS, IPv6 address, routes) through its own direct-netsh code — broken on Windows 26200 (`set dns`+duplicate `add dns`, then `netsh interface ipv6 set address` failing with error 1, …). Filtering each broken netsh call individually was unbounded whack-a-mole. The official OpenVPN-GUI and OpenVPN Connect never spawn openvpn.exe themselves — they ask the OpenVPN Interactive Service to do it, which spawns openvpn with `--msg-channel <handle>` so OpenVPN delegates every privileged op back to the service's own (working) implementation. Privycs now does the same: a new minimal client for the community OpenVPN 2.x Interactive Service control pipe (`\\.\pipe\openvpn\service`), wire protocol cross-checked against OpenVPN's `interactive.c` (server) and openvpn-gui's reference client. Falls back to the legacy direct spawn when the interactive service isn't installed/running (older OpenVPN installs). One structural fix resolves DNS + IPv6-address + route netsh failures together. The v0.9.15.53 `--pull-filter ignore "dhcp-option DNS"` + helper-applied DNS is kept as belt-and-braces (proven path, lowest risk). Recommended OpenVPN on Windows: the standard community installer with the Interactive Service enabled (the MSI default).
- **v0.9.15.53** *(Windows-affecting; Linux + macOS functionally unchanged from v0.9.15.50)* — Stop fighting OpenVPN's broken Windows DNS code path; set the tunnel DNS ourselves. On Windows 10.0.26200 all three OpenVPN-2.7.1 drivers are broken: DCO 2.8.2 applies a single pushed `dhcp-option DNS <ip>` as `netsh ... set dns` immediately followed by `netsh ... add dns` for the *same* server (Windows 26200 rejects the duplicate `add`, OpenVPN treats the netsh failure as fatal); the `--disable-dco` fallback (v0.9.15.48) lands on TAP-Windows6 9.27 which has its own media-state-up bug; `--windows-driver wintun` (v0.9.15.50) is a dead no-op because OpenVPN 2.7 removed Wintun support entirely and silently falls back to DCO. Same gateway config works on Linux openvpn 2.x and Android ics-openvpn — the defect is strictly the Windows-2.7.1 netsh-DNS code. Fix mirrors the proven macOS swanctl DNS-override pattern: launch openvpn.exe with `--pull-filter ignore "dhcp-option DNS"` so it never runs the broken netsh sequence (the raw PUSH_REPLY is still logged so the intended DNS is recoverable), then a new privileged-helper action applies the tunnel DNS with a single clean `netsh interface ip set dns ... static` (never `add` → no duplicate). No restore needed — the ovpn-dco/tap adapter is ephemeral and drops its DNS on disconnect. DCO data-plane offload stays enabled; only its netsh-DNS path was broken and is now bypassed.
- **v0.9.15.50** *(Windows-affecting; Linux + macOS byte-equivalent to v0.9.15.48)* — Windows OpenVPN driver selection switched to `--windows-driver wintun` (replaces the v0.9.15.48 `--disable-dco` workaround). Layered bug story on Windows 26200 + OpenVPN 2.7.1: DCO 2.8.2 hits a `netsh ... add dns` duplicate-DNS-add error after TLS handshake (v0.9.15.45 baseline); `--disable-dco` worked around that but fell back to TAP-Windows6 9.27 which has its own media-state-up bug on Windows 26200 (TUN/TAP never reports "up", routes never install); `--windows-driver wintun` explicitly selects the Wintun adapter (shipped by the OpenVPN-Windows installer since 2.5.x) which brings itself up reliably and uses a netsh code path without the duplicate-add quirk. Linux + macOS unaffected by either Windows-only DCO/TAP bug. **Caveat:** OpenVPN 2.7 has deprecated `--windows-driver wintun` and removed Wintun support in some builds; users on Windows 26200 may need to install OpenVPN 2.6.x or wait for an upstream fix. The Android part of this tag was an experimental overlay-pattern for ICS-OpenVPN that did not fully fix the symptom; the actual Android fix is in v0.9.15.51.
- **v0.9.15.48** — Windows OpenVPN: `--disable-dco` added to the privileged-helper spawn args to work around the OpenVPN 2.7.1 + DCO 2.8.2 `netsh ... add dns` duplicate-DNS bug on Windows 26200. Forces fallback to the older TAP-Windows6 adapter (later revisited in v0.9.15.50). Linux + macOS byte-equivalent to v0.9.15.46.
- **v0.9.15.46** — Desktop-side bundled with the Android `wireguard-android` Maven-drop release. Two desktop-only fixes here: (1) IPv6 leak killswitch now correctly short-circuits on dual-stack tunnels — pre-fix, `tunHasIPv6` called `net.ParseIP` on the raw `LocalAddress` string which can contain a comma-separated v4+v6 list (e.g. `10.x.y.z/32, fdXX::Y/128`); `ParseIP` returned nil, classification fell to "v4-only", killswitch fired, and the Windows netsh rule install failed because of a separate bug; (2) **Pill-switch reconnect-when-connected** — `SelectConfig` / `SelectProtocol` now auto-disconnect + reconnect through the new protocol slot if a tunnel is currently up, atomic under `tunnelMu`. Matches user expectation that clicking a pill while connected switches the live tunnel, not just the next-connect slot.
- **v0.9.15.45** — Windows AmneziaWG traffic counters: switched to per-interface counter readouts via `getWindowsTrafficStats` (`GetIfEntry2` over `iphlpapi.dll`) keyed by the wintun adapter name. The v0.9.15.44 helper-response fix had cleaned up the connected-state detection but inadvertently removed the rx/tx bytes from the status payload (the official `amneziawg-windows` tunnel.Run owns its own UAPI pipe and we no longer dump it through the helper).
- **v0.9.15.42** — Windows AmneziaWG: delegated the tunnel implementation to upstream `amnezia-vpn/amneziawg-windows` `tunnel.Run` instead of our custom per-tunnel SCM service. After many iterations (v0.9.15.25–.40) chasing netsh races, named-pipe security descriptors, DNS-induced bind disruption and other implementation gotchas of the custom service, the pragmatic fix was to stop implementing the Windows AmneziaWG tunnel ourselves. Credit added to the Amnezia VPN team in the file header.
- **v0.9.15.40** — Windows AmneziaWG diagnostic landing: `winipcfg.SetDNS` replaces a netsh call that was being torn down by IPv6-killswitch's parallel netsh activity (race on `Tcpip6\Parameters`). Plus explicit Windows security-descriptor on the AWG status named pipe so the user-context Privycs app could read it without ACL issues.
- **v0.9.15.39** — Windows AmneziaWG: address + routes installed via `winipcfg.LUID` instead of `netsh` (eliminates a class of "address not found on interface" races). Plus a defensive "pipe-before-bringUp" ordering so the AWG status pipe is listening before the tunnel comes up.
- **v0.9.15.36** — Diagnostic-only Windows-AmneziaWG release while we collected a clean trace to identify why the tunnel-up sequence hung between "applying AWG UAPI" and any subsequent log line. (Per the standing "stop after 3 failed guess-fixes — demand trace before next attempt" rule.) Root cause analysis fed into v0.9.15.40 and v0.9.15.42.
- **v0.9.15.35** — Privycs-VPN privileged-helper now self-heals its Windows Firewall UDP allow-rule for AmneziaWG on every helper startup (was only run from the installer code path in v0.9.15.34, missed by binary-replace upgrades).
- **v0.9.15.34** — Windows AmneziaWG tunnel was crashing with `ERROR_PROCESS_ABORTED` because there was no Windows Firewall rule allowing the AWG UDP bind for `privycs-vpn.exe`. Vanilla WireGuard wasn't affected (its MSI installer registers its own firewall rule for `wireguard.exe`); AmneziaWG ships no Windows tunnel-service installer. Fix: register the rule in the privileged helper.
- **v0.9.15.31** — On-disk WireGuard/AmneziaWG conf-file paths use the slot stable-ID (`gw-<protocol>-<configId>`). v0.9.15.30's UUID-filename change set the in-data identifier but the on-disk path was still using the sanitized connection name; multiple gateway-downloaded WG slots on the same connection clobbered each other's file.
- **v0.9.15.30** — UUID-based gateway-stable identifier in both `ProtocolConfig.id` and on-disk filename so config re-imports map cleanly to existing slots. Plus tunnel-health-probe failover threshold tightened from 3×30 s to 3×10 s — a dead tunnel now fails over to the next protocol within ~30 s instead of ~3 min. Plus a refactor of the Windows AmneziaWG per-tunnel SCM service hardening.
- **v0.9.15.29** — Multi-config filename collision avoidance and per-config verify-phase tolerance. When a connection holds multiple configs of the same protocol type, each gets its own slot-stable file on disk; verify-phase budget extended so slow IKE_SA negotiation isn't killed mid-handshake.
- **v0.9.15.28** — Windows AmneziaWG vs vanilla WireGuard conf-file separation: AWG configs land in `<name>-amneziawg.conf` so they cannot clobber an existing vanilla `<name>.conf` on disk for the same connection name. Both variants share the same interface identity at runtime — only the on-disk file path differs.
- **v0.9.15.26** — Windows AmneziaWG now fails loudly when the privileged helper is unreachable. Pre-fix, the path fell through to the vanilla-WireGuard UAC-elevated fallback (`wireguard.exe /installtunnelservice`) which has no AmneziaWG-go equivalent and would silently install a vanilla tunnel.
- **v0.9.15.25** — Windows AmneziaWG routing: `route.exe` doesn't accept CIDR for IPv4 nor `-6` for IPv6 — replaced with `route ADD <ip> MASK <mask>` (v4) and `netsh interface ipv6 add route` (v6); set wintun metric=1 so the AWG `/0` route wins the routing tiebreaker against the physical default. Plus a desktop verify-phase failover (extends multi-protocol failover to the initial verify window — was only firing on mid-session tunnel death).
- **v0.9.15.18** — AmneziaWG logo brand-mark silhouette used consistently in the protocol pill, connection-list rows, and the Connect button. Multi-config-picker UX refinements.
- **v0.9.15.6** — Tunnel-state mutation serialised behind a new `tunnelMu` `sync.Mutex`. Race shapes covered: user-disconnect during `proto.Up`'s long-running native call; rapid Connect re-taps (NDIS teardown for WG, IKE-SA delete for IPSec, management-socket close for OVPN — each up to 5 s); NetworkMonitor recovery + user-disconnect concurrency; `SelectConfig` mid-connect. `Connect` refactored into public-wrapper + `connectInternal` so pool paths can call internal while holding the mutex (Go's `sync.Mutex` is non-reentrant).
- **v0.9.15.4** — **AmneziaWG promoted to a first-class 4th protocol** (was a runtime variant of WireGuard). Separate import-time content detection — `Jc`, `Jmin`, `Jmax`, `S1-S4`, `H1-H4`, `I1-I5` keys identify the obfuscation profile at save time. Slot in the protocol enum ahead of WireGuard, entry in the failover preference order. Plus **multi-config-per-protocol-per-connection**: a single connection can hold any number of `ProtocolConfig` entries, including multiples of the same protocol type (e.g. WG-UDP + WG-TCP endpoints to the same server). Failover walks the full ordered-configs list. Same model as Android v0.9.15.4 — pools remain separate-and-distinct from this (Pools are for VPN providers; multi-config is for one own server with multiple ways to reach it).
- **v0.9.14.96** — **IPv6 leak killswitch** (always-on, no setting). All three desktop OSes get a privileged-helper firewall block: Linux ip6tables in a custom `PRIVYCS_V6_KS` chain, macOS pf anchor `privycs-v6-killswitch`, Windows Firewall named rules. Loopback (`::1`) always exempted. Triggered automatically when the tunnel is IPv4-only AND the OS has live IPv6 connectivity; cleared on disconnect (success + error path). **Plus** a startup-cleanup pass on every app boot that clears any orphan rules left by a previously-crashed session — failsafe in the secure direction (worst case: brief IPv6 outage on first boot, never an unprotected leak). **Plus** UI surfacing: helper-RPC failure now emits a `vpn:ipv6_leak_warning` Wails event + system Notify(NotifyError) so the user sees a banner instead of being silently unprotected.
- **v0.9.14.95** — SAFETY: UI silent-fail detection. Two-tick debounce in App.Status() — if `a.connected==true` but the protocol's authoritative status check disagrees for two consecutive 2-second polls (~4 s), force `a.connected=false`. Prevents the UI from lying when the IPSec SA expired silently or charon crashed mid-session.
- **v0.9.14.94** — macOS IPSec disconnect-hang fix: `swanctl --terminate` now bounded by a 3-second `CommandContext` timeout. If charon is in a zombie state and won't ack the terminate, the timeout kills swanctl and falls through to `ipsec stop` (SIGTERM the daemon directly).
- **v0.9.14.93** — macOS IPSec disconnect: kill charon entirely (`ipsec stop`) instead of only `swanctl --terminate`. Charon's userspace IKE_SA / DPD timers can otherwise reinstall kernel SAs via rekey between our flushes.
- **v0.9.14.90** — macOS charon-restart: also flush kernel SADB (`setkey -F`) and SPD (`setkey -FP`) between stop and start. Without this, orphan kernel SAs from the dead charon survived and routed traffic — fresh charon's `swanctl --list-sas` showed 0 bytes while user's traffic still flowed via the orphan SA.
- **v0.9.14.78** — Tunnel-health pill defensive gate: pill now hidden whenever `connected == false`, even if the singleton monitor's state-flow still holds a stale value. Closes a UI glitch where the pill could remain visible after disconnect if the connected→disconnected transition's stop-event was missed. **In-app Help screen** added: 5th bottom-bar icon opens a native-rendered view of this very document, fetched live from `www.privycs.com/docs/desktop-client.md`.
- **v0.9.14.77** *(Android only — desktop unchanged)* — task-swipe-from-recents survival on aggressive Android OEMs.
- **v0.9.14.76** *(Android only — desktop unchanged)* — widget v6 + bullet-proof "{" defenses.
- **v0.9.14.75** *(Android only — desktop unchanged)* — Per-App-VPN system apps + foreground keepalive.
- **v0.9.14.73** — Connect-on-Demand bullet-proofing: the "Only these SSIDs" / "Except these SSIDs" rules now refuse to act on an empty/unknown SSID (was silently treating empty as "no match" and letting connects through that should have been blocked). Default-route classifier got VPN-aware so a tunnel-driven utun/tun/wg/ipsec interface is no longer mis-counted as "ethernet" when reading network type.
- **v0.9.14.66** — **Multi-protocol failover.** When a connection has more than one protocol config (e.g. one WireGuard `.conf` + one IPSec `.sswan` under the same connection name), the tunnel-health monitor's recovery loop rotates protocols on persistent failure: WireGuard → IPSec → OpenVPN → repeat with 5-min cooldown if all three fail. Plus parallel-tunnel zombie cleanup so a stuck old protocol can no longer block the new one.
- **v0.9.14.64–65** — macOS sleep/wake recovery for IPSec: NSWorkspace power-event hook (cgo Objective-C bridge with `-x objective-c -fobjc-arc` CFLAGS) reconnects within ~1 s of wake instead of waiting for the periodic tunnel-health probe; caffeinate-based display-sleep prevention while a tunnel is up.
- **v0.9.14.62–63** — macOS + Linux IPSec traffic stats now populated from `swanctl --list-sas` byte counters; macOS swanctl conf hardened for sleep/wake survival (autosuspend → off, periodic DPD).
- **v0.9.14.58–61** — macOS IPSec backend pivot: dropped the Apple-Stack/AppleScript path entirely (TCC-blocked on Sequoia for unsigned apps) and made **Homebrew swanctl** the sole macOS backend. Auto-starts charon via `ipsec start` from the privileged helper when needed; PPK + DNS-override + split-tunnel CIDRs all wired through swanctl conf.
- **v0.9.14.49** — macOS Pro IPSec on Apple-Stack path: split-tunnel CIDR routing, RFC 8784 PPK via Homebrew swanctl, DNS-override, lifecycle cleanup. *(Apple-Stack path retired in v0.9.14.58.)*
- **v0.9.14.45** — **Tunnel-health pill + auto-recovery for single connections** (was pool-only). Pill visible at the bottom of the Connect card. Recovery is a disconnect-then-reconnect for single connections; for pools it rotates to the next member. Defensive panic-recovery on macOS network callback to avoid main-loop crashes.
- **v0.9.14.46–47** — Post-quantum IPSec: macOS desktop IPSec generates a `.mobileconfig` from `.sswan` and hands it to Apple's IKEv2 stack (path A retired in v0.9.14.58); Linux/Windows desktop continue with strongSwan/swanctl (path B). RFC 8784 PPK plumbing on Linux + Windows via swanctl.
- **v0.9.14.20–44** — macOS desktop hardening (24 micro-fixes during the v0.9.14.x macOS-OpenVPN/WireGuard/IPSec ride): in-process wireguard-go to replace wg-quick (avoids the LaunchDaemon detection that closed the wireguard-go socket), launchd-PATH-aware binary lookup for Homebrew installs, OpenVPN log+pid pre-creation with mode 0644 so the user app can read them, traffic-stats parsed from `netstat -ibn`, ⌘Q wired through AppMenu, helper-install via temp file + osascript, find Homebrew-installed wg/wg-quick + extend launchd PATH, codesigning + notarization scaffold (dormant until Apple Dev secrets are populated).
- **v0.9.14.4–14** — DNS preset picker (Cloudflare, Quad9, NextDNS, AdGuard, Google) with brand-coloured badges in the dropdown. Available globally (Settings), per-pool, and per-single-connection. Test button probes the resolver over the active tunnel.
- **v0.9.14.0** — Backup schema **v4** carries per-network auto-tunnel rules in encrypted backups; v3 backups still restore cleanly with empty rules.
- **v0.9.13.0–1** — **Per-network rules engine** (Settings → Network Rules) with SSID-, BSSID-, and Mobile-key matching and four action types (disconnect / connect / use-connection / use-pool). BSSID-trust defends against rogue/evil-twin SSID spoofing. **Tunnel-health UI** made visible on Settings + Connect screens.
- **v0.9.13.4** — EMERGENCY desktop kill-switches: rules-engine ping-pong-loop guard, pool keepalive bounded, tunnel-health-recovery gating against COD-off.
- **v0.9.12.0** — **Tunnel Health Monitor (Phase 1)**: periodic ICMP-ping liveness check on desktop (every 60 s), three-strike-out → Recovering, recovery loop drives a disconnect-then-reconnect for single connections / next-member-pick for pools. Three modes: Off / Auto / Active.
- **v0.9.11.68–70** — DNS-Override Tier 1+2: validation, presets, Test button, per-pool override, per-single-connection override, Private-DNS hint banner.
- **v0.9.11.55** — Per-pool client-side split tunnel (WireGuard + OpenVPN, IPv4 + IPv6) with "Exclude private networks" preset.
- **v0.9.11.46** — Pool implementation hardening (18 audit findings closed): synchronous persistence of active/pending member, rotation-alarm sequence counter against manual+scheduled races, Kill-Switch-engaged no longer falsely marks members unreachable, recently-flapping members soft-deprioritised, battery-saver state changes re-arm schedule live, country cache invalidation on network change.
- **v0.9.11.34** — Pool Detail surfaces unreachable members with an amber **Unreachable** badge, a per-row tint, and a header counter ("12 unreachable"). **Reset all** link clears every flag in one persisted write so you do not have to wait for the 30-minute TTL when you know the network came back. Tooltip on each badge carries the failure reason and timestamp.
- **v0.9.11.33** — Pool resilience against dead servers: three-attempt retry loop on `Up()` failure, post-connect WireGuard handshake health-check (poll `latest-handshake` every 500 ms for 5 s), pre-warm reachability probe (DNS + TCP-Dial). Members get a 30-min unreachable TTL; if the whole pool is filtered out, all flags reset to keep the rotator usable through laptop sleep / WiFi switch
- **v0.9.11.32** — Cold-start UX: the Pool indicator card renders on the very first Vue frame via a synchronous `BootstrapState` snapshot + `pool:bootstrap` event, no longer blocked behind the slowest of four IPC calls. Loading toasts surface SelfIP detection ("Detecting your location...") and MMDB country backfill ("Loading exit-point countries X / Y"). Go runtime bumped to 1.25 (1.23 left security support when 1.25 shipped)
- **v0.9.11.31** — Pool rotation tick adopts the pre-written `.conf` instead of writing it again — file write removed from the critical path
- **v0.9.11.30** — Pool pre-warm now writes the next slot's `.conf` to disk 60 s before rotation (in addition to picking the next member), so the rotation tick only does service install + handshake
- **v0.9.11.29** — Pool pre-warm: 60 s ahead of each scheduled rotation the client picks the next member, persists `PendingMemberID`, and the UI shows "Next: <name>" alongside the countdown
- **v0.9.11.27** — Pool slot alternation A/B (each rotation uses the opposite slot — eliminates same-name service-registry races); custom rotation interval in addition to the presets
- **v0.9.11.24** — Stable per-Pool tunnel name (`privycs-pool-<id8>-<slot>`) replaces per-member naming so the OS service registry stays clean across hundreds of rotations
- **v0.9.11.23** — Round-Robin actually rotates the OS tunnel now (previous version only updated metadata; the connect short-circuit on "tunnel already running" is now bypassed by a forced disconnect-then-reconnect cycle)
- **v0.9.11.22** — Pool restored at startup, Welcome screen no longer flashes on cold start, fewer disk writes during rotation
- **v0.9.11.13** — Round-Robin auto-pins to the user's home region (Europe / North America / Asia / etc.) — prevents the rotation from pinballing across continents on each tick
- **v0.9.11.x** — Initial Connection Pool feature: virtual connection wrapping multiple endpoints, three policies (Geo-Nearest, Random, Round-Robin), `.zip` archive import with progress toast, country flag rendering via SVG, "Currently: <member>" indicator on the Connect screen, dedicated Pool Detail view
- **v0.9.10.32** — Pre-connect warning when Connect-on-Demand is enabled but no rule matches the current network — Cancel aborts the connect intent, **Connect anyway** proceeds with the user accepting that COD will tear the tunnel down again
- **v0.9.10.31** — Kill Switch engages on user-initiated disconnect with a 3-second NDIS-settle delay (Windows BSOD avoidance); Pause auto-reconnects after expiry when the user connected manually; OpenVPN and IPSec traffic stats now match across all installed driver variants (variadic adapter-name lookup); public Disconnect cancels an active Pause
- **v0.9.10.30** — Pause UI strengthened (blue pill with PauseIcon + Resume-Now banner) and Pause is now respected by the network-monitor loop so Connect-on-Demand cannot instantly override it; 1 min and 3 min options added
- **v0.9.10.29** — Closed Android-parity gaps: connection-switch with auto-reconnect, hardcore Kill Switch UI lock, per-protocol brand-coloured pills (WireGuard red, OpenVPN orange, IPSec blue), defensive KS re-arm on Status() poll, picker is KS-aware and warns on impossible reconnects
- **v0.9.10.27–28** — SSID detection on locked-down enterprise Windows: four-path lookup ending at WLAN profile XML files, which bypass the Location GPO entirely
- **v0.9.10.13–22** — Cross-platform Sinkhole driver ported from Android (Linux iptables / macOS pf / Windows WFP via privileged helper), Connect-on-Demand overhauled with cross-platform SSID-roam events, locale-independent network-type detection, "Any network" trigger
- **v0.9.10.0** — Initial unified version with WireGuard + OpenVPN + IPSec, encrypted local backup, QR import, system tray, light/dark themes

Complete changelog and release notes on the [GitHub Releases page](https://github.com/hoep/privycs-vpn/releases).
