//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// signalNotify registers for SIGINT and SIGTERM on Unix platforms.
func signalNotify(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
}
