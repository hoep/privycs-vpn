# Privycs Connect Guide

Privycs Connect is the VPN client for end-user devices. It is a single binary that handles enrollment, WireGuard tunnel management, NAT traversal, and automatic updates.

---

## What is Privycs Connect?

Privycs Connect is a zero-touch VPN client designed for end users who need to connect to a Privycs-managed VPN without manual WireGuard configuration. Key features:

- **Single binary** -- no dependencies, no installers, no package managers
- **Cross-platform** -- Linux (amd64/arm64), macOS (Intel/Apple Silicon), Windows
- **One-command enrollment** -- contact the gateway, validate a token, receive WireGuard configuration automatically
- **Built-in NAT traversal** -- Direct UDP, STUN, TURN relay, and TCP WebSocket fallback
- **Self-update** -- download and verify new versions from the gateway
- **systemd integration** -- install as a system service for auto-connect on boot

---

## Installation

### Quick install (recommended)

The gateway serves a one-liner installer at `/install-connect.sh`. It detects your OS and architecture, installs the WireGuard dependencies (`wireguard-tools` plus `openresolv`/`resolvconf` for DNS), downloads the matching `privycs-connect` binary, and installs it to `/usr/local/bin/privycs-connect`. Replace `vpn.company.com` with your gateway's address.

```bash
sudo curl -fsSL https://vpn.company.com/install-connect.sh | bash
```

Pass an enrollment token to install **and** enroll in a single step. When a token is supplied and `systemctl` is present, the installer also runs `service install` + `service enable`, so the tunnel auto-starts on boot:

```bash
sudo curl -fsSL https://vpn.company.com/install-connect.sh | bash -s -- <enrollment-token>
```

The script covers Linux (amd64/arm64) and macOS (Intel/Apple Silicon). It installs missing WireGuard tooling automatically via `apt` (Debian/Ubuntu), `dnf`/`yum` (RHEL/Fedora), `pacman` (Arch), or `brew` (macOS), and must run as root (via `sudo`).

### Manual download

If you prefer to place the binary yourself, download it for your platform, make it executable, and install the WireGuard tools separately (`wireguard-tools` + `openresolv`/`resolvconf` on Linux, `brew install wireguard-tools` on macOS, WireGuard for Windows).

**Linux (amd64)**

```bash
curl -sL https://www.privycs.com/downloads/privycs-connect-linux-amd64 \
  -o /usr/local/bin/privycs-connect
chmod +x /usr/local/bin/privycs-connect
```

**Linux (arm64)**

```bash
curl -sL https://www.privycs.com/downloads/privycs-connect-linux-arm64 \
  -o /usr/local/bin/privycs-connect
chmod +x /usr/local/bin/privycs-connect
```

**macOS (Intel)**

```bash
curl -sL https://www.privycs.com/downloads/privycs-connect-darwin-amd64 \
  -o /usr/local/bin/privycs-connect
chmod +x /usr/local/bin/privycs-connect
```

**macOS (Apple Silicon)**

```bash
curl -sL https://www.privycs.com/downloads/privycs-connect-darwin-arm64 \
  -o /usr/local/bin/privycs-connect
chmod +x /usr/local/bin/privycs-connect
```

**Windows**

Download `privycs-connect-windows-amd64.exe` from `https://www.privycs.com/downloads/` and place it in a directory on your PATH. All commands below that use `privycs-connect` should use `privycs-connect.exe` on Windows. Administrator privileges are required for enrollment and tunnel management (run Command Prompt or PowerShell as Administrator).

---

## Quick Start

The typical workflow for a new user is three commands:

### Step 1: Get an Enrollment Token

An administrator creates an enrollment token in the Privycs dashboard under **Enrollment** (or **Users > Add User**). The token is a long alphanumeric string. The admin provides the token and the gateway URL to the user.

### Step 2: Enroll

```bash
sudo privycs-connect enroll https://vpn.company.com a1b2c3d4e5f6...
```

This contacts the gateway, validates the token, generates WireGuard keys locally, receives an IP allocation and configuration, and saves everything to `/etc/privycs/connect/`.

### Step 3: Connect

```bash
sudo privycs-connect up
```

This brings up the WireGuard tunnel using the saved configuration.

### Step 4: Verify

```bash
sudo privycs-connect status
```

This shows the current connection state, assigned IP address, and gateway information.

---

## Command Reference

> **Privycs Connect runs as root only.** Every command must be run with `sudo` (or as the root user). The client keeps its enrollment state and WireGuard configuration under `/etc/privycs/connect/` (root-owned, `0600`) and manages network interfaces, routing, and DNS — all of which require root. On Windows, run the terminal as Administrator.

### enroll

Registers this device with a Privycs gateway and receives VPN configuration.

```
sudo privycs-connect enroll <gateway-url> <token>
```

**What it does:**

1. Contacts the gateway at the given URL over HTTPS
2. Presents the enrollment token for validation
3. Generates a WireGuard key pair locally (the private key never leaves the device)
4. Sends the public key to the gateway
5. Receives in return: an allocated IP address, the server's public key, endpoint address, DNS servers, allowed IPs, and other WireGuard parameters
6. Saves the enrollment state and WireGuard configuration to `/etc/privycs/connect/`
7. Creates the WireGuard configuration file at `/etc/privycs/connect/privycs0.conf`

**Requires:** root/sudo

**Arguments:**

| Argument | Description |
|---|---|
| `gateway-url` | The HTTPS URL of the Privycs gateway (e.g. `https://vpn.company.com`) |
| `token` | The enrollment token provided by the administrator |

**Example:**

```bash
sudo privycs-connect enroll https://vpn.company.com a1b2c3d4e5f6789012345678
```

**Output on success:**

```
Enrolled successfully.
Gateway:   vpn.company.com
Interface: privycs0
Address:   10.100.20.5/32
DNS:       10.100.10.150
```

**Notes:**

- Enrollment is a one-time operation. The credentials persist across reboots.
- If the device is already enrolled, the command exits with an error. Use `unenroll` first to re-enroll.
- The enrollment token may be single-use or multi-use depending on how the administrator created it.

---

### up

Brings up the VPN tunnel.

```
sudo privycs-connect up [--proxy] [--verbose]
```

**What it does:**

1. Reads the saved WireGuard configuration from `/etc/privycs/connect/privycs0.conf`
2. Creates the WireGuard network interface `privycs0`
3. Applies the configuration (peer, endpoint, allowed IPs, DNS)
4. Sets up routing rules according to the allowed IPs

**Requires:** root/sudo

**Flags:**

| Flag | Description |
|---|---|
| `--proxy` | Enable NAT traversal proxy. The client attempts Direct UDP first, then falls back through STUN, TURN, and TCP tunnel in order. Use this when behind a restrictive firewall or symmetric NAT. |
| `--verbose` | Print detailed connection information including handshake timing, endpoint resolution, and routing setup. |

**Examples:**

```bash
# Standard connection
sudo privycs-connect up

# Connection with NAT traversal
sudo privycs-connect up --proxy

# Connection with verbose output
sudo privycs-connect up --verbose
```

---

### down

Tears down the VPN tunnel.

```
sudo privycs-connect down
```

**What it does:**

1. Removes the WireGuard network interface `privycs0`
2. Cleans up routing rules
3. Restores original DNS configuration

**Requires:** root/sudo

**Example:**

```bash
sudo privycs-connect down
```

The enrollment state and configuration files are preserved. You can run `up` again at any time to reconnect.

---

### status

Shows the current enrollment and connection state.

```
sudo privycs-connect status
```

**What it shows:**

- **Enrolled:** whether the device is enrolled (yes/no)
- **Connected:** whether the VPN tunnel is currently active
- **Gateway:** the gateway URL
- **Interface:** the WireGuard interface name (e.g. `privycs0`)
- **Address:** the assigned VPN IP address
- **Last Connect:** timestamp of the last successful connection

**Requires:** root/sudo. Privycs Connect reads its state and configuration from `/etc/privycs/connect/` (root-owned, `0600`), so every command — including `status` — must run under `sudo` or as root.

**Example output:**

```
Enrolled:     yes
Connected:    yes
Gateway:      vpn.company.com
Interface:    privycs0
Address:      10.100.20.5/32
Last Connect: 2026-03-22 14:30:05 UTC
```

If not enrolled:

```
Enrolled: no
```

---

### update

Checks the gateway for a newer Privycs Connect binary and, if one is published, downloads and installs it in place.

```
sudo privycs-connect update
```

**What it does:**

1. Reads the gateway URL from the device's enrollment state (the device must be enrolled)
2. Calls the gateway's version-check endpoint and compares the running **semantic** version against the published one (build numbers are ignored — the connect and gateway binaries have independent build counters)
3. If they match, prints `Already up to date.` and exits
4. Otherwise downloads the new binary for this OS/architecture
5. Verifies the SHA-256 checksum the gateway reports. This is **mandatory** — if the gateway returns no checksum, or it does not match, the update is refused and nothing is replaced
6. Atomically swaps the binary in place (the previous binary is briefly kept as a `.old` backup and restored automatically if the swap fails)

**Requires:** root/sudo, and the device must already be enrolled.

**Example (already current):**

```
Checking for updates from https://vpn.company.com ...
  Current: v1.4.2
  Latest:  v1.4.2
Already up to date.
```

**Example (update applied):**

```
Checking for updates from https://vpn.company.com ...
  Current: v1.4.2
  Latest:  v1.5.0

Update available. Downloading ...
  Downloaded: 7314216 bytes
  Checksum: verified

Updated successfully: v1.4.2 -> v1.5.0
Restart the VPN connection to use the new version:
  sudo privycs-connect down && sudo privycs-connect up
```

After updating, restart the tunnel so the new binary takes over. If you run it via the systemd service, restart that instead:

```bash
sudo privycs-connect service stop
sudo privycs-connect service start
```

For unattended or fleet updates — or to recover a broken binary without invoking it — use the standalone [`update-connect.sh`](#standalone-updater-update-connect-sh) script instead.

---

### unenroll

Removes all enrollment data and configuration from this device.

```
sudo privycs-connect unenroll
```

**What it does:**

1. Tears down the VPN tunnel if it is active
2. Deletes the WireGuard configuration file (`/etc/privycs/connect/privycs0.conf`)
3. Deletes the enrollment state file (`/etc/privycs/connect/connect.json`)
4. Deletes the WireGuard config JSON (`/etc/privycs/connect/wg-config.json`)
5. Removes the JWT authentication token

**Requires:** root/sudo

**WARNING:** After unenrolling, the device cannot connect to the VPN until it is enrolled again with a new token. The allocated IP address is released and may be reassigned.

**Example:**

```bash
sudo privycs-connect unenroll
```

**Output:**

```
Tunnel stopped.
Configuration removed.
Unenrolled successfully.
```

---

### service

Manages the Privycs Connect systemd service for automatic VPN startup on boot.

```
sudo privycs-connect service <action>
```

**Actions:**

| Action | Description | Requires Root |
|---|---|---|
| `install` | Creates the systemd service unit file at `/etc/systemd/system/privycs-connect.service` and reloads systemd | Yes |
| `uninstall` | Stops and disables the service, removes the unit file | Yes |
| `enable` | Enables the service to start automatically on boot | Yes |
| `disable` | Disables automatic start on boot (does not stop the service) | Yes |
| `start` | Starts the VPN service immediately | Yes |
| `stop` | Stops the VPN service immediately | Yes |
| `status` | Shows the current service status (active, inactive, failed) | Yes |

**Examples:**

```bash
# Install and enable auto-start
sudo privycs-connect service install
sudo privycs-connect service enable
sudo privycs-connect service start

# Check status
sudo privycs-connect service status

# Disable and remove
sudo privycs-connect service stop
sudo privycs-connect service disable
sudo privycs-connect service uninstall
```

**Typical setup for a workstation:**

```bash
sudo privycs-connect enroll https://vpn.company.com TOKEN
sudo privycs-connect service install
sudo privycs-connect service enable
sudo privycs-connect service start
```

The VPN will now connect automatically on every boot.

---

### proxy

Runs the NAT traversal proxy as a standalone process.

```
sudo privycs-connect proxy [--gateway url] [--token token]
```

**What it does:**

Starts a local proxy process that handles NAT traversal for the WireGuard tunnel. This is useful for debugging connectivity issues or running the proxy independently.

The proxy attempts connection methods in this order:

1. **Direct UDP** -- standard WireGuard UDP connection (fastest, lowest overhead)
2. **STUN** -- UDP hole-punching using a STUN server (for cone NAT)
3. **TURN** -- relayed connection through a TURN server (for symmetric NAT)
4. **TCP tunnel** -- WebSocket-based TCP tunnel (works behind the most restrictive firewalls)

**Requires:** root/sudo

**Flags:**

| Flag | Description |
|---|---|
| `--gateway` | Override the gateway URL from the saved config |
| `--token` | Override the authentication token |

**Example:**

```bash
sudo privycs-connect proxy
```

In most cases, you do not need to run the proxy separately. Use `sudo privycs-connect up --proxy` instead, which starts the proxy integrated with the tunnel.

---

### version

Shows version information.

```
sudo privycs-connect version
```

**Requires:** root/sudo (like every Privycs Connect command).

**Example output:**

```
privycs-connect v1.4.2
Build:  287
Commit: a3f8c91
Date:   2026-03-20T10:15:00Z
```

---

## Standalone Updater (`update-connect.sh`)

Alongside the in-app `update` command, the gateway serves a self-contained shell updater at `/update-connect.sh`. It does the same job — version-check, checksum-verified download, atomic binary swap — but as an out-of-band script rather than the binary updating itself. Use it for:

- **Cron / automation** — schedule unattended updates across a fleet
- **Recovery** — replace a broken or partially-written binary that can no longer run its own `update`
- **Fleet rollout** — update many hosts without invoking each binary directly

On an enrolled host the script reads the gateway URL from the device's own state (`/etc/privycs/connect/connect.json`), so no arguments are needed. It works on any systemd-based Linux distribution, stops any running `privycs-connect` / `privycs-connect-daemon` services before swapping the binary, and restarts whichever were active afterwards.

**One-liner:**

```bash
curl -fsSL https://vpn.company.com/update-connect.sh | sudo bash
```

**Or download and run it:**

```bash
sudo ./update-connect.sh
```

**Flags:**

| Flag | Description |
|---|---|
| `--gateway <url>` | Use this gateway instead of the one in the enrollment state (required if the host is not enrolled) |
| `--check` | Report whether an update is available, but do not install it |
| `--force` | Reinstall even when the versions already match |
| `--insecure`, `-k` | Accept a self-signed gateway TLS certificate |
| `-h`, `--help` | Print usage |

The updater refuses to install a binary whose SHA-256 checksum does not match what the gateway reports, and it requires root (run via `sudo`).

---

## Global Flags

These flags can be used with any command.

### --insecure, -k

Skip TLS certificate verification when connecting to the gateway. Use this when the gateway uses a self-signed certificate.

```bash
sudo privycs-connect --insecure enroll https://vpn.local TOKEN
sudo privycs-connect -k up
```

**WARNING:** This disables certificate validation. Use only in development or testing environments, or during initial setup before a valid certificate is provisioned. Do not use in production.

---

## Configuration Files

All configuration is stored in `/etc/privycs/connect/`. The directory is created during enrollment and all files are owned by root with `0600` permissions (readable only by root).

| File | Description |
|---|---|
| `connect.json` | Enrollment state: gateway URL, agent ID, JWT authentication token, enrollment timestamp |
| `wg-config.json` | WireGuard configuration in JSON format: private key, peer public key, endpoint, allowed IPs, DNS, address |
| `privycs0.conf` | Standard WireGuard `.conf` file generated from `wg-config.json`, used by `wg-quick` |

You should not need to edit these files manually. If the configuration becomes corrupted, unenroll and re-enroll:

```bash
sudo privycs-connect unenroll
sudo privycs-connect enroll https://vpn.company.com NEW_TOKEN
```

---

## NAT Traversal

Privycs Connect includes built-in NAT traversal for situations where a direct UDP connection to the VPN server is not possible. Enable it with the `--proxy` flag on the `up` command.

### Connection Methods

The client attempts each method in order and uses the first one that succeeds:

#### 1. Direct UDP (Default)

Standard WireGuard UDP connection to the server's public endpoint. This is the fastest method with the lowest overhead. Works when the client can send UDP packets to the server's listen port (typically 51820).

#### 2. STUN Hole-Punch

Uses a STUN server to discover the client's public IP and port mapping, then attempts UDP hole-punching. Works with most consumer NAT routers (full-cone, restricted-cone, port-restricted-cone NAT). Does not work with symmetric NAT.

#### 3. TURN Relay

Routes traffic through a TURN relay server. Works with symmetric NAT and most firewall configurations. Adds latency due to the relay hop but is reliable. The relay server must be configured in the gateway.

#### 4. TCP Tunnel (WebSocket Fallback)

Encapsulates WireGuard UDP packets inside a TCP WebSocket connection. This works in almost every network environment, including corporate networks that only allow HTTP/HTTPS traffic. Highest overhead of all methods but the most universally compatible.

### Using NAT Traversal

```bash
# Connect with automatic NAT traversal fallback
sudo privycs-connect up --proxy

# With verbose output to see which method was selected
sudo privycs-connect up --proxy --verbose
```

Verbose output example:

```
Trying Direct UDP to 203.0.113.50:51820...
Direct UDP failed (timeout after 5s).
Trying STUN hole-punch via stun.example.com:3478...
STUN hole-punch failed (symmetric NAT detected).
Trying TURN relay via turn.example.com:3478...
TURN relay established.
Tunnel up: privycs0 (10.100.20.5/32) via TURN relay.
```

---

## Troubleshooting

### "Not enrolled"

The device has no enrollment data. Run the enroll command:

```bash
sudo privycs-connect enroll https://vpn.company.com TOKEN
```

If you previously unenrolled, you need a new enrollment token from your administrator.

### "Connection refused" or "Cannot reach gateway"

- Verify the gateway URL is correct and the server is reachable: `curl -k https://vpn.company.com/api/v1/health`
- Check that your local firewall allows outbound HTTPS (port 443)
- If using a proxy, ensure HTTPS traffic is not being intercepted
- Try with `--insecure` if the gateway uses a self-signed certificate

### "Token invalid" or "Token expired"

The enrollment token has expired or has been used the maximum number of times. Request a new token from your administrator.

### "Checksum mismatch" during update

The downloaded binary does not match the expected checksum. This could indicate a network issue or tampered download. Try again:

```bash
sudo privycs-connect update
```

If the problem persists, download the binary manually from `https://www.privycs.com/downloads/` and replace the installed binary.

### DNS not resolving through VPN

- Check that the VPN is connected: `sudo privycs-connect status`
- Verify DNS configuration: `sudo cat /etc/privycs/connect/privycs0.conf | grep DNS`
- On Linux, check that `resolvconf` or `systemd-resolved` is handling DNS correctly
- Try flushing the DNS cache and reconnecting:

```bash
sudo privycs-connect down
sudo privycs-connect up
```

### Split tunnel -- some traffic not going through VPN

The `AllowedIPs` field in the WireGuard configuration controls which traffic is routed through the VPN. This is configured by the administrator on the gateway side. If you need access to additional networks, contact your administrator to adjust the VPN interface settings.

### Tunnel connected but no traffic flowing

- Check the WireGuard handshake: `sudo wg show privycs0`
  - If "latest handshake" is recent, the tunnel is healthy and the issue is likely routing
  - If there is no handshake, the endpoint is unreachable -- try `--proxy` mode
- Check that the server-side interface is up and the peer is configured
- Verify firewall rules on the server allow forwarding

### Connection drops frequently

- Check network stability on your local network
- If behind NAT, enable the proxy: `sudo privycs-connect up --proxy`
- WireGuard uses persistent keepalive to maintain NAT mappings; verify it is configured (the gateway sets this automatically)

### Service fails to start on boot

- Check service status: `sudo privycs-connect service status`
- Check journal logs: `sudo journalctl -u privycs-connect --no-pager -n 50`
- Ensure the service was installed and enabled:

```bash
sudo privycs-connect service install
sudo privycs-connect service enable
```

- Verify that network is available before the service starts. The systemd unit should have `After=network-online.target` (this is set automatically by `service install`).
