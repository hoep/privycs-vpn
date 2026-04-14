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
		// Linux/macOS: wg show requires root to display transfer stats
		out, err := execHidden("sudo", "wg", "show", w.ifaceName).CombinedOutput()
		if err == nil && len(out) > 0 {
			status.Connected = true
			w.parseWgShowOutput(string(out), &status)
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
	// wg-quick requires the config to be in /etc/wireguard/<name>.conf
	etcConf := filepath.Join("/etc/wireguard", w.ifaceName+".conf")
	if err := copyConfToEtcWithBypass(w.confPath, etcConf); err != nil {
		return fmt.Errorf("failed to install config: %w", err)
	}

	// Use a dedicated timeout context — NOT the Wails app context.
	// Configs with many AllowedIPs (hundreds of subnets) cause wg-quick to
	// run ip route add for each entry, which can take 60+ seconds.
	cmdCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	log.Printf("Running wg-quick up %s (this may take a while with many routes)...", w.ifaceName)
	out, err := execHiddenContext(cmdCtx, "sudo", "wg-quick", "up", w.ifaceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg-quick up failed: %s: %w", string(out), err)
	}
	w.connectedAt = time.Now()
	log.Printf("WireGuard connected via wg-quick (interface: %s)", w.ifaceName)
	return nil
}

func (w *WireGuardProtocol) downUnix(ctx context.Context) error {
	out, err := execHiddenContext(ctx, "sudo", "wg-quick", "down", w.ifaceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("wg-quick down failed: %s: %w", string(out), err)
	}
	// Keep /etc/wireguard/<name>.conf — it will be overwritten on next Connect.
	// Removing it here breaks the edit-save-reconnect flow.
	w.connectedAt = time.Time{}
	log.Printf("WireGuard disconnected")
	return nil
}

// copyConfToEtcWithBypass copies the config to /etc/wireguard/ and injects
// PostUp/PreDown rules for endpoint bypass routing. When AllowedIPs include
// subnets that cover the VPN server's IP, traffic to the server itself would
// go through the tunnel, breaking the connection. The bypass route sends
// server traffic through the default gateway instead.
func copyConfToEtcWithBypass(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	content := string(data)

	// Parse the endpoint IP from the [Peer] section
	endpointIP, endpointIPv6 := parseEndpointIPs(content)

	// Inject PostUp/PreDown bypass routes if not already present
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

		// Insert after the last line of [Interface] section (before [Peer])
		peerIdx := strings.Index(content, "[Peer]")
		if peerIdx > 0 {
			content = content[:peerIdx] + bypassRules + "\n" + content[peerIdx:]
			log.Printf("Injected endpoint bypass routes for %s (IPv6: %s)", endpointIP, endpointIPv6)
		}
	}

	// Ensure /etc/wireguard/ exists
	execHidden("sudo", "mkdir", "-p", filepath.Dir(dst)).Run()
	cmd := execHidden("sudo", "tee", dst)
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo tee %s: %w", dst, err)
	}
	execHidden("sudo", "chmod", "600", dst).Run()
	return nil
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
	wgExe := findWireGuardExe()
	if wgExe == "" {
		return fmt.Errorf("wireguard.exe not found")
	}

	log.Printf("Starting WireGuard tunnel %s via %s", w.ifaceName, wgExe)

	// wireguard.exe /installtunnelservice requires admin privileges.
	psScript := fmt.Sprintf(
		`Start-Process -FilePath '%s' -ArgumentList '/installtunnelservice',('%s') -Verb RunAs -Wait -WindowStyle Hidden`,
		wgExe, w.confPath,
	)
	cmd := execHiddenContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("WireGuard start failed: %s", string(out))
		return fmt.Errorf("wireguard start failed: %s: %w", string(out), err)
	}

	// Wait for the Windows service to actually start (takes 1-3 seconds)
	svcName := "WireGuardTunnel$" + w.ifaceName
	log.Printf("Waiting for service %s to start...", svcName)
	for i := 0; i < 15; i++ {
		time.Sleep(500 * time.Millisecond)
		svcOut, svcErr := execHidden("sc", "query", svcName).CombinedOutput()
		if svcErr == nil && strings.Contains(string(svcOut), "RUNNING") {
			log.Printf("Service %s is running", svcName)
			w.connectedAt = time.Now()
			return nil
		}
	}

	// Service installed but may not be running yet — still count as success
	w.connectedAt = time.Now()
	log.Printf("WireGuard tunnel service installed (may still be starting)")
	return nil
}

func (w *WireGuardProtocol) downWindows(ctx context.Context) error {
	wgExe := findWireGuardExe()
	if wgExe == "" {
		return fmt.Errorf("wireguard.exe not found")
	}

	log.Printf("Stopping WireGuard tunnel %s", w.ifaceName)

	psScript := fmt.Sprintf(
		`Start-Process -FilePath '%s' -ArgumentList '/uninstalltunnelservice',('%s') -Verb RunAs -Wait -WindowStyle Hidden`,
		wgExe, w.ifaceName,
	)
	cmd := execHiddenContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", psScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("WireGuard uninstall output: %s, error: %v", string(out), err)
	}

	// Wait for service to stop
	svcName := "WireGuardTunnel$" + w.ifaceName
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		svcOut, _ := execHidden("sc", "query", svcName).CombinedOutput()
		if !strings.Contains(string(svcOut), "RUNNING") {
			break
		}
	}

	w.connectedAt = time.Time{}
	log.Printf("WireGuard tunnel %s stopped", w.ifaceName)
	return nil
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
