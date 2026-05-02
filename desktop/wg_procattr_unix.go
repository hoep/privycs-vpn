//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// applyDetachedSession configures cmd so its child process becomes a
// new session leader (setsid). Necessary on macOS for wg-quick: the
// script forks wireguard-go as a background daemon, and on Mac that
// daemon needs to detach from the parent's controlling terminal /
// process group to survive after wg-quick exits. When the helper is
// spawned by launchd as a system daemon, the inherited session is
// what launchd hands out, and wireguard-go's standard daemonize-via-
// double-fork doesn't fully detach — the child exits prematurely or
// the utun device allocation fails silently. Calling setsid() on the
// wg-quick subprocess gives it (and its descendants) a fresh session
// that survives independent of the helper.
//
// Linux is unaffected by this in practice (kernel WG, no userspace
// daemon to detach), but Setsid is harmless on Linux so we apply it
// uniformly across Unix platforms rather than darwin-only.
func applyDetachedSession(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
