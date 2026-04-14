//go:build linux

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

const sudoersPath = "/etc/sudoers.d/privycs-vpn"

// sudoers rules needed for the desktop VPN client to manage tunnels
const sudoersTemplate = `# Privycs VPN Desktop Client — passwordless sudo for VPN operations
%s ALL=(ALL) NOPASSWD: /usr/bin/wg-quick, /usr/bin/wg, /usr/sbin/openvpn, /usr/sbin/swanctl, /usr/sbin/ipsec, /sbin/ip, /usr/sbin/iptables, /usr/bin/tee, /bin/rm, /bin/chmod, /usr/bin/chmod, /bin/mkdir, /usr/bin/mkdir
`

// ensureSudoers checks if the sudoers file exists with correct permissions.
// If missing, uses pkexec (graphical sudo prompt) to install it.
// Returns silently if already configured or on non-Linux platforms.
func ensureSudoers() {
	// Check if file already exists
	if _, err := os.Stat(sudoersPath); err == nil {
		return
	}

	currentUser, err := user.Current()
	if err != nil {
		log.Printf("Sudoers: cannot determine current user: %v", err)
		return
	}

	// Don't configure for root
	if currentUser.Uid == "0" {
		return
	}

	log.Printf("Sudoers: configuring passwordless VPN commands for user %s", currentUser.Username)

	content := fmt.Sprintf(sudoersTemplate, currentUser.Username)

	// Use pkexec for a graphical authentication prompt
	// This shows the OS password dialog once, then configures sudo permanently
	cmd := exec.Command("pkexec", "tee", sudoersPath)
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = nil

	if err := cmd.Run(); err != nil {
		log.Printf("Sudoers: failed to install (user may have cancelled auth prompt): %v", err)
		return
	}

	// Set correct permissions (sudoers files must be 0440)
	chmodCmd := exec.Command("pkexec", "chmod", "0440", sudoersPath)
	chmodCmd.Run()

	log.Printf("Sudoers: installed %s for user %s", sudoersPath, currentUser.Username)
}
