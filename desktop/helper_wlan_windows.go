//go:build windows

package main

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"
)

// cmdWlanSSID returns the currently-connected WLAN SSID via the
// privileged helper. Tries multiple paths in order:
//
//   1. WlanQueryInterface(current_connection) - works on consumer
//      Windows where Location permission is granted.
//   2. Read WLAN profile XML files from disk.
//      C:\ProgramData\Microsoft\Wlansvc\Profiles\Interfaces\
//        {adapter-guid}\{profile-guid}.xml. Each file contains
//      <SSIDConfig><SSID><name>...</name></SSID></SSIDConfig>. The
//      file mtime is updated on (re-)connect, so the most recently
//      modified XML in the connected adapter's folder is the
//      currently-active SSID. ACL on this folder is typically
//      SYSTEM-only (helper can read, user-mode app cannot), but
//      the data is plain disk metadata and is NOT subject to the
//      Windows Location permission gate that blocks the WLAN APIs.
//
// Path 2 is the workaround for the locked-down enterprise Win11
// case where ALL of WlanQueryInterface, WlanGetProfileList, and
// the registry hive HKLM\SOFTWARE\Microsoft\Windows NT\
// CurrentVersion\NetworkList\Profiles return ERROR_ACCESS_DENIED.
func (h *PrivilegedHelper) cmdWlanSSID(cmd HelperCommand) HelperResponse {
	log.Println("Helper: wlan_ssid query (SYSTEM context)")

	// Path 1: same syscall the user-mode app uses, just from
	// SYSTEM context. May still fail under computer-wide GPO.
	if ssid, ok := readWLANSSIDViaSyscall(); ok && ssid != "" {
		log.Printf("Helper: wlan_ssid via WlanQueryInterface = %q", ssid)
		return HelperResponse{Success: true, Output: ssid}
	}

	// Path 2: WLAN profile XML files. Most aggressively blocked
	// systems still leave these readable for SYSTEM.
	if ssid := readSSIDFromProfileXML(); ssid != "" {
		log.Printf("Helper: wlan_ssid via profile XML = %q", ssid)
		return HelperResponse{Success: true, Output: ssid}
	}

	// Empty + success means "no detection succeeded" - caller will
	// fall through to its own fallback paths (registry, netsh).
	log.Println("Helper: wlan_ssid all paths returned empty")
	return HelperResponse{Success: true, Output: ""}
}

// readSSIDFromProfileXML walks
// C:\ProgramData\Microsoft\Wlansvc\Profiles\Interfaces\{adapter-guid}\
// for the currently-connected wireless adapter (identified via
// WlanEnumInterfaces, which is NOT location-restricted), reads each
// XML profile, parses out the SSID, and returns the SSID of the
// most recently modified XML.
//
// File mtime represents the last (re-)connect time the OS wrote to
// that profile, so when the adapter is in state=connected the most
// recent XML corresponds to the active SSID.
//
// Returns "" on any failure - caller treats that as "could not
// determine".
func readSSIDFromProfileXML() string {
	// Need PROGRAMDATA. Falls back to default if env var missing.
	programData := os.Getenv("PROGRAMDATA")
	if programData == "" {
		programData = `C:\ProgramData`
	}
	baseDir := filepath.Join(programData, "Microsoft", "Wlansvc", "Profiles", "Interfaces")

	// Identify the connected adapter's GUID. We avoid reading XMLs
	// from disconnected adapters - those would give us stale most-
	// recent-profile data.
	adapterGUID, ok := getConnectedAdapterGUID()
	if !ok {
		log.Println("Helper: profile XML - no connected WLAN adapter")
		return ""
	}

	adapterDir := filepath.Join(baseDir, adapterGUID)
	entries, err := os.ReadDir(adapterDir)
	if err != nil {
		log.Printf("Helper: profile XML - cannot read %s: %v", adapterDir, err)
		return ""
	}

	type profile struct {
		ssid  string
		mtime int64
	}
	var found []profile

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".xml") {
			continue
		}
		full := filepath.Join(adapterDir, name)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		ssid := parseSSIDFromXML(data)
		if ssid == "" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		found = append(found, profile{ssid: ssid, mtime: info.ModTime().UnixNano()})
	}

	if len(found) == 0 {
		log.Printf("Helper: profile XML - no parseable profiles in %s", adapterDir)
		return ""
	}

	// Newest first.
	sort.Slice(found, func(i, j int) bool {
		return found[i].mtime > found[j].mtime
	})
	log.Printf("Helper: profile XML - %d profile(s), newest=%q", len(found), found[0].ssid)
	return found[0].ssid
}

// parseSSIDFromXML extracts the SSID name from a Windows WLAN
// profile XML. The schema is well-defined; we only care about the
// <SSIDConfig><SSID><name> element.
func parseSSIDFromXML(data []byte) string {
	type ssidT struct {
		Name string `xml:"name"`
	}
	type ssidConfigT struct {
		SSID ssidT `xml:"SSID"`
	}
	type wlanProfile struct {
		XMLName    xml.Name    `xml:"WLANProfile"`
		SSIDConfig ssidConfigT `xml:"SSIDConfig"`
	}
	var p wlanProfile
	if err := xml.Unmarshal(data, &p); err != nil {
		return ""
	}
	return p.SSIDConfig.SSID.Name
}

// getConnectedAdapterGUID returns the GUID string of the first WLAN
// adapter in state=connected (1). Format: {xxxxxxxx-xxxx-xxxx-xxxx-
// xxxxxxxxxxxx} (canonical Windows GUID with braces, matches the
// folder name used by the WLAN service for profile storage).
//
// WlanEnumInterfaces is NOT subject to the Location-permission GPO -
// it only returns adapter identity + state, not SSID-data - so it
// works even on the locked-down systems where WlanQueryInterface
// returns ERROR_ACCESS_DENIED.
func getConnectedAdapterGUID() (string, bool) {
	var clientHandle uintptr
	var negotiatedVersion uint32
	const wlanAPIVersion = 0x00000002

	ret, _, _ := procWlanOpenHandle.Call(
		uintptr(wlanAPIVersion), 0,
		uintptr(unsafe.Pointer(&negotiatedVersion)),
		uintptr(unsafe.Pointer(&clientHandle)),
	)
	if ret != 0 {
		return "", false
	}
	defer procWlanCloseHandle.Call(clientHandle, 0)

	// v1.0.5.30: see readWLANSSIDViaSyscall in network_monitor_windows.go
	// for the typed-pointer + unsafe.Add refactor rationale (avoids the
	// unsafeptr vet warnings).
	var ifList *wlanInterfaceInfoList
	ret, _, _ = procWlanEnumInterfaces.Call(
		clientHandle, 0,
		uintptr(unsafe.Pointer(&ifList)),
	)
	if ret != 0 || ifList == nil {
		return "", false
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(ifList)))

	const headerSize = 8
	entrySize := unsafe.Sizeof(wlanInterfaceInfo{})
	ifListBase := unsafe.Pointer(ifList)
	for i := uint32(0); i < ifList.NumberOfItems; i++ {
		entry := (*wlanInterfaceInfo)(unsafe.Add(ifListBase, uintptr(headerSize)+uintptr(i)*entrySize))
		if entry.IsState != 1 {
			continue
		}
		return guidToBracedString(entry.InterfaceGUID), true
	}
	return "", false
}

// guidToBracedString formats a Windows GUID byte array into the
// canonical `{xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx}` text form. The
// first three components are little-endian, the last two are read
// as-is per the standard Windows GUID encoding.
func guidToBracedString(g [16]byte) string {
	d1 := binary.LittleEndian.Uint32(g[0:4])
	d2 := binary.LittleEndian.Uint16(g[4:6])
	d3 := binary.LittleEndian.Uint16(g[6:8])
	return fmt.Sprintf("{%08X-%04X-%04X-%02X%02X-%02X%02X%02X%02X%02X%02X}",
		d1, d2, d3,
		g[8], g[9],
		g[10], g[11], g[12], g[13], g[14], g[15],
	)
}
