//go:build windows

package main

import (
	"log"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

var (
	iphlpapi             = syscall.NewLazyDLL("iphlpapi.dll")
	procNotifyAddrChange = iphlpapi.NewProc("NotifyAddrChange")
)

// startPlatformWatcher uses the Win32 NotifyAddrChange API to receive
// immediate notification when any network adapter address changes.
func startPlatformWatcher(callback func()) (stopFn func(), err error) {
	stopCh := make(chan struct{})
	var once sync.Once

	go func() {
		for {
			var handle syscall.Handle
			var overlapped syscall.Overlapped
			ret, _, callErr := procNotifyAddrChange.Call(
				uintptr(unsafe.Pointer(&handle)),
				uintptr(unsafe.Pointer(&overlapped)),
			)

			select {
			case <-stopCh:
				return
			default:
			}

			if ret == 0 {
				log.Println("Network monitor: NotifyAddrChange triggered")
				callback()
			} else {
				log.Printf("Network monitor: NotifyAddrChange returned %d (%v)", ret, callErr)
			}
		}
	}()

	log.Println("Network monitor: listening for Windows address change notifications")
	return func() { once.Do(func() { close(stopCh) }) }, nil
}

// getCurrentSSIDPlatform returns the current WiFi SSID on Windows
// by parsing the output of netsh wlan show interfaces.
func getCurrentSSIDPlatform() string {
	out, err := exec.Command("netsh", "wlan", "show", "interfaces").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		// Match "SSID" but not "BSSID"
		if strings.HasPrefix(line, "SSID") && !strings.HasPrefix(line, "BSSID") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

// getNetworkTypePlatform returns "wifi", "ethernet", or "none" on Windows.
func getNetworkTypePlatform() string {
	ssid := getCurrentSSIDPlatform()
	if ssid != "" {
		return "wifi"
	}

	out, err := exec.Command("ipconfig").Output()
	if err != nil {
		return "none"
	}
	if strings.Contains(string(out), "Default Gateway") {
		return "ethernet"
	}
	return "none"
}
