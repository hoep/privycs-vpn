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

// WireGuardProtocol implements VPNProtocol for WireGuard connections
type WireGuardProtocol struct {
	confPath    string
	ifaceName   string
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
		// Check for wireguard.exe
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
	_, err := exec.LookPath("wg-quick")
	return err == nil
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
	status := ProtocolStatus{
		Protocol:      "wireguard",
		ServerAddress: w.serverAddr,
		LocalAddress:  w.localAddr,
	}

	if runtime.GOOS == "windows" {
		// On Windows, check if the WireGuard tunnel service is running.
		// sc query does NOT require admin privileges.
		svcName := "WireGuardTunnel$" + w.ifaceName
		out, err := execHidden("sc", "query", svcName).CombinedOutput()
		if err == nil && strings.Contains(string(out), "RUNNING") {
			status.Connected = true
			status.BytesRx, status.BytesTx = getWindowsTrafficStats(w.ifaceName)
		}
	} else {
		// Linux/macOS: query tunnel state via the privileged helper.
		// No direct sudo — that would prompt every 2s during status polls.
		client := NewHelperClient()
		if !client.IsHelperReachable() {
			return status
		}
		resp, err := client.SendCommand("status", map[string]string{
			"protocol":  "wireguard",
			"interface": w.ifaceName,
		})
		if err == nil && resp.Success && len(resp.Output) > 0 {
			status.Connected = true
			w.parseWgShowOutput(resp.Output, &status)
		}
	}

	if status.Connected && !w.connectedAt.IsZero() {
		status.ConnectedAt = w.connectedAt.Format(time.RFC3339)
	}

	return status
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
	// Ensure directory exists
	os.MkdirAll(filepath.Dir(w.confPath), 0700)

	if err := os.WriteFile(w.confPath, cfg, 0600); err != nil {
		return fmt.Errorf("failed to write WireGuard config: %w", err)
	}

	// Derive interface name from config filename — this determines the
	// Windows service name (WireGuardTunnel$<ifaceName>) and the
	// Linux/macOS wg-quick interface name.
	w.ifaceName = strings.TrimSuffix(filepath.Base(w.confPath), ".conf")

	// Parse config for status display
	w.parseConfFile(string(cfg))

	log.Printf("WireGuard config written to %s (tunnel: %s)", w.confPath, w.ifaceName)
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

	// Install the enhanced config into /etc/wireguard via the helper.
	installResp, err := client.SendCommand("wg_install_config", map[string]string{
		"interface": w.ifaceName,
		"content":   enhanced,
	})
	if err != nil {
		return fmt.Errorf("helper install failed: %w", err)
	}
	if !installResp.Success {
		return fmt.Errorf("config install failed: %s", installResp.Error)
	}

	log.Printf("Using privileged helper for wg-quick up %s", w.ifaceName)
	resp, err := client.SendCommand("connect", map[string]string{
		"protocol":  "wireguard",
		"interface": w.ifaceName,
	})
	if err != nil {
		return fmt.Errorf("helper connect failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("wg-quick up failed: %s", resp.Error)
	}
	w.connectedAt = time.Now()
	log.Printf("WireGuard connected via helper (interface: %s)", w.ifaceName)
	return nil
}

func (w *WireGuardProtocol) downUnix(ctx context.Context) error {
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		return fmt.Errorf("privileged helper not running — install it in Settings → Privileged Helper")
	}
	log.Printf("Using privileged helper for wg-quick down %s", w.ifaceName)
	resp, err := client.SendCommand("disconnect", map[string]string{
		"protocol":  "wireguard",
		"interface": w.ifaceName,
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
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}

	// Windows: pass through unchanged.
	if runtime.GOOS == "windows" {
		return string(data), nil
	}

	content := string(data)

	endpointIP, endpointIPv6 := parseEndpointIPs(content)

	if endpointIP != "" && !strings.Contains(content, endpointIP+"/32") {
		bypassRules := fmt.Sprintf(
			"PostUp = ip route add %s/32 $(ip route show default | sed 's/default//') || true\n"+
				"PreDown = ip route del %s/32 || true\n",
			endpointIP, endpointIP)

		if endpointIPv6 != "" {
			bypassRules += fmt.Sprintf(
				"PostUp = ip -6 route add %s/128 $(ip -6 route show default | sed 's/default//') || true\n"+
					"PreDown = ip -6 route del %s/128 || true\n",
				endpointIPv6, endpointIPv6)
		}

		peerIdx := strings.Index(content, "[Peer]")
		if peerIdx > 0 {
			content = content[:peerIdx] + bypassRules + "\n" + content[peerIdx:]
			log.Printf("Injected endpoint bypass routes for %s (IPv6: %s)", endpointIP, endpointIPv6)
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
	if findWireGuardExe() == "" {
		return fmt.Errorf("wireguard.exe not found")
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
		})
		if err != nil {
			return fmt.Errorf("helper connect failed: %w", err)
		}
		if !resp.Success {
			return fmt.Errorf("installtunnelservice failed: %s", resp.Error)
		}
		if err := waitForWGService(w.ifaceName); err != nil {
			log.Printf("WireGuard service wait: %v", err)
		}
		w.connectedAt = time.Now()
		log.Printf("WireGuard connected via helper (service: WireGuardTunnel$%s)", w.ifaceName)
		return nil
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
	if err := waitForWGService(w.ifaceName); err != nil {
		log.Printf("WireGuard service wait: %v", err)
	}
	w.connectedAt = time.Now()
	return nil
}

func (w *WireGuardProtocol) downWindows(ctx context.Context) error {
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
// enter RUNNING state after installtunnelservice. Returns after ~7.5s max.
func waitForWGService(ifaceName string) error {
	svcName := "WireGuardTunnel$" + ifaceName
	for i := 0; i < 15; i++ {
		time.Sleep(500 * time.Millisecond)
		out, err := execHidden("sc", "query", svcName).CombinedOutput()
		if err == nil && strings.Contains(string(out), "RUNNING") {
			return nil
		}
	}
	return fmt.Errorf("service %s did not enter RUNNING state", svcName)
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
