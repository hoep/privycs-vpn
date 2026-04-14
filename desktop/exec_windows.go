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

// runElevated runs a command with UAC elevation (runas) on Windows.
// This triggers a UAC prompt for the user to approve admin access.
func runElevated(executable string, args string) error {
	verb := "runas"
	cmd := exec.Command("cmd", "/C", "start", "", "/wait", "/b", verb, executable, args)
	hideWindow(cmd)
	return cmd.Run()
}
