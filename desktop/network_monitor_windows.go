//go:build windows

package main

import (
	"log"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	iphlpapi             = syscall.NewLazyDLL("iphlpapi.dll")
	procNotifyAddrChange = iphlpapi.NewProc("NotifyAddrChange")
)

// startPlatformWatcher uses the Win32 NotifyAddrChange API to receive
// immediate notification when any network adapter address changes.
//
// CRITICAL: pass NULL for the LPOVERLAPPED parameter. With a non-NULL
// overlapped struct AND no event handle wired into overlapped.HEvent,
// NotifyAddrChange returns ERROR_IO_PENDING (997) immediately; the
// previous implementation treated 997 as just a logged "error" and
// fell straight back into the for-loop, hot-spinning at 100% CPU and
// allocating a fresh handle + overlapped struct each iteration. Real-
// world impact: 9 GB log file in minutes, handle exhaustion in
// npfs.sys, system-wide instability and connect-time crashes once
// the kernel handle table thinned out.
//
// The synchronous form (overlapped == NULL) blocks the goroutine
// inside the system call until an actual address-change event
// occurs; the kernel wakes us, we fire the callback, and we loop
// back into the next blocking call. Zero CPU between events.
func startPlatformWatcher(callback func()) (stopFn func(), err error) {
	stopCh := make(chan struct{})
	var once sync.Once

	go func() {
		for {
			var handle syscall.Handle
			ret, _, _ := procNotifyAddrChange.Call(
				uintptr(unsafe.Pointer(&handle)),
				0, // NULL overlapped -> synchronous, blocks until change
			)

			select {
			case <-stopCh:
				return
			default:
			}

			if ret == 0 {
				log.Println("Network monitor: address change detected")
				callback()
			} else {
				// Synchronous mode should only return NO_ERROR (0) on
				// success or a real error code. If a real error happens
				// (e.g. kernel API change on a future Windows build),
				// throttle to avoid the log-spam pattern that this
				// fix was created to eliminate.
				log.Printf("Network monitor: unexpected return %d - throttling for 5s", ret)
				select {
				case <-stopCh:
					return
				case <-time.After(5 * time.Second):
				}
			}
		}
	}()

	log.Println("Network monitor: listening for Windows address change notifications")
	return func() { once.Do(func() { close(stopCh) }) }, nil
}

// getCurrentSSIDPlatform returns the current WiFi SSID on Windows
// by parsing the output of netsh wlan show interfaces.
func getCurrentSSIDPlatform() string {
	// execHidden wraps exec.Command with CREATE_NO_WINDOW so the
	// netsh console window doesn't flash every time the On-Demand
	// poll loop ticks (roughly every 2s) - otherwise the user sees
	// a cmd window pop up and immediately close repeatedly as long
	// as On-Demand is on.
	out, err := execHidden("netsh", "wlan", "show", "interfaces").Output()
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
//
// CRITICAL: do NOT parse ipconfig output for "Default Gateway" string.
// That string is localized - on a German Windows install ipconfig
// emits "Standardgateway" instead, which would always miss and the
// function would always return "none" for Ethernet-connected German
// users (the user reported exactly this symptom: UI shows "No
// network" while ping 8.8.8.8 succeeds because they were on a wired
// connection on a German Windows). Use net.Interfaces() instead -
// the kernel-level interface enumeration is locale-independent.
func getNetworkTypePlatform() string {
	ssid := getCurrentSSIDPlatform()
	if ssid != "" {
		return "wifi"
	}
	if hasActiveNonLoopbackIPv4() {
		return "ethernet"
	}
	return "none"
}

// hasActiveNonLoopbackIPv4 reports whether any UP non-loopback
// interface has at least one global IPv4 address. Used to detect
// "I have ethernet/wired connectivity" without parsing localized
// command output.
func hasActiveNonLoopbackIPv4() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil {
				continue
			}
			// Skip APIPA / link-local (169.254.x.x) - those mean DHCP
			// failed, not "we have connectivity".
			if ip.IsLinkLocalUnicast() {
				continue
			}
			return true
		}
	}
	return false
}
