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

	"golang.org/x/sys/windows/registry"
)

// registryOpenKey opens a HKLM subkey for reading. Wraps the
// golang.org/x/sys/windows/registry API to keep the rest of the
// file syscall-style.
func registryOpenKey(path string) (registry.Key, error) {
	return registry.OpenKey(registry.LOCAL_MACHINE, path, registry.READ)
}

var (
	iphlpapi             = syscall.NewLazyDLL("iphlpapi.dll")
	procNotifyAddrChange = iphlpapi.NewProc("NotifyAddrChange")

	wlanapi                      = syscall.NewLazyDLL("wlanapi.dll")
	procWlanOpenHandle           = wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle          = wlanapi.NewProc("WlanCloseHandle")
	procWlanRegisterNotification = wlanapi.NewProc("WlanRegisterNotification")
	procWlanEnumInterfaces       = wlanapi.NewProc("WlanEnumInterfaces")
	procWlanQueryInterface       = wlanapi.NewProc("WlanQueryInterface")
	procWlanFreeMemory           = wlanapi.NewProc("WlanFreeMemory")
)

// WLAN_INTF_OPCODE values from wlanapi.h. We only need
// wlan_intf_opcode_current_connection (7) which returns a
// WLAN_CONNECTION_ATTRIBUTES struct including the SSID.
const wlanIntfOpcodeCurrentConnection = 7

// Subset of the Windows WLAN structs we read. Field order matches
// the C definition exactly - any layout change here corrupts the
// reads below.

type dot11SSID struct {
	Length uint32
	SSID   [32]byte
}

type dot11AssociationAttributes struct {
	Dot11Bssid           [6]byte
	_pad                 [2]byte
	Dot11SSID            dot11SSID
	Dot11BSSType         uint32
	Dot11PHYType         uint32
	UDot11PhyIndex       uint32
	WlanSignalQuality    uint32
	ULRxRate             uint32
	ULTxRate             uint32
}

type wlanInterfaceInfo struct {
	InterfaceGUID        [16]byte
	StrInterfaceDescr    [256]uint16
	IsState              uint32
}

type wlanInterfaceInfoList struct {
	NumberOfItems uint32
	Index         uint32
	// Followed by NumberOfItems * wlanInterfaceInfo entries
}

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
			// Invalidate the SSID cache so the upcoming checkAndAct
			// reads fresh data. Without this, a real WLAN state
			// transition could be served stale-cached for up to 500ms.
			invalidateSSIDCache()
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

// ssidCache is a tiny TTL cache fronting the multi-path SSID lookup
// in getCurrentSSIDPlatform. Real-world checkAndAct call patterns
// look like 5-7 calls in the same 100ms window during a WLAN
// connect / disconnect burst (one per ACM event from the kernel,
// plus the 2s-follow-up scheduled by the network monitor). Without
// the cache each call walked the full pipeline: helper IPC + 5 file
// reads + XML parse for users with multiple saved profiles - 25
// disk reads per event burst on the user's machine. With a 500ms
// TTL all but the first call inside a burst hit the cache and
// return immediately.
//
// 500ms is short enough that a real network state change (WLAN
// disconnect from one network and reconnect to another usually
// takes >1s) will be visible on the next call, and long enough to
// dedupe the same-event burst pattern. The 2s safety follow-up
// always reads fresh.
type ssidCacheEntry struct {
	ssid string
	ts   time.Time
}

var (
	ssidCacheMu sync.Mutex
	ssidCache   ssidCacheEntry
)

const ssidCacheTTL = 500 * time.Millisecond

// getCurrentSSIDPlatform returns the current WiFi SSID on Windows
// with a 500ms TTL cache around the multi-path lookup. See
// ssidLookupFresh for the actual detection paths.
func getCurrentSSIDPlatform() string {
	ssidCacheMu.Lock()
	if !ssidCache.ts.IsZero() && time.Since(ssidCache.ts) < ssidCacheTTL {
		cached := ssidCache.ssid
		ssidCacheMu.Unlock()
		return cached
	}
	ssidCacheMu.Unlock()

	result := ssidLookupFresh()

	ssidCacheMu.Lock()
	ssidCache = ssidCacheEntry{ssid: result, ts: time.Now()}
	ssidCacheMu.Unlock()
	return result
}

// ssidLookupFresh runs the four-path SSID detection without caching.
// Each path logs its outcome so users on hostile environments
// (Location-permission GPO etc.) can see in the log exactly which
// fallback finally succeeded - or which step returned empty.
//
//   1. Helper IPC (SYSTEM context). On most enterprise GPOs the
//      Location restriction is user-scoped, so a SYSTEM-context
//      WlanQueryInterface from the helper bypasses it. The helper
//      itself further falls through to reading WLAN profile XML
//      files off disk, which works even when the WLAN APIs are
//      computer-wide blocked.
//   2. User-mode WlanQueryInterface. Works on consumer Windows.
//   3. Registry walk of HKLM\...\NetworkList\Profiles. NOT subject
//      to the Location gate because it reads on-disk profile
//      metadata. Only consulted when an adapter is in connected
//      state, otherwise the most-recent profile would be a stale
//      answer.
//   4. netsh wlan show interfaces parser. Last-ditch fallback.
func ssidLookupFresh() string {
	if ssid, ok := readWLANSSIDViaHelper(); ok && ssid != "" {
		return ssid
	}
	if ssid, ok := readWLANSSIDViaSyscall(); ok && ssid != "" {
		return ssid
	}
	if isAnyWLANAdapterConnected() {
		if ssid, ok := readWLANSSIDViaRegistry(); ok && ssid != "" {
			return ssid
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		ssid, hasAdapter := readNetshSSID()
		if ssid != "" {
			return ssid
		}
		if !hasAdapter {
			return ""
		}
		if attempt < 2 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return ""
}

// invalidateSSIDCache forces the next getCurrentSSIDPlatform call to
// re-run the full lookup. Called on WLAN-state-change platform
// events so a user-perceived state transition (associate / drop)
// reflects in the next checkAndAct without waiting up to 500ms for
// the cache to expire.
func invalidateSSIDCache() {
	ssidCacheMu.Lock()
	ssidCache = ssidCacheEntry{}
	ssidCacheMu.Unlock()
}

// isAnyWLANAdapterConnected reports whether the WLAN service has at
// least one interface in state=connected (1). Used to gate the
// registry-based SSID lookup so we do not return a stale most-
// recent profile when the adapter is actually disconnected /
// associating / authenticating.
func isAnyWLANAdapterConnected() bool {
	var clientHandle uintptr
	var negotiatedVersion uint32
	const wlanAPIVersion = 0x00000002

	ret, _, _ := procWlanOpenHandle.Call(
		uintptr(wlanAPIVersion), 0,
		uintptr(unsafe.Pointer(&negotiatedVersion)),
		uintptr(unsafe.Pointer(&clientHandle)),
	)
	if ret != 0 {
		return false
	}
	defer procWlanCloseHandle.Call(clientHandle, 0)

	// v1.0.5.30: see readWLANSSIDViaSyscall for the typed-pointer
	// + unsafe.Add refactor that avoids the unsafeptr vet warnings.
	var ifList *wlanInterfaceInfoList
	ret, _, _ = procWlanEnumInterfaces.Call(
		clientHandle, 0,
		uintptr(unsafe.Pointer(&ifList)),
	)
	if ret != 0 || ifList == nil {
		return false
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(ifList)))

	const headerSize = 8
	entrySize := unsafe.Sizeof(wlanInterfaceInfo{})
	ifListBase := unsafe.Pointer(ifList)
	for i := uint32(0); i < ifList.NumberOfItems; i++ {
		entry := (*wlanInterfaceInfo)(unsafe.Add(ifListBase, uintptr(headerSize)+uintptr(i)*entrySize))
		if entry.IsState == 1 {
			return true
		}
	}
	return false
}

// readWLANSSIDViaHelper asks the privileged helper to return the
// currently-connected WLAN SSID. The helper runs as LocalSystem
// which on most enterprise setups is not subject to the user-session
// Location-permission GPO that produces ERROR_ACCESS_DENIED on
// WlanQueryInterface from the user-mode app.
//
// Diagnostic note: a previous version gated this on
// IsHelperReachable() but that probe was apparently transient-
// failing on the user's system (other helper actions like
// killswitch_disable worked fine yet IsHelperReachable returned
// false here). We now go straight into SendCommand and let any
// real failure surface as a logged error.
func readWLANSSIDViaHelper() (string, bool) {
	log.Printf("WLAN: trying helper wlan_ssid")
	client := NewHelperClient()
	resp, err := client.SendCommand("wlan_ssid", nil)
	if err != nil {
		log.Printf("WLAN: helper wlan_ssid IPC failed: %v", err)
		return "", false
	}
	if !resp.Success {
		log.Printf("WLAN: helper wlan_ssid reported failure: %s", resp.Error)
		return "", false
	}
	ssid := strings.TrimSpace(resp.Output)
	log.Printf("WLAN: helper returned ssid=%q", ssid)
	if ssid == "" {
		return "", false
	}
	return ssid, true
}

// readWLANSSIDViaRegistry walks HKLM\SOFTWARE\Microsoft\Windows NT\
// CurrentVersion\NetworkList\Profiles and returns the ProfileName of
// the most-recently-connected wireless profile (NameType == 47).
//
// Why this works on Location-GPO-blocked systems:
//   - The data is plain stored disk metadata, not a live wireless
//     scan. The Location-permission gate that blocks dot11Ssid /
//     strProfileName via WlanQueryInterface does NOT apply to
//     reading this part of HKLM (it is treated as system
//     configuration, not location data).
//   - DateLastConnected is updated by the OS the moment a profile
//     is associated, so the most-recent profile is the current one
//     when the WLAN adapter is in state=connected.
//
// Caveats:
//   - Returns the saved profile name. For default profiles this
//     equals the SSID; user-renamed profiles will diverge.
//   - If the user has connected to multiple WiFi networks recently
//     and none of them is currently the active one, the result
//     could be a stale profile. The caller should only consult
//     this when WlanEnumInterfaces reports state=connected.
func readWLANSSIDViaRegistry() (string, bool) {
	const profilesPath = `SOFTWARE\Microsoft\Windows NT\CurrentVersion\NetworkList\Profiles`

	root, err := registryOpenKey(profilesPath)
	if err != nil {
		log.Printf("WLAN[registry]: open Profiles failed: %v", err)
		return "", false
	}
	defer root.Close()

	subkeys, err := root.ReadSubKeyNames(-1)
	if err != nil {
		log.Printf("WLAN[registry]: enum subkeys failed: %v", err)
		return "", false
	}
	log.Printf("WLAN[registry]: %d profile(s) on disk", len(subkeys))

	var bestName string
	var bestStamp uint64

	for _, sub := range subkeys {
		k, err := registryOpenKey(profilesPath + `\` + sub)
		if err != nil {
			continue
		}
		nameType, _, _ := k.GetIntegerValue("NameType")
		// 47 = wireless. 6 = wired. 0 = unknown / disconnected.
		// 71 = mobile broadband.
		if nameType != 47 {
			k.Close()
			continue
		}
		name, _, err := k.GetStringValue("ProfileName")
		if err != nil || name == "" {
			k.Close()
			continue
		}
		// DateLastConnected is REG_BINARY, 16 bytes (SYSTEMTIME).
		// The first 8 bytes encoded as little-endian uint64 give
		// us a stamp comparable for "most recent" sorting without
		// having to parse SYSTEMTIME.
		stamp := uint64(0)
		if data, _, err := k.GetBinaryValue("DateLastConnected"); err == nil && len(data) >= 8 {
			for i := 0; i < 8; i++ {
				stamp |= uint64(data[i]) << (8 * i)
			}
		}
		if stamp >= bestStamp {
			bestStamp = stamp
			bestName = name
		}
		k.Close()
	}

	if bestName != "" {
		log.Printf("WLAN[registry]: most-recent wireless profile=%q", bestName)
		return bestName, true
	}
	log.Printf("WLAN[registry]: no wireless profile found")
	return "", false
}

// readWLANSSIDViaSyscall queries the WLAN service directly through
// the Win32 wlanapi.dll. Returns (ssid, true) on success including
// the case where there are no connected WLAN adapters. Returns
// (any, false) only when the API itself failed and we should fall
// back to netsh.
//
// IMPORTANT: as of Windows 10 build 2004 the wlanAssociationAttributes
// .dot11Ssid field requires the Location permission. Enterprise GPOs
// often block Location for non-elevated user sessions ("Standort-
// meldungen vom Administrator gesperrt" dialog). When that happens
// uSSIDLength returns 0 even though the adapter is associated.
//
// Workaround: also read strProfileName at offset 8. The profile
// name is the saved name of the WiFi network (defaults to SSID at
// connect time, locally stored, NOT subject to location permission
// in our testing). For most home / office networks profile name ==
// SSID exactly, which makes the user's "only" rule work without
// additional configuration.
func readWLANSSIDViaSyscall() (ssid string, ok bool) {
	var clientHandle uintptr
	var negotiatedVersion uint32
	const wlanAPIVersion = 0x00000002 // WLAN_API_VERSION_2_0

	ret, _, _ := procWlanOpenHandle.Call(
		uintptr(wlanAPIVersion),
		0,
		uintptr(unsafe.Pointer(&negotiatedVersion)),
		uintptr(unsafe.Pointer(&clientHandle)),
	)
	if ret != 0 {
		log.Printf("WLAN[user]: WlanOpenHandle failed ret=%d", ret)
		return "", false
	}
	defer procWlanCloseHandle.Call(clientHandle, 0)

	// v1.0.5.30: refactored from `var ifListPtr uintptr` +
	// `unsafe.Pointer(uintptr(...))` round-trips to a typed
	// **wlanInterfaceInfoList. Win32 still gets the raw pointer
	// because procWlanEnumInterfaces.Call takes uintptr, but
	// `&ifList` reinterpretation only crosses unsafe-Pointer
	// rules once at the syscall boundary (vet-clean). All
	// subsequent struct + offset access uses typed pointers +
	// unsafe.Add, which vet's unsafeptr analyzer accepts as
	// safe pointer arithmetic.
	var ifList *wlanInterfaceInfoList
	ret, _, _ = procWlanEnumInterfaces.Call(
		clientHandle, 0,
		uintptr(unsafe.Pointer(&ifList)),
	)
	if ret != 0 || ifList == nil {
		log.Printf("WLAN[user]: WlanEnumInterfaces failed ret=%d ifList=%v", ret, ifList)
		return "", false
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(ifList)))

	count := ifList.NumberOfItems
	if count == 0 {
		log.Printf("WLAN[user]: 0 interfaces enumerated")
		return "", true
	}
	log.Printf("WLAN[user]: enumerated %d interface(s)", count)

	const headerSize = 8 // NumberOfItems + Index = 2 * uint32
	entrySize := unsafe.Sizeof(wlanInterfaceInfo{})
	ifListBase := unsafe.Pointer(ifList)

	for i := uint32(0); i < count; i++ {
		entry := (*wlanInterfaceInfo)(unsafe.Add(ifListBase, uintptr(headerSize)+uintptr(i)*entrySize))
		log.Printf("WLAN[user]: interface[%d] state=%d (1=connected)", i, entry.IsState)

		// IsState 1 = wlan_interface_state_connected. Skip
		// disconnected/associating adapters.
		if entry.IsState != 1 {
			continue
		}

		// Same typed-pointer pattern for WlanQueryInterface output —
		// declare as *byte so we can do unsafe.Add arithmetic on
		// the WLAN_CONNECTION_ATTRIBUTES layout without uintptr
		// round-trips. WlanFreeMemory takes the raw pointer back
		// as uintptr, which is the only unsafe.Pointer rule-3
		// conversion left.
		var dataPtr *byte
		var dataSize uint32
		ret, _, _ := procWlanQueryInterface.Call(
			clientHandle,
			uintptr(unsafe.Pointer(&entry.InterfaceGUID[0])),
			uintptr(wlanIntfOpcodeCurrentConnection),
			0,
			uintptr(unsafe.Pointer(&dataSize)),
			uintptr(unsafe.Pointer(&dataPtr)),
			0,
		)
		log.Printf("WLAN[user]: WlanQueryInterface ret=%d size=%d", ret, dataSize)
		if ret != 0 || dataPtr == nil {
			continue
		}

		// WLAN_CONNECTION_ATTRIBUTES layout (verified against
		// wlanapi.h):
		//   isState                 (uint32)  - offset   0
		//   wlanConnectionMode      (uint32)  - offset   4
		//   strProfileName[256]     (wchar_t) - offset   8  (512 bytes)
		//   wlanAssociationAttributes:
		//     dot11Bssid (6 bytes)            - offset 520
		//     padding to align uint32         - offset 526..527
		//     dot11Ssid:
		//       uSSIDLength (uint32)          - offset 528
		//       ucSSID[32]                    - offset 532
		//
		// PRIOR BUG: I had ssidOffset = 530 here. The DOT11_ASSOCIATION
		// _ATTRIBUTES struct starts the DOT11_SSID at +8 from its base
		// (6 bytes BSSID + 2 bytes alignment padding for uint32-sized
		// uSSIDLength), not +10. Reading at the wrong offset gave
		// ssidLen garbage values from inside the BSSID, which the
		// `<= 32` guard always rejected.
		const profileNameOffset = 8
		const profileNameByteLen = 512 // 256 * sizeof(wchar_t)
		const ssidLenOffset = 528
		const ssidBytesOffset = 532
		dataBase := unsafe.Pointer(dataPtr)

		// Try the location-restricted real SSID first.
		ssidLen := *(*uint32)(unsafe.Add(dataBase, ssidLenOffset))
		log.Printf("WLAN[user]: dot11Ssid uSSIDLength=%d", ssidLen)
		if ssidLen > 0 && ssidLen <= 32 {
			ssidBytes := (*[32]byte)(unsafe.Add(dataBase, ssidBytesOffset))
			result := string(ssidBytes[:ssidLen])
			log.Printf("WLAN[user]: returning dot11Ssid=%q", result)
			procWlanFreeMemory.Call(uintptr(dataBase))
			return result, true
		}

		// Fallback: profile name. Not subject to location permission
		// in our testing because the OS reads it from the locally-
		// saved WLAN profile, not from a live scan.
		profileWChar := (*[256]uint16)(unsafe.Add(dataBase, profileNameOffset))
		nameLen := 0
		for nameLen < 256 && profileWChar[nameLen] != 0 {
			nameLen++
		}
		log.Printf("WLAN[user]: strProfileName length=%d", nameLen)
		if nameLen > 0 {
			result := utf16ToString(profileWChar[:nameLen])
			log.Printf("WLAN[user]: returning strProfileName=%q", result)
			procWlanFreeMemory.Call(uintptr(dataBase))
			return result, true
		}
		_ = profileNameByteLen // referenced via offset constants, retained for documentation
		procWlanFreeMemory.Call(uintptr(dataBase))
	}

	return "", true
}

// utf16ToString decodes a UTF-16 LE slice into a Go UTF-8 string.
// Standard library's syscall.UTF16ToString does the same but
// requires a []uint16 slice; we already have one so we just call it.
func utf16ToString(s []uint16) string {
	return syscall.UTF16ToString(s)
}

// readNetshSSID parses one shot of netsh wlan show interfaces.
// Returns (ssid, hasAdapter). hasAdapter is true if the output
// contains anything that looks like a WLAN adapter description (a
// non-error response with at least one SSID-or-BSSID-or-State line),
// false if the WLAN service is unavailable or there are no adapters.
func readNetshSSID() (ssid string, hasAdapter bool) {
	// execHidden wraps exec.Command with CREATE_NO_WINDOW so the
	// netsh console window doesn't flash.
	out, err := execHidden("netsh", "wlan", "show", "interfaces").Output()
	if err != nil {
		return "", false
	}
	text := string(out)
	// Heuristic: any non-empty non-error netsh output that mentions
	// a state field implies a WLAN adapter is present. Locale-
	// independent enough: "State" / "Status" both contain "tat".
	hasAdapter = len(strings.TrimSpace(text)) > 0 &&
		(strings.Contains(text, "SSID") || strings.Contains(text, "BSSID") ||
			strings.Contains(text, "tat"))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		// Match "SSID" but not "BSSID"
		if strings.HasPrefix(line, "SSID") && !strings.HasPrefix(line, "BSSID") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), true
			}
		}
	}
	return "", hasAdapter
}

// getNetworkTypePlatform returns "wifi", "ethernet", or "none" on Windows.
//
// Detection order:
//   1. Try SSID via getCurrentSSIDPlatform. If we get one, it is
//      definitively WiFi.
//   2. SSID empty: check WlanEnumInterfaces for any adapter in
//      state=connected. This is NOT subject to the Location-
//      permission GPO (only the SSID-data-returning calls are), so
//      on enterprise machines where SSID detection is blocked we
//      can still tell that the user IS on WiFi.
//   3. Otherwise check for any non-loopback IPv4 interface →
//      ethernet.
//
// Without step 2 the symptom on locked-down corporate Win11 machines
// is "WiFi-joined while on Ethernet -> status stays ethernet, VPN
// never connects". With step 2 the type is correctly "wifi" and
// trigger-based rules (wifi / wifi_mobile / any) fire even when
// SSID-list-based rules cannot.
func getNetworkTypePlatform() string {
	ssid := getCurrentSSIDPlatform()
	if ssid != "" {
		return "wifi"
	}
	if isAnyWLANAdapterConnected() {
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
