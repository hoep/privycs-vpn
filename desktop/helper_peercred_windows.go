//go:build windows

package main

import (
	"net"
)

// getPeerUID is a Windows stub. AF_UNIX sockets on Windows (10 1803+)
// do not expose peer credentials — there is no SO_PEERCRED / xucred
// equivalent on this transport. Defence is the icacls ACL applied to
// the socket file (Authenticated Users only — see
// privileged_helper.go around the listener-setup block).
//
// Returns (0, nil) so the cross-platform UID-check codepath always
// passes on Windows; the Authenticated-Users gate is the real
// enforcement. Scheduled v1.0.6: switch Windows transport to named
// pipes which DO expose GetNamedPipeClientProcessId →
// OpenProcessToken → GetTokenInformation(TokenUser) for proper SID
// gating equivalent to peer-cred.
func getPeerUID(_ net.Conn) (uint32, error) {
	return 0, nil
}

// peerCredSupported reports whether this platform can return the peer
// UID for a connected AF_UNIX socket. Windows AF_UNIX: false. Callers
// can use this to skip the allowed-UID enforcement entirely on
// Windows without faking a value.
func peerCredSupported() bool {
	return false
}
