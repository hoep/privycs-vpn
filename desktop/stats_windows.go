//go:build windows

package main

import (
	"log"
	"net"
	"strings"
	"syscall"
	"unsafe"
)

// getWindowsTrafficStats reads per-interface byte counters for the
// first UP network adapter whose Name contains any of the supplied
// needle substrings (case-insensitive). Variadic so callers can try
// several known patterns - e.g. OpenVPN can be "OpenVPN", "ovpn-dco",
// "TAP-Windows", "Wintun" depending on which transport driver is
// installed; IPSec via Windows rasdial uses the user's
// ConnectionName as the adapter alias. Uses Win32 GetIfEntry2 via
// iphlpapi.dll - no PowerShell subprocess, returns in 1-2 ms.
//
// On no match, logs the list of UP adapters so a user reporting
// "traffic stats stay zero" can see what adapter names are actually
// present and the caller can be updated with the right needle.
func getWindowsTrafficStats(needles ...string) (rx, tx int64) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return 0, 0
	}

	// Build the lowercase needle list once.
	nLower := make([]string, 0, len(needles))
	for _, n := range needles {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		nLower = append(nLower, strings.ToLower(n))
	}
	if len(nLower) == 0 {
		return 0, 0
	}

	var matched *net.Interface
	var matchedOn string
	upNames := make([]string, 0, 8)
	for i := range ifaces {
		// Only consider up adapters so a stale/disconnected VPN
		// adapter does not frontrun the currently-active one.
		if ifaces[i].Flags&net.FlagUp == 0 {
			continue
		}
		nameLower := strings.ToLower(ifaces[i].Name)
		upNames = append(upNames, ifaces[i].Name)
		for _, needle := range nLower {
			if strings.Contains(nameLower, needle) {
				matched = &ifaces[i]
				matchedOn = needle
				break
			}
		}
		if matched != nil {
			break
		}
	}
	if matched == nil {
		log.Printf("Traffic stats: no UP adapter matches any of %v (UP adapters: %v)",
			needles, upNames)
		return 0, 0
	}

	rxU, txU, err := getIfEntry2Stats(uint32(matched.Index))
	if err != nil {
		log.Printf("Traffic stats: GetIfEntry2 idx=%d failed: %v", matched.Index, err)
		return 0, 0
	}
	rx = int64(rxU)
	tx = int64(txU)
	if rx > 0 || tx > 0 {
		log.Printf("Traffic stats for %s (matched=%q idx=%d): rx=%d tx=%d",
			matched.Name, matchedOn, matched.Index, rx, tx)
	}
	return rx, tx
}

// mibIfRow2 mirrors MIB_IF_ROW2 from netioapi.h. Field sizes and
// order MUST match Windows' definition exactly or GetIfEntry2 writes
// past the end of the struct.
//
// Reference:
// https://learn.microsoft.com/en-us/windows/win32/api/netioapi/ns-netioapi-mib_if_row2
//
// The struct is 1352 bytes on 64-bit Windows. Only InterfaceIndex
// (input) and InOctets / OutOctets (output) are read by our code; the
// remaining fields are present solely for correct layout.
type mibIfRow2 struct {
	InterfaceLuid                uint64
	InterfaceIndex               uint32
	InterfaceGuid                [16]byte
	Alias                        [257]uint16 // WCHAR * (IF_MAX_STRING_SIZE + 1)
	Description                  [257]uint16
	PhysicalAddressLength        uint32
	PhysicalAddress              [32]byte
	PermanentPhysicalAddress     [32]byte
	Mtu                          uint32
	Type                         uint32
	TunnelType                   uint32
	MediaType                    uint32
	PhysicalMediumType           uint32
	AccessType                   uint32
	DirectionType                uint32
	InterfaceAndOperStatusFlags  uint8
	_                            [3]byte // alignment padding for ULONG fields
	OperStatus                   uint32
	AdminStatus                  uint32
	MediaConnectState            uint32
	NetworkGuid                  [16]byte
	ConnectionType               uint32
	_                            uint32 // alignment padding before ULONG64
	TransmitLinkSpeed            uint64
	ReceiveLinkSpeed             uint64
	InOctets                     uint64
	InUcastPkts                  uint64
	InNUcastPkts                 uint64
	InDiscards                   uint64
	InErrors                     uint64
	InUnknownProtos              uint64
	InUcastOctets                uint64
	InMulticastOctets            uint64
	InBroadcastOctets            uint64
	OutOctets                    uint64
	OutUcastPkts                 uint64
	OutNUcastPkts                uint64
	OutDiscards                  uint64
	OutErrors                    uint64
	OutUcastOctets               uint64
	OutMulticastOctets           uint64
	OutBroadcastOctets           uint64
	OutQLen                      uint64
}

// iphlpapi is declared once in network_monitor_windows.go; reuse.
var procGetIfEntry2 = iphlpapi.NewProc("GetIfEntry2")

// getIfEntry2Stats calls iphlpapi!GetIfEntry2 with a zero-valued row
// whose InterfaceIndex field is pre-populated. On success Windows
// fills in the entire row — we return the InOctets and OutOctets
// fields and let the rest fall out of scope.
func getIfEntry2Stats(ifIndex uint32) (rx uint64, tx uint64, err error) {
	var row mibIfRow2
	row.InterfaceIndex = ifIndex

	ret, _, _ := procGetIfEntry2.Call(uintptr(unsafe.Pointer(&row)))
	if ret != 0 { // NO_ERROR = 0; anything else means failure
		return 0, 0, syscall.Errno(ret)
	}
	return row.InOctets, row.OutOctets, nil
}

// getLinuxInterfaceStats is a no-op on Windows.
func getLinuxInterfaceStats(ifaceName string) (rx, tx int64) {
	return 0, 0
}
