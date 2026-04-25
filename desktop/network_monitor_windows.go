//go:build windows

package main

import (
	"fmt"
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

	wlanapi                     = syscall.NewLazyDLL("wlanapi.dll")
	procWlanOpenHandle          = wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle         = wlanapi.NewProc("WlanCloseHandle")
	procWlanRegisterNotification = wlanapi.NewProc("WlanRegisterNotification")
)

// Windows WLAN_NOTIFICATION_SOURCE bit flags. We only care about
// ACM (Auto Configuration Module) - that source emits the
// connection / disconnection / SSID change events we want.
const wlanNotificationSourceACM = 0x00000008

// WLAN_NOTIFICATION_DATA layout per wlanapi.h. We do NOT need to
// inspect the Code field - any ACM notification is a potential
// trigger to re-evaluate rules. Fields kept for documentation.
type wlanNotificationData struct {
	NotificationSource uint32
	NotificationCode   uint32
	InterfaceGuid      [16]byte
	DataSize           uint32
	Data               unsafe.Pointer
}

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

	// Source 1: NotifyAddrChange (kernel callback on IP / interface
	// up-down). Catches Ethernet plug-in, DHCP renewal with new IP,
	// most WiFi associate / disassociate events.
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

	// Source 2: WlanRegisterNotification (kernel callback on WLAN ACM
	// events). Catches the SSID-roam-without-IP-change case that
	// NotifyAddrChange misses - e.g. switching between two access
	// points in the same enterprise mesh that hand out IPs from the
	// same DHCP pool. ACM source emits connection_complete (10),
	// disconnected (21), network_available (15), scan_complete (8)
	// among others; we treat ANY ACM event as a re-evaluation
	// trigger, so we do not need to filter by code.
	wlanStop, wlanErr := startWlanWatcher(stopCh, callback)
	if wlanErr != nil {
		// Non-fatal: WLAN service may not be running on a server SKU
		// or in a stripped-down Windows install. NotifyAddrChange
		// remains active and handles Ethernet + most WiFi cases.
		log.Printf("Network monitor: WLAN watcher unavailable (%v) - falling back to address-change only", wlanErr)
	}

	log.Println("Network monitor: listening for Windows address-change + WLAN notifications")
	return func() {
		once.Do(func() {
			close(stopCh)
			if wlanStop != nil {
				wlanStop()
			}
		})
	}, nil
}

// startWlanWatcher opens a wlanapi handle, registers a kernel
// callback for ACM-source notifications, and returns a cleanup
// function. The callback runs on a Windows-internal worker thread;
// we bridge into our Go callback via syscall.NewCallback which
// produces a pointer the kernel can invoke directly.
//
// On any error during open / register the function returns and the
// rest of the network monitor keeps working with NotifyAddrChange
// only - WLAN watching is a best-effort enhancement, not a hard
// dependency.
func startWlanWatcher(stopCh <-chan struct{}, callback func()) (cleanup func(), err error) {
	var clientHandle uintptr
	var negotiatedVersion uint32
	// WLAN_API_VERSION_2_0 = 0x00000002 (Vista+; we target Win10+).
	const wlanAPIVersion = 0x00000002

	ret, _, _ := procWlanOpenHandle.Call(
		uintptr(wlanAPIVersion),
		0, // pReserved
		uintptr(unsafe.Pointer(&negotiatedVersion)),
		uintptr(unsafe.Pointer(&clientHandle)),
	)
	if ret != 0 {
		return nil, fmt.Errorf("WlanOpenHandle returned %d", ret)
	}

	// Build the kernel-callable trampoline. The Win32 callback
	// signature is:
	//   VOID WINAPI WlanNotificationCallback(
	//       PWLAN_NOTIFICATION_DATA pNotifData, PVOID pContext);
	// syscall.NewCallback wraps a Go closure; the returned uintptr
	// is suitable to pass to RegisterNotification. The Go callback
	// itself does not need to inspect the notification details - any
	// ACM event is a re-eval trigger.
	cbPtr := syscall.NewCallback(func(notifData uintptr, context uintptr) uintptr {
		// Recover from any panic so a bad callback cannot crash the
		// WLAN service worker thread.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Network monitor: WLAN callback panic: %v", r)
			}
		}()
		// Off-thread the work - the kernel callback should return
		// fast and not run user logic synchronously.
		go func() {
			select {
			case <-stopCh:
				return
			default:
			}
			log.Println("Network monitor: WLAN notification received")
			callback()
		}()
		return 0
	})

	var prevSource uint32
	ret, _, _ = procWlanRegisterNotification.Call(
		clientHandle,
		uintptr(wlanNotificationSourceACM),
		1, // bIgnoreDuplicate = TRUE (suppress identical back-to-back)
		cbPtr,
		0, // pCallbackContext
		0, // pReserved
		uintptr(unsafe.Pointer(&prevSource)),
	)
	if ret != 0 {
		procWlanCloseHandle.Call(clientHandle, 0)
		return nil, fmt.Errorf("WlanRegisterNotification returned %d", ret)
	}

	cleanup = func() {
		// Unregister by setting source mask to 0 + nil callback,
		// then close the handle. Failures here are non-fatal -
		// shutdown either way.
		var prev uint32
		procWlanRegisterNotification.Call(
			clientHandle, 0, 0, 0, 0, 0,
			uintptr(unsafe.Pointer(&prev)),
		)
		procWlanCloseHandle.Call(clientHandle, 0)
	}
	return cleanup, nil
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
