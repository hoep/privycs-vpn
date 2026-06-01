//go:build !darwin

package main

import (
	"context"
	"fmt"
)

// Non-darwin stubs for the NEVPNManager bridge. NEVPNManager is an Apple
// framework; Linux uses swanctl/charon and Windows uses rasdial, so these
// are never reached on those platforms (the IPSecProtocol dispatch only
// calls them under runtime.GOOS == "darwin").

func macosConfigureNEVPN(_ *IPSecProtocol, _ *sswanProfile) error {
	return fmt.Errorf("NEVPNManager is macOS-only")
}

func macosUpNEVPN(_ *IPSecProtocol, _ context.Context) error {
	return fmt.Errorf("NEVPNManager is macOS-only")
}

func macosDownNEVPN(_ *IPSecProtocol, _ context.Context) error {
	return nil
}

func macosStatusNEVPN(_ *IPSecProtocol) (connected bool, localAddr string) {
	return false, ""
}
