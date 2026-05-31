//go:build darwin

package main

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// getPeerUID returns the UID of the process at the other end of the
// AF_UNIX connection on macOS via LOCAL_PEERCRED (getsockopt level
// SOL_LOCAL=0 with option name LOCAL_PEERCRED).
//
// v1.0.5.30: closes the local-RCE-as-root attack flagged in the
// 2026-05-21 production audit. Same root cause + fix shape as the
// Linux SO_PEERCRED path but uses the macOS-specific socket option.
//
// Returns an error if the connection is not AF_UNIX or the kernel
// refuses to deliver the credentials (rare; mostly indicates a
// non-unix transport sneaked through).
func getPeerUID(conn net.Conn) (uint32, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("getPeerUID: connection is not AF_UNIX (%T)", conn)
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("getPeerUID: SyscallConn: %w", err)
	}
	var xucred *unix.Xucred
	var sockErr error
	ctlErr := raw.Control(func(fd uintptr) {
		xucred, sockErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	})
	if ctlErr != nil {
		return 0, fmt.Errorf("getPeerUID: rawconn.Control: %w", ctlErr)
	}
	if sockErr != nil {
		return 0, fmt.Errorf("getPeerUID: GetsockoptXucred(LOCAL_PEERCRED): %w", sockErr)
	}
	if xucred == nil {
		return 0, fmt.Errorf("getPeerUID: xucred is nil")
	}
	return xucred.Uid, nil
}

// peerCredSupported reports whether this platform can return the peer
// UID for a connected AF_UNIX socket. macOS: always true.
func peerCredSupported() bool {
	return true
}
