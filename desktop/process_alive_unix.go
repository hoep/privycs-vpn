//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// isProcessAlive returns true if a process with the given pid is alive,
// regardless of whether the caller has permission to signal it. EPERM
// means the process exists but is owned by another UID (e.g. helper-
// spawned openvpn running as root) — that still counts as alive; only
// ESRCH means the kernel has no such pid.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return !errors.Is(err, syscall.ESRCH)
}
