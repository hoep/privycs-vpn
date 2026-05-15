//go:build windows

package main

import (
	"fmt"
	"os"

	awgring "github.com/amnezia-vpn/amneziawg-windows/ringlogger"
)

// dumpAwgLog reads the amneziawg-windows ringlogger memory-mapped
// log file (C:\Program Files\AmneziaWG\Data\log.bin) and writes
// its contents to stdout. Exposed via `--dump-awg-log` CLI flag
// so the user can inspect what awgtunnel.Run did during the
// AWG-tunnel-service lifetime — neither our log nor any system
// log captures that output because their tunnel/service.go
// replaces log.SetOutput with the ringlogger after the first
// InitGlobalLogger call.
//
// Must be run as admin/SYSTEM because the Data directory ACL
// (set by amneziawg-windows conf.RootDirectory) restricts access
// to SYSTEM + Builtin Administrators only.
func dumpAwgLog() {
	if err := awgring.DumpTo(os.Stdout, false); err != nil {
		fmt.Fprintf(os.Stderr, "dump-awg-log failed: %v\n", err)
		os.Exit(1)
	}
}
