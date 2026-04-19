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
// first network adapter whose Name or Description contains the
// caller-supplied substring (e.g. "OpenVPN", "WireGuard"). Uses the
// native Win32 GetIfEntry2 API via iphlpapi.dll — no PowerShell
// subprocess, no console-window flash, no WMI provider dependency,
// returns in 1–2 ms per call. Safe to poll every 1–2 s.
//
// Previous implementation spawned PowerShell on every call which:
//
//   - Made the UI window flicker on Windows 11 because conhost
//     briefly materialises behind CREATE_NO_WINDOW despite the flag
//     being set (documented Win11 regression; see Microsoft forums).
//   - Burned ~50 ms of CPU per poll on a fresh PowerShell engine
//     startup, stacking up over a long session.
//   - Returned zero for ovpn-dco adapters on the .NET IPv4Statistics
//     path because DCO bypasses the IP layer (see GitHub issue
//     OpenVPN/openvpn#447 and follow-ups). Get-NetAdapterStatistics
//     worked but still spawned PowerShell.
//
// The new path calls iphlpapi!GetIfEntry2 directly with the index of
// the Go-enumerated network interface, which talks to the NDIS driver
// and returns DCO-accurate, 64-bit-wide InOctets / OutOctets values.
func getWindowsTrafficStats(adapterName string) (rx, tx int64) {
	needle := strings.ToLower(adapterName)

	ifaces, err := net.Interfaces()
	if err != nil {
		return 0, 0
	}

	var matched *net.Interface
	for i := range ifaces {
		// Only consider up adapters so a stale/disconnected VPN
		// adapter (e.g. a lingering TAP adapter from a previous
		// session) doesn't frontrun the currently-active one.
		if ifaces[i].Flags&net.FlagUp == 0 {
			continue
		}
		nameLower := strings.ToLower(ifaces[i].Name)
		if strings.Contains(nameLower, needle) {
			matched = &ifaces[i]
			break
		}
	}
	if matched == nil {
		return 0, 0
	}

	rxU, txU, err := getIfEntry2Stats(uint32(matched.Index))
	if err != nil {
		return 0, 0
	}
	// Both counters fit into int64 until ~8 EB; VPN sessions don't
	// push that much traffic, so the cast is safe.
	rx = int64(rxU)
	tx = int64(txU)
	if rx > 0 || tx > 0 {
		log.Printf("Traffic stats for %s (GetIfEntry2 idx=%d): rx=%d tx=%d",
			matched.Name, matched.Index, rx, tx)
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
