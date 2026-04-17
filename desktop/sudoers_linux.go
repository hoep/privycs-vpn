//go:build linux

package main

import (
	"log"
	"os"
)

const sudoersPath = "/etc/sudoers.d/privycs-vpn"

// ensureSudoers removes the legacy NOPASSWD sudoers file that earlier versions
// installed for passwordless VPN commands. The privileged helper service now
// handles all privileged operations through its IPC socket — the sudoers
// approach is obsolete and unnecessarily broad (NOPASSWD on tee/chmod/mkdir
// could let a compromised client overwrite arbitrary files as root).
//
// If the legacy file exists and the helper is reachable, we ask the helper
// to remove it. Otherwise we leave it alone (harmless — nothing invokes sudo
// anymore).
func ensureSudoers() {
	if _, err := os.Stat(sudoersPath); err != nil {
		return
	}
	log.Printf("Sudoers: legacy NOPASSWD file at %s detected, requesting cleanup via helper", sudoersPath)
	client := NewHelperClient()
	if !client.IsHelperReachable() {
		log.Printf("Sudoers: helper not reachable — legacy file left in place (not used at runtime)")
		return
	}
	resp, err := client.SendCommand("remove_legacy_sudoers", nil)
	if err != nil {
		log.Printf("Sudoers: cleanup via helper failed: %v", err)
		return
	}
	if resp.Success {
		log.Printf("Sudoers: legacy file removed")
	}
}
