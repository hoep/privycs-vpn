//go:build windows

package main

import (
	"os"
	"os/signal"
)

// signalNotify registers for interrupt signals on Windows.
func signalNotify(ch chan<- os.Signal) {
	signal.Notify(ch, os.Interrupt)
}
