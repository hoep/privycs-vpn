# Privycs VPN — Desktop Client

Cross-platform VPN client for **AmneziaWG, WireGuard, OpenVPN and
IPSec/IKEv2** connections. Built with Wails v2 (Go backend + Vue 3
frontend), compiled into a single native binary for Windows, macOS and
Linux.

See the [repository README](../README.md) for the project overview.

## Features

### Multi-protocol

Four VPN protocols, each with native system integration:

- **WireGuard / AmneziaWG** — kernel WireGuard on Linux, the WireGuard
  tunnel service on Windows, and an in-process `wireguard-go` on macOS.
  AmneziaWG is the DPI-resistant WireGuard variant, handled through the
  same path with the obfuscation parameters applied.
- **OpenVPN** — the `openvpn` binary driven over its management
  interface. TCP/UDP, works behind restrictive firewalls.
- **IPSec/IKEv2** — `swanctl` (strongSwan) on Linux, the native macOS
  IPSec stack, and the Windows IKEv2 stack. Enterprise standard.

### Multi-config connections

Each connection can hold several protocol configs for the same server:

- Import `office.conf` (WireGuard) for a connection
- Later add `office.ovpn` (OpenVPN) to the *same* connection
- Switch protocol on the connection screen with one click
- The client remembers the last protocol used per connection

Multiple independent connections are supported, each with its own set of
protocol configs.

### Config import

| Format | Extension | Protocol | Detection |
|--------|-----------|----------|-----------|
| WireGuard / AmneziaWG | `.conf` | WireGuard | `[Interface]` + `PrivateKey` |
| OpenVPN | `.ovpn` | OpenVPN | `remote` or `<ca>` directives |
| strongSwan | `.sswan` | IPSec | JSON with a `remote` field |

Protocol detection is automatic from the file extension and content.
Drag-and-drop or file picker.

### Connection screen

- Large connect / disconnect button with an animated status indicator
- Protocol switcher showing every configured protocol for the connection
- Live transfer statistics (download / upload)
- Uptime counter, VPN IP, server endpoint and last-handshake display
- Kill-switch status indicator

### Kill switch

Blocks network traffic leaks when the VPN drops unexpectedly, enforced at
the OS firewall level:

- **Linux** — `iptables` rules permitting traffic only through the VPN
  interface and loopback
- **macOS** — `pf` (packet filter) anchor rules
- **Windows** — WFP (Windows Filtering Platform) rules

### Settings

Kill-switch toggle, auto-connect on startup, minimize-to-tray, DNS
override, routing mode (full / split tunnel) and a log viewer.

### Single instance

Only one instance runs at a time; launching a second brings the existing
window to the foreground.

## Architecture

```
+-------------------------------------------+
|              Wails v2 Window              |
|  +-------------------------------------+  |
|  |     Vue 3 + TypeScript + Tailwind   |  |
|  |        (embedded via go:embed)      |  |
|  +------------------|------------------+  |
|         Wails auto-generated bindings     |
|  +------------------|------------------+  |
|  |       Go Backend (direct, no IPC)   |  |
|  |   - protocol handlers (WG/OVPN/IPSec)| |
|  |   - ConnectionRegistry (multi-config)| |
|  |   - KillSwitch (iptables/pf/WFP)    |  |
|  |   - privileged helper (root ops)    |  |
|  +-------------------------------------+  |
+-------------------------------------------+
```

There is no separate daemon process — the protocol handlers run inside
the Wails application. The Vue frontend calls Go methods through
auto-generated TypeScript bindings: no IPC, no sockets, no serialization
overhead. Operations that need elevated privileges go through a small
privileged helper service.

## Building

### Prerequisites

- Go 1.23+
- Node.js 20+
- Wails CLI — `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- Linux — `libgtk-3-dev` and `libwebkit2gtk-4.1-dev` (Debian 13+) or
  `libwebkit2gtk-4.0-dev` (older)
- Protocol tools available on `PATH` — `wg`/`wg-quick`, `openvpn`,
  `swanctl`

### Build commands

```bash
cd desktop

go build ./...     # fast compile check of the Go backend (Linux)
wails build        # full app build  -> ./build/bin/privycs-vpn
wails dev          # hot-reload development mode
```

On Debian 13 (Trixie), which ships `libwebkit2gtk-4.1`, add the build
tag: `wails build -tags webkit2_41`.

Release builds for all three desktop platforms are produced by CI; a
local `wails build` is only needed for development.

## Project structure

```
desktop/
  main.go                  Wails entry, window config, single instance
  app.go                   App struct — all frontend-callable methods
  protocol*.go             VPNProtocol interface + per-protocol handlers
  wg_macos*.go             in-process wireguard-go for macOS
  connection_registry.go   multi-config connection storage (JSON)
  killswitch*.go           OS-level kill switch (iptables / pf / WFP)
  privileged_helper.go     privileged helper service (root operations)
  frontend/                Vue 3 + TypeScript + Tailwind UI
    src/views/             Connect, Connections, Settings, Logs, ...
    src/stores/            Pinia stores
    wailsjs/               auto-generated TypeScript bindings
```

## Data storage

Persistent data lives in the platform app-data directory:

- **Linux** — `~/.local/share/privycs-vpn/`
- **macOS** — `~/Library/Application Support/privycs-vpn/`
- **Windows** — `%LOCALAPPDATA%\privycs-vpn\`

`connections.json` holds saved connections and protocol configs;
`settings.json` holds app settings. Stored secrets are encrypted at rest.

## CI/CD

`.github/workflows/desktop-release.yml` builds Linux, macOS and Windows
artifacts on a `v*` tag push and attaches them to the GitHub Release.

## License

**GPL-3.0** — see [LICENSE](../LICENSE). Part of the Privycs VPN project;
the combined work is GPL-3.0 because the mobile client links GPL-2.0
libraries. See the repository README for the full licensing rationale.
