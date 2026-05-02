//go:build windows

package main

import "os/exec"

// applyDetachedSession is a no-op on Windows. Process-group-detach
// semantics (setsid) are Unix-specific; on Windows the WireGuard
// tunnel service handles its own process lifecycle via the SCM.
func applyDetachedSession(cmd *exec.Cmd) {}
