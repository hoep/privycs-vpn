//go:build !windows

package main

import (
	"context"
	"os/exec"
)

// hideWindow is a no-op on non-Windows platforms.
func hideWindow(cmd *exec.Cmd) {}

// execHidden creates a command (no special handling needed on Unix).
func execHidden(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// execHiddenContext creates a context-aware command (no special handling on Unix).
func execHiddenContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// runElevated runs a command with sudo on Unix platforms.
func runElevated(executable string, args string) error {
	cmd := exec.Command("sudo", executable, args)
	return cmd.Run()
}
