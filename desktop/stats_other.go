//go:build !windows

package main

import (
	"fmt"
	"os"
	"strings"
)

// getWindowsTrafficStats is a no-op on non-Windows platforms.
func getWindowsTrafficStats(adapterName string) (rx, tx int64) {
	return 0, 0
}

// getLinuxInterfaceStats reads RX/TX bytes from /sys/class/net for a given interface.
// Works for any network interface (tun0, wg0, etc.) without requiring root.
func getLinuxInterfaceStats(ifaceName string) (rx, tx int64) {
	rxPath := fmt.Sprintf("/sys/class/net/%s/statistics/rx_bytes", ifaceName)
	txPath := fmt.Sprintf("/sys/class/net/%s/statistics/tx_bytes", ifaceName)

	if data, err := os.ReadFile(rxPath); err == nil {
		fmt.Sscan(strings.TrimSpace(string(data)), &rx)
	}
	if data, err := os.ReadFile(txPath); err == nil {
		fmt.Sscan(strings.TrimSpace(string(data)), &tx)
	}
	return rx, tx
}
