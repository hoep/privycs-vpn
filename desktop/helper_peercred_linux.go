//go:build linux

package main

import (
	"fmt"
	"net"
	"syscall"
)

// getPeerUID returns the UID of the process at the other end of the
// AF_UNIX connection on Linux via SO_PEERCRED.
//
// v1.0.5.30: closes the local-RCE-as-root attack flagged in the
// 2026-05-21 production audit. Without this check, any unprivileged
// local user could connect to the 0666 socket and invoke any
// whitelisted action (including ones that write files / spawn
// privileged processes as root).
//
// Returns syscall errors if the connection is not an AF_UNIX socket
// or if the kernel cannot deliver the peer credentials (rare; usually
// indicates a kernel module restriction or a non-unix transport).
func getPeerUID(conn net.Conn) (uint32, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("getPeerUID: connection is not AF_UNIX (%T)", conn)
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("getPeerUID: SyscallConn: %w", err)
	}
	var ucred *syscall.Ucred
	var sockErr error
	ctlErr := raw.Control(func(fd uintptr) {
		ucred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if ctlErr != nil {
		return 0, fmt.Errorf("getPeerUID: rawconn.Control: %w", ctlErr)
	}
	if sockErr != nil {
		return 0, fmt.Errorf("getPeerUID: GetsockoptUcred(SO_PEERCRED): %w", sockErr)
	}
	if ucred == nil {
		return 0, fmt.Errorf("getPeerUID: ucred is nil")
	}
	return ucred.Uid, nil
}

// peerCredSupported reports whether this platform can return the peer
// UID for a connected AF_UNIX socket. Linux: always true.
func peerCredSupported() bool {
	return true
}
