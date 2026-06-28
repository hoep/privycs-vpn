//go:build windows

package main

import (
	"context"
	"os/exec"
	"syscall"
)

// hideWindow sets the CREATE_NO_WINDOW flag on a command so no console
// window flashes on screen when running external processes on Windows.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

// execHidden creates a command with hidden console window.
func execHidden(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	hideWindow(cmd)
	return cmd
}

// execHiddenContext creates a context-aware command with hidden console window.
func execHiddenContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideWindow(cmd)
	return cmd
}

// NOTE: a previous runElevated() helper that shelled out via
// `cmd /C start ... runas <exe> <args>` was removed — it had no callers
// (elevation now goes through the privileged helper / SCM service, not an
// ad-hoc UAC shell-out) and `cmd /C` is a real shell, so it was a standing
// command-injection footgun (flagged by Aikido). Don't reintroduce a
// cmd.exe-based elevation path; route privileged actions through the helper.
