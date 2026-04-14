//go:build linux

package main

import (
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// startPlatformWatcher subscribes to NetworkManager D-Bus signals for
// immediate network change notifications.  If D-Bus/NetworkManager is
// unavailable it falls back to polling nmcli every 30 seconds.
func startPlatformWatcher(callback func()) (stopFn func(), err error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		log.Printf("Network monitor: D-Bus system bus unavailable (%v), falling back to nmcli polling", err)
		return startNmcliPoller(callback), nil
	}

	// Subscribe to NetworkManager state change signals.
	matchRules := []string{
		"type='signal',interface='org.freedesktop.NetworkManager',member='StateChanged'",
		"type='signal',interface='org.freedesktop.DBus.Properties',member='PropertiesChanged',path_namespace='/org/freedesktop/NetworkManager'",
	}

	for _, rule := range matchRules {
		call := conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, rule)
		if call.Err != nil {
			log.Printf("Network monitor: D-Bus AddMatch failed (%v), falling back to nmcli polling", call.Err)
			conn.Close()
			return startNmcliPoller(callback), nil
		}
	}

	signalCh := make(chan *dbus.Signal, 16)
	conn.Signal(signalCh)

	stopCh := make(chan struct{})

	go func() {
		for {
			select {
			case <-stopCh:
				return
			case sig, ok := <-signalCh:
				if !ok {
					return
				}
				log.Printf("Network monitor: D-Bus signal received (%s)", sig.Name)
				callback()
			}
		}
	}()

	stop := func() {
		close(stopCh)
		conn.RemoveSignal(signalCh)
		conn.Close()
	}

	log.Println("Network monitor: listening for NetworkManager D-Bus signals")
	return stop, nil
}

// startNmcliPoller is the fallback when D-Bus is not available.
func startNmcliPoller(callback func()) func() {
	stopCh := make(chan struct{})
	var once sync.Once

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				callback()
			}
		}
	}()

	log.Println("Network monitor: using nmcli polling fallback (30s interval)")
	return func() { once.Do(func() { close(stopCh) }) }
}

// getCurrentSSIDPlatform returns the current WiFi SSID on Linux.
// It tries D-Bus first, then falls back to nmcli.
func getCurrentSSIDPlatform() string {
	ssid := getSSIDViaDbus()
	if ssid != "" {
		return ssid
	}
	return getSSIDViaNmcli()
}

func getSSIDViaDbus() string {
	conn, err := dbus.SystemBus()
	if err != nil {
		return ""
	}
	defer conn.Close()

	// Get list of devices
	nmObj := conn.Object("org.freedesktop.NetworkManager", "/org/freedesktop/NetworkManager")
	var devices []dbus.ObjectPath
	err = nmObj.Call("org.freedesktop.NetworkManager.GetDevices", 0).Store(&devices)
	if err != nil {
		return ""
	}

	for _, devPath := range devices {
		devObj := conn.Object("org.freedesktop.NetworkManager", devPath)

		// Check if device is a WiFi device (type 2)
		devType, err := devObj.GetProperty("org.freedesktop.NetworkManager.Device.DeviceType")
		if err != nil {
			continue
		}
		if dt, ok := devType.Value().(uint32); !ok || dt != 2 {
			continue
		}

		// Get active access point
		apProp, err := devObj.GetProperty("org.freedesktop.NetworkManager.Device.Wireless.ActiveAccessPoint")
		if err != nil {
			continue
		}
		apPath, ok := apProp.Value().(dbus.ObjectPath)
		if !ok || apPath == "/" || apPath == "" {
			continue
		}

		// Get SSID from access point
		apObj := conn.Object("org.freedesktop.NetworkManager", apPath)
		ssidProp, err := apObj.GetProperty("org.freedesktop.NetworkManager.AccessPoint.Ssid")
		if err != nil {
			continue
		}
		ssidBytes, ok := ssidProp.Value().([]byte)
		if !ok || len(ssidBytes) == 0 {
			continue
		}
		return string(ssidBytes)
	}
	return ""
}

func getSSIDViaNmcli() string {
	out, err := exec.Command("nmcli", "-t", "-f", "active,ssid", "dev", "wifi").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "yes:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "yes:"))
		}
	}
	return ""
}

// getNetworkTypePlatform returns "wifi", "ethernet", or "none" on Linux.
func getNetworkTypePlatform() string {
	ssid := getCurrentSSIDPlatform()
	if ssid != "" {
		return "wifi"
	}

	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return "none"
	}
	if strings.TrimSpace(string(out)) != "" {
		return "ethernet"
	}
	return "none"
}
