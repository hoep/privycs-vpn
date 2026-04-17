package main

import (
	"log"
	"runtime"
	"strings"
	"sync"
)

// KillSwitch prevents network traffic when VPN is disconnected.
// Uses iptables on Linux, pf on macOS, WFP on Windows.
type KillSwitch struct {
	mu      sync.Mutex
	enabled bool
	active  bool
}

// NewKillSwitch creates a new KillSwitch instance
func NewKillSwitch() *KillSwitch {
	return &KillSwitch{}
}

// IsEnabled returns whether the kill switch is configured
func (ks *KillSwitch) IsEnabled() bool {
	return ks.enabled
}

// Enable turns on the kill switch (will activate on next connect)
func (ks *KillSwitch) Enable() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.enabled = true
	log.Println("Kill switch enabled")
}

// Disable turns off the kill switch completely.
// Always runs platform cleanup — even if the in-memory 'active' flag is
// false — to catch stale firewall rules left behind by app crashes or
// aborted Connect() attempts. The platform deactivate is idempotent
// (deleting non-existent rules is a no-op on all OSes) so this is safe
// to call unconditionally.
func (ks *KillSwitch) Disable() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.deactivatePlatform()
	ks.enabled = false
	ks.active = false
	log.Println("Kill switch disabled")
}

// Activate applies firewall rules to block non-VPN traffic
func (ks *KillSwitch) Activate() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if !ks.enabled || ks.active {
		return
	}
	ks.activatePlatform()
	ks.active = true
	log.Println("Kill switch activated")
}

// Deactivate removes the firewall rules.
// Always runs platform cleanup regardless of the in-memory 'active' flag.
// This catches stale rules from a crashed previous run AND ensures a failed
// Connect() attempt can always restore network access.
func (ks *KillSwitch) Deactivate() {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	wasActive := ks.active
	ks.deactivatePlatform()
	ks.active = false
	if wasActive {
		log.Println("Kill switch deactivated")
	}
}

func (ks *KillSwitch) activatePlatform() {
	// Try privileged helper first (no sudo/password prompts)
	client := NewHelperClient()
	if client.IsHelperReachable() {
		log.Println("Kill switch: using privileged helper to enable")
		resp, err := client.SendCommand("killswitch_enable", nil)
		if err == nil && resp.Success {
			log.Println("Kill switch: enabled via helper")
			return
		}
		log.Printf("Kill switch: helper failed (%v / %s), falling back to direct", err, resp.Error)
	}

	// Fallback: direct execution (requires sudo on Linux/macOS)
	switch runtime.GOOS {
	case "linux":
		ks.activateLinux()
	case "darwin":
		ks.activateMacOS()
	case "windows":
		ks.activateWindows()
	}
}

func (ks *KillSwitch) deactivatePlatform() {
	// Try privileged helper first (no sudo/password prompts)
	client := NewHelperClient()
	if client.IsHelperReachable() {
		log.Println("Kill switch: using privileged helper to disable")
		resp, err := client.SendCommand("killswitch_disable", nil)
		if err == nil && resp.Success {
			log.Println("Kill switch: disabled via helper")
			return
		}
		log.Printf("Kill switch: helper failed (%v / %s), falling back to direct", err, resp.Error)
	}

	// Fallback: direct execution
	switch runtime.GOOS {
	case "linux":
		ks.deactivateLinux()
	case "darwin":
		ks.deactivateMacOS()
	case "windows":
		ks.deactivateWindows()
	}
}

// Linux: iptables rules
func (ks *KillSwitch) activateLinux() {
	// First clean up any stale rules from previous crash/kill
	ks.deactivateLinux()

	commands := [][]string{
		{"sudo", "iptables", "-I", "OUTPUT", "-o", "lo", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		// Allow all WireGuard interfaces (privycs-sh-*, wg*, etc.)
		{"sudo", "iptables", "-I", "OUTPUT", "-m", "comment", "--comment", "privycs-ks", "-o", "privycs+", "-j", "ACCEPT"},
		// Allow tun interfaces (OpenVPN)
		{"sudo", "iptables", "-I", "OUTPUT", "-m", "comment", "--comment", "privycs-ks", "-o", "tun+", "-j", "ACCEPT"},
		// Allow VPN protocol ports (WireGuard, OpenVPN, IKE, ESP)
		{"sudo", "iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "51820", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"sudo", "iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "51821", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"sudo", "iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "51822", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"sudo", "iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "51823", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"sudo", "iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "1194", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"sudo", "iptables", "-A", "OUTPUT", "-p", "tcp", "--dport", "1194", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"sudo", "iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "500", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"sudo", "iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "4500", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		// Allow DHCP (needed to maintain WiFi lease)
		{"sudo", "iptables", "-A", "OUTPUT", "-p", "udp", "--dport", "67:68", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		// Allow local network (for gateway/DNS resolution)
		{"sudo", "iptables", "-A", "OUTPUT", "-d", "10.0.0.0/8", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"sudo", "iptables", "-A", "OUTPUT", "-d", "192.168.0.0/16", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		{"sudo", "iptables", "-A", "OUTPUT", "-d", "172.16.0.0/12", "-j", "ACCEPT", "-m", "comment", "--comment", "privycs-ks"},
		// Block everything else
		{"sudo", "iptables", "-A", "OUTPUT", "-j", "DROP", "-m", "comment", "--comment", "privycs-ks"},
	}
	for _, cmd := range commands {
		execHidden(cmd[0], cmd[1:]...).Run()
	}
}

func (ks *KillSwitch) deactivateLinux() {
	// Remove all rules with the privycs-ks comment
	for {
		out, err := execHidden("sudo", "iptables", "-L", "OUTPUT", "--line-numbers", "-n").CombinedOutput()
		if err != nil {
			break
		}
		found := false
		for _, line := range splitLines(string(out)) {
			if contains(line, "privycs-ks") {
				fields := splitFields(line)
				if len(fields) > 0 {
					execHidden("sudo", "iptables", "-D", "OUTPUT", fields[0]).Run()
					found = true
					break // Line numbers shift, restart
				}
			}
		}
		if !found {
			break
		}
	}
}

// macOS: pf anchor rules
// Creates a pf anchor "privycs_ks" that blocks all traffic except loopback and VPN interfaces.
// Uses /etc/pf.anchors/privycs_ks for the rule file and loads it via pfctl.
func (ks *KillSwitch) activateMacOS() {
	anchorFile := "/etc/pf.anchors/privycs_ks"
	rules := "# Privycs Kill Switch — block all non-VPN traffic\n" +
		"pass on lo0 all\n" +
		"pass on utun0 all\n" +
		"pass on utun1 all\n" +
		"pass on utun2 all\n" +
		"pass on utun3 all\n" +
		"pass out proto udp to any port 51820\n" + // WireGuard
		"pass out proto udp to any port 1194\n" + // OpenVPN UDP
		"pass out proto tcp to any port 1194\n" + // OpenVPN TCP
		"pass out proto udp to any port 500\n" + // IKE
		"pass out proto udp to any port 4500\n" + // IKE NAT-T
		"pass out proto esp all\n" + // IPSec ESP
		"block drop all\n"

	// Write anchor file
	cmd := execHidden("sudo", "tee", anchorFile)
	cmd.Stdin = strings.NewReader(rules)
	if err := cmd.Run(); err != nil {
		log.Printf("Kill switch: failed to write pf anchor: %v", err)
		return
	}

	// Load anchor into pf
	commands := [][]string{
		{"sudo", "pfctl", "-a", "privycs_ks", "-f", anchorFile},
		{"sudo", "pfctl", "-e"}, // enable pf if not already
	}
	for _, cmd := range commands {
		if err := execHidden(cmd[0], cmd[1:]...).Run(); err != nil {
			log.Printf("Kill switch: pf command %v failed: %v", cmd, err)
		}
	}
	log.Println("Kill switch: macOS pf anchor activated")
}

func (ks *KillSwitch) deactivateMacOS() {
	// Flush the anchor rules and remove the anchor file
	execHidden("sudo", "pfctl", "-a", "privycs_ks", "-F", "all").Run()
	execHidden("sudo", "rm", "-f", "/etc/pf.anchors/privycs_ks").Run()
	log.Println("Kill switch: macOS pf anchor deactivated")
}

// Windows: netsh advfirewall rules
// Creates firewall rules that block all outbound traffic except loopback and VPN protocols.
// Uses rule names prefixed with "PrivycsKS-" for easy identification and cleanup.
func (ks *KillSwitch) activateWindows() {
	// Clean any stale PrivycsKS-* rules from a previous run first.
	// Without this, repeated activations create duplicate rule entries.
	// Linux already does this via deactivateLinux() at the top of activateLinux().
	ks.deactivateWindows()

	commands := [][]string{
		// Allow loopback
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-Loopback", "dir=out", "action=allow", "remoteip=127.0.0.0/8"},
		// Allow WireGuard (UDP 51820)
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-WireGuard", "dir=out", "action=allow", "protocol=udp", "remoteport=51820"},
		// Allow OpenVPN (UDP+TCP 1194)
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-OpenVPN-UDP", "dir=out", "action=allow", "protocol=udp", "remoteport=1194"},
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-OpenVPN-TCP", "dir=out", "action=allow", "protocol=tcp", "remoteport=1194"},
		// Allow IKE (UDP 500, 4500)
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-IKE", "dir=out", "action=allow", "protocol=udp", "remoteport=500"},
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-IKE-NATT", "dir=out", "action=allow", "protocol=udp", "remoteport=4500"},
		// Block all other outbound
		{"netsh", "advfirewall", "firewall", "add", "rule", "name=PrivycsKS-BlockAll", "dir=out", "action=block"},
	}
	for _, cmd := range commands {
		if err := execHidden(cmd[0], cmd[1:]...).Run(); err != nil {
			log.Printf("Kill switch: netsh command failed: %v", err)
		}
	}
	log.Println("Kill switch: Windows firewall rules activated")
}

func (ks *KillSwitch) deactivateWindows() {
	// Remove all PrivycsKS-* rules
	rules := []string{"PrivycsKS-Loopback", "PrivycsKS-WireGuard", "PrivycsKS-OpenVPN-UDP",
		"PrivycsKS-OpenVPN-TCP", "PrivycsKS-IKE", "PrivycsKS-IKE-NATT", "PrivycsKS-BlockAll"}
	for _, name := range rules {
		execHidden("netsh", "advfirewall", "firewall", "delete", "rule", "name="+name).Run()
	}
	log.Println("Kill switch: Windows firewall rules deactivated")
}

// helpers
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func splitFields(s string) []string {
	var fields []string
	field := ""
	for _, c := range s {
		if c == ' ' || c == '\t' {
			if field != "" {
				fields = append(fields, field)
				field = ""
			}
		} else {
			field += string(c)
		}
	}
	if field != "" {
		fields = append(fields, field)
	}
	return fields
}
