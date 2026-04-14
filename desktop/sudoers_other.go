//go:build !linux

package main

// ensureSudoers is a no-op on non-Linux platforms.
// macOS uses authorization services, Windows uses UAC elevation.
func ensureSudoers() {}
