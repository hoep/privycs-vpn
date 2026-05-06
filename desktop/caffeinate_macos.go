//go:build darwin

package main

// Prevent-display-sleep helper — wraps macOS's `caffeinate` tool to
// keep the display + idle awake while a VPN tunnel is up. Opt-in via
// the PreventDisplaySleep setting (default OFF). Use case: privacy/
// stability-first users who want zero sleep-related VPN drops at the
// cost of battery life.
//
// We deliberately use the public `caffeinate` CLI rather than calling
// IOPMAssertionCreateWithName directly: caffeinate is a stable binary
// shipped with every macOS, and binding to its lifecycle is a single
// child-process pattern we already use for OpenVPN. No cgo, no
// power-assertion-leak risk on crash, no entitlement surprises.

import (
	"log"
	"os/exec"
	"sync"
)

var (
	caffeinateMu  sync.Mutex
	caffeinateCmd *exec.Cmd
)

// startCaffeinate spawns `caffeinate -di` if not already running.
//
//	-d  prevent display sleep
//	-i  prevent idle sleep (system-level)
//
// We don't add -s (system sleep) because that wouldn't help: on
// MacBooks the user can still close the lid and get system sleep, and
// blocking that would just confuse them. Display+idle is the right
// granularity: keep the screen on while tunnel is up, accept lid-close
// as an explicit "I want to sleep" signal.
//
// Idempotent — multiple calls during a single tunnel lifetime are a
// no-op after the first.
func startCaffeinate() {
	caffeinateMu.Lock()
	defer caffeinateMu.Unlock()
	if caffeinateCmd != nil && caffeinateCmd.Process != nil {
		return
	}
	cmd := exec.Command("/usr/bin/caffeinate", "-di")
	if err := cmd.Start(); err != nil {
		log.Printf("Caffeinate: failed to start: %v", err)
		return
	}
	caffeinateCmd = cmd
	log.Printf("Caffeinate: started (pid %d) — display + idle sleep prevented while tunnel is up", cmd.Process.Pid)
	// Reaper goroutine — without this the zombie sticks around on
	// SIGTERM-via-stopCaffeinate until the parent exits.
	go func() {
		_ = cmd.Wait()
	}()
}

// stopCaffeinate kills the caffeinate child if running. Idempotent —
// calling on an already-stopped or never-started instance is a no-op.
// Called on user-initiated disconnect, on tunnel-death recovery
// (after the new tunnel comes up startCaffeinate fires again), and
// implicitly on app exit via the OS reaping the child process group.
func stopCaffeinate() {
	caffeinateMu.Lock()
	defer caffeinateMu.Unlock()
	if caffeinateCmd == nil || caffeinateCmd.Process == nil {
		return
	}
	if err := caffeinateCmd.Process.Kill(); err != nil {
		log.Printf("Caffeinate: kill failed: %v", err)
	} else {
		log.Printf("Caffeinate: stopped (pid %d)", caffeinateCmd.Process.Pid)
	}
	caffeinateCmd = nil
}
