# Privycs VPN Desktop Client

Cross-platform VPN client for WireGuard, OpenVPN, and IPSec/IKEv2 connections. Built with Wails v2 (Go backend + Vue 3 frontend), compiled into a single native binary.

## Features

### Multi-Protocol Support

The client supports three VPN protocols, each with native system integration:

- **WireGuard** — Uses `wg-quick` on Linux/macOS, `wireguard.exe` on Windows. Fast, modern protocol with state-of-the-art cryptography (ChaCha20, Curve25519).
- **OpenVPN** — Uses the `openvpn` binary with management interface. Flexible protocol with TCP/UDP support, works behind restrictive firewalls.
- **IPSec/IKEv2** — Uses `swanctl` (strongSwan) on Linux, `scutil` on macOS, PowerShell `Add-VpnConnection` on Windows. Native OS support, enterprise standard.

### Multi-Config Management

Each connection can hold multiple protocol configurations for the same server:

- Import `office.conf` (WireGuard) for a connection
- Later add `office.ovpn` (OpenVPN) to the same connection
- Switch between protocols on the connection screen with one click
- The client remembers which protocol was last used per connection

Multiple independent connections are also supported (e.g. Office VPN, Home Server, Corp Gateway), each with their own set of protocol configs.

### Config Import

Import VPN configurations from standard config files:

| Format | Extension | Protocol | Detection |
|--------|-----------|----------|-----------|
| WireGuard | `.conf` | WireGuard | `[Interface]` + `PrivateKey` |
| OpenVPN | `.ovpn` | OpenVPN | `remote` or `<ca>` directives |
| strongSwan | `.sswan` | IPSec | JSON with `remote` field |
| Apple Profile | `.mobileconfig` | IPSec | XML plist payload |

Protocol detection is automatic from file extension and content. Drag-and-drop or file picker.

### Connection Screen

- Large connect/disconnect button with animated status indicator
- Protocol switcher showing all configured protocols for the active connection
- Real-time transfer statistics (download/upload bytes)
- Connection uptime counter
- VPN IP address, server endpoint, and last handshake display
- Kill switch status indicator

### Kill Switch

Prevents network traffic leaks when the VPN disconnects unexpectedly:

- **Linux**: iptables rules that block all traffic except through the VPN interface and loopback
- **macOS**: pf (packet filter) anchor rules (planned)
- **Windows**: WFP (Windows Filtering Platform) rules (planned)

### Settings

- Kill switch toggle
- Auto-connect on startup
- Minimize to tray on close (when connected)
- DNS override
- Routing mode (full tunnel / split tunnel)
- Log viewer with recent daemon entries

### Single Instance

Only one instance of the client can run at a time. Launching a second instance brings the existing window to the foreground.

## Architecture

```
+-------------------------------------------+
|          Wails v2 Window                   |
|  +--------------------------------------+ |
|  |      Vue 3 + TypeScript + Tailwind   | |
|  |      (embedded via go:embed)         | |
|  +---------------------|----------------+ |
|         Wails auto-generated bindings      |
|  +---------------------|----------------+ |
|  |     Go Backend (direct, no IPC)      | |
|  |  - ProtocolManager (WG/OVPN/IPSec)  | |
|  |  - ConnectionRegistry (multi-config) | |
|  |  - KillSwitch (iptables/pf/WFP)     | |
|  |  - Settings (JSON persistence)       | |
|  +--------------------------------------+ |
+-------------------------------------------+
```

There is no separate daemon process. The VPN protocol handlers run directly inside the Wails application. The Vue frontend calls Go methods through auto-generated TypeScript bindings — no IPC, no sockets, no serialization overhead.

## Building

### Prerequisites

- Go 1.23+
- Node.js 20+
- Wails CLI: `go install github.com/wailsapp/wails/v2/cmd/wails@latest`
- Linux: `libgtk-3-dev libwebkit2gtk-4.1-dev` (Debian 13+) or `libwebkit2gtk-4.0-dev` (older)
- Protocol binaries: `wg-quick` (WireGuard), `openvpn` (OpenVPN), `swanctl` (IPSec)

### Build Commands

```bash
# Build for current platform (Linux)
make desktop-build

# Install frontend dependencies only
make desktop-deps

# Development mode with hot reload
make desktop-dev

# Regenerate TypeScript bindings after Go changes
make desktop-bindings

# Publish binary to download server
make desktop-publish
```

### Build Output

The build produces a single binary at `desktop/build/bin/privycs-vpn` (~8.5 MB) that contains the compiled Go backend and the embedded Vue frontend.

### Debian 13 Note

Debian 13 (Trixie) ships `libwebkit2gtk-4.1` instead of `4.0`. The Makefile handles this automatically with the `-tags webkit2_41` build flag.

## Project Structure

```
desktop/
├── main.go                    # Wails app entry, window config, single instance
├── app.go                     # App struct with all frontend-callable methods
├── protocol.go                # VPNProtocol interface, protocol detection
├── protocol_wireguard.go      # WireGuard: wg-quick (Linux/macOS), wireguard.exe (Windows)
├── protocol_openvpn.go        # OpenVPN: openvpn binary with management interface
├── protocol_ipsec.go          # IPSec: swanctl (Linux), scutil (macOS), PowerShell (Windows)
├── connection_registry.go     # Multi-config connection storage (JSON file)
├── settings.go                # App settings, version, platform-specific data dirs
├── killswitch.go              # Network kill switch (iptables/pf/WFP)
├── autoconnect.go             # Auto-connect manager
├── wails.json                 # Wails project config
├── go.mod / go.sum
├── build/
│   └── bin/privycs-vpn        # Built binary
└── frontend/
    ├── src/
    │   ├── App.vue            # Shell with header, router, bottom nav
    │   ├── views/
    │   │   ├── ConnectionView.vue     # Main connect screen with protocol switcher
    │   │   ├── AddConnectionView.vue  # Config import (file picker, drag-drop)
    │   │   ├── ConnectionsView.vue    # Saved connections with protocol badges
    │   │   ├── ProtocolSelector.vue   # Protocol availability overview
    │   │   ├── SettingsView.vue       # App settings
    │   │   └── LogsView.vue          # Log viewer
    │   ├── stores/vpn.ts      # Pinia store with Wails event listener
    │   └── router/index.ts    # Vue Router (hash mode)
    ├── wailsjs/               # Auto-generated TypeScript bindings
    ├── tailwind.config.js
    └── package.json
```

## Data Storage

All persistent data is stored in the platform-specific app data directory:

- **Linux**: `~/.local/share/privycs-vpn/`
- **macOS**: `~/Library/Application Support/privycs-vpn/`
- **Windows**: `%LOCALAPPDATA%\privycs-vpn\`

Files:
- `connections.json` — Saved connections with protocol configs
- `settings.json` — App settings (kill switch, auto-connect, theme)

## CI/CD

The desktop client is built automatically by GitHub Actions on tag push:

- **Workflow**: `.github/workflows/desktop-release.yml`
- **Trigger**: Push tag matching `v*`
- **Artifacts**: `privycs-vpn-linux-amd64` binary + SHA-256 checksum
- **Release**: Uploaded to GitHub Release alongside gateway/agent binaries

## API Reference (Go Methods)

All public methods on the `App` struct are callable from the Vue frontend via auto-generated TypeScript bindings:

| Method | Parameters | Description |
|--------|-----------|-------------|
| `Status()` | — | Returns full connection status |
| `Connect(protocol)` | protocol (optional) | Connect using active or specified protocol |
| `Disconnect()` | — | Disconnect active VPN |
| `SetProtocol(protocol)` | protocol name | Switch active protocol globally |
| `ImportConfig(protocol, content, filename, name, connectionID)` | config details | Import config, optionally to existing connection |
| `ListConnections()` | — | List all saved connections |
| `ActivateConnection(id, protocol)` | connection ID, protocol (optional) | Switch to a saved connection |
| `SwitchConnectionProtocol(protocol)` | protocol name | Switch protocol within active connection |
| `DeleteConnection(id)` | connection ID | Remove a saved connection |
| `RemoveProtocolFromConnection(connectionID, protocol)` | IDs | Remove one protocol from a connection |
| `GetAvailableProtocols()` | — | List system-available protocols |
| `GetSettings()` | — | Get app settings |
| `UpdateSettings(settings)` | settings object | Save app settings |
| `SetKillSwitch(enabled)` | boolean | Toggle kill switch |
| `GetLogs()` | — | Get recent log entries |
| `GetVersion()` | — | Get app version string |

## License

Proprietary. Copyright Privycs.
