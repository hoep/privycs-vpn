//go:build !darwin

package main

import "fmt"

// Non-darwin stubs for the in-process WireGuard tunnel. Linux uses the
// existing wg-quick path in privileged_helper.go; Windows uses the
// WireGuard tunnel service. Only macOS embeds wireguard-go directly,
// because launchd's process model breaks wg-quick's normal mode of
// operation. See wg_macos.go for the real implementation and the
// architectural rationale.

func wgDarwinUp(friendlyName, configContent string) (string, error) {
	return "", fmt.Errorf("wgDarwinUp called on non-darwin platform")
}

func wgDarwinDown(friendlyName string) error {
	return fmt.Errorf("wgDarwinDown called on non-darwin platform")
}

func wgDarwinStatus(friendlyName string) (string, bool, error) {
	return "", false, fmt.Errorf("wgDarwinStatus called on non-darwin platform")
}
