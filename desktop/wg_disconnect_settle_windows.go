//go:build windows

package main

import (
	"log"
	"time"

	"golang.org/x/sys/windows/svc/mgr"
)

// waitForServiceGoneByName polls the Service Control Manager until the
// named service no longer exists, or maxWait elapses. Used after a
// stop+delete sequence to confirm that the kernel-side cleanup
// (driver async I/O, NDIS unbind, WFP filter removal, WireGuard /
// AmneziaWG service-process exit) has completed before the caller
// returns to a path that may immediately start a new tunnel.
//
// The historic Windows BSOD pattern (v0.9.10.29 and a recurrence in
// the v1.0.5.x failover code path) is exactly this race: SCM accepts
// DeleteService and returns success while the kernel is still tearing
// down the per-tunnel adapter; a subsequent CreateAdapter call races
// the lingering NDIS / WFP state and corrupts kernel memory. SCM
// fully dropping the service from its registry is a reliable
// downstream signal that the in-kernel cleanup has completed (because
// SCM holds the service entry alive until the last user-mode handle
// is closed AND the service process has exited).
//
// Returns true on confirmed cleanup, false on timeout (caller logs
// and proceeds — degraded mode, single residual race risk).
func waitForServiceGoneByName(m *mgr.Mgr, name string, maxWait time.Duration) bool {
	const (
		pollInterval = 75 * time.Millisecond
		// Post-disappearance grace for the lowest layer of driver
		// async I/O (NDIS detach packet drain, WFP filter
		// asynchronous removal). Empirically ~150-250 ms is
		// enough on every Windows release we have tested; 250 ms
		// is the safe upper bound.
		driverAsyncGrace = 250 * time.Millisecond
	)

	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		s, err := m.OpenService(name)
		if err != nil {
			// Most likely ERROR_SERVICE_DOES_NOT_EXIST (1060).
			// SCM has fully dropped the service entry — wait the
			// small driver-async grace, then return success.
			time.Sleep(driverAsyncGrace)
			return true
		}
		// Service still registered — close handle and keep
		// polling. Holding the handle open across pollInterval
		// would itself extend the service lifetime.
		s.Close()
		time.Sleep(pollInterval)
	}
	log.Printf(
		"waitForServiceGoneByName: %s did not disappear within %v — continuing in degraded mode",
		name, maxWait,
	)
	return false
}

// waitForVanillaWGServiceGone opens its own SCM handle and waits for
// the per-tunnel `WireGuardTunnel$<iface>` service to be fully gone
// after `wireguard.exe /uninstalltunnelservice` has returned. Used by
// the privileged helper after vanilla-WireGuard disconnect.
func waitForVanillaWGServiceGone(ifaceName string, maxWait time.Duration) bool {
	m, err := mgr.Connect()
	if err != nil {
		log.Printf("waitForVanillaWGServiceGone: SCM connect failed: %v", err)
		return false
	}
	defer m.Disconnect()
	return waitForServiceGoneByName(m, "WireGuardTunnel$"+ifaceName, maxWait)
}
