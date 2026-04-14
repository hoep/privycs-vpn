package main

import (
	"log"
	"sync"
	"time"
)

// AutoConnectManager handles auto-connect on app start.
// When ConnectOnDemand is enabled, it delegates to NetworkMonitor for
// network-aware connection management. Otherwise, it falls back to the
// simple one-shot connect-on-start behavior.
type AutoConnectManager struct {
	mu             sync.Mutex
	running        bool
	stopCh         chan struct{}
	networkMonitor *NetworkMonitor
}

// NewAutoConnectManager creates a new auto-connect manager
func NewAutoConnectManager() *AutoConnectManager {
	return &AutoConnectManager{
		networkMonitor: NewNetworkMonitor(),
	}
}

// IsRunning returns whether auto-connect is active
func (ac *AutoConnectManager) IsRunning() bool {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	return ac.running
}

// NetworkMonitor returns the underlying network monitor for status queries
func (ac *AutoConnectManager) NetworkMonitor() *NetworkMonitor {
	return ac.networkMonitor
}

// StartWithOnDemand begins network-aware connect-on-demand monitoring.
// This replaces the simple Start() when ConnectOnDemand settings are enabled.
func (ac *AutoConnectManager) StartWithOnDemand(settings *ConnectOnDemandSettings, connectFn func(), disconnectFn func(), isConnectedFn func() bool) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if ac.running {
		return
	}

	ac.stopCh = make(chan struct{})
	ac.running = true

	log.Println("Auto-connect: using connect-on-demand mode")
	ac.networkMonitor.Start(settings, connectFn, disconnectFn, isConnectedFn)
}

// Start begins auto-connect on start: waits briefly then calls connectFn
// if the VPN is not already connected. This is the legacy one-shot behavior.
func (ac *AutoConnectManager) Start(connectFn func(), isConnectedFn func() bool) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if ac.running {
		return
	}

	ac.stopCh = make(chan struct{})
	ac.running = true
	log.Println("Auto-connect: enabled, will connect after startup")

	go func() {
		// Wait for app initialization to complete
		select {
		case <-time.After(3 * time.Second):
		case <-ac.stopCh:
			return
		}

		// Check if already connected (e.g. tunnel survived app restart)
		if isConnectedFn() {
			log.Println("Auto-connect: already connected, skipping")
			return
		}

		log.Println("Auto-connect: triggering connection")
		connectFn()
	}()
}

// Stop ends auto-connect monitoring (both legacy and on-demand modes)
func (ac *AutoConnectManager) Stop() {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if !ac.running {
		return
	}

	// Stop network monitor if it was running
	ac.networkMonitor.Stop()

	close(ac.stopCh)
	ac.running = false
	log.Println("Auto-connect: stopped")
}
