//go:build !windows

package main

import (
	"fmt"
	"os"
)

// dumpAwgLog is a no-op stub on non-Windows builds. The amneziawg-
// windows ringlogger is Windows-only (memory-mapped binary log
// format used by the official AmneziaWG Windows client).
func dumpAwgLog() {
	fmt.Fprintln(os.Stderr, "--dump-awg-log is only available on Windows")
	os.Exit(1)
}
