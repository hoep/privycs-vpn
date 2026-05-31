//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework SystemConfiguration -framework CoreFoundation
#include <netinet/in.h>
#include <string.h>
#include <SystemConfiguration/SystemConfiguration.h>
#include <CoreFoundation/CoreFoundation.h>

extern void goNetworkChanged();

// --- Source 1: SCNetworkReachability (general internet-reachable
// flags). Catches "we lost internet" / "we got internet" type events
// at the IP layer. Does NOT fire when SSID changes without an IP
// change (e.g. roaming between two APs in the same enterprise mesh).
static void reachabilityCallback(SCNetworkReachabilityRef target,
                                  SCNetworkReachabilityFlags flags,
                                  void *info) {
    goNetworkChanged();
}

// --- Source 2: SCDynamicStore notifications on Network/Interface/...
// /AirPort keys. THIS is the source that fires on SSID changes -
// changing AP triggers a property update on State:/Network/Interface
// /<bsdName>/AirPort even if the IP stays the same. Mirrors the
// Windows WlanRegisterNotification add-on.
static void dynamicStoreCallback(SCDynamicStoreRef store,
                                  CFArrayRef changedKeys,
                                  void *info) {
    goNetworkChanged();
}

static SCNetworkReachabilityRef refGlobal = NULL;
static CFRunLoopRef runLoopRef = NULL;
static SCDynamicStoreRef storeRef = NULL;
static CFRunLoopSourceRef storeRunLoopSource = NULL;

// Start the dynamic-store watcher and add it to the current run
// loop. Returns 0 on success, negative on failure. Failure is
// non-fatal at the call site - the reachability watcher is the
// minimum and runs even if SCDynamicStore is unavailable.
static int startDynamicStoreWatch() {
    SCDynamicStoreContext ctx = {0, NULL, NULL, NULL, NULL};
    storeRef = SCDynamicStoreCreate(NULL,
                                    CFSTR("com.privycs.vpn.netmon"),
                                    dynamicStoreCallback,
                                    &ctx);
    if (!storeRef) return -1;

    // Wildcard patterns:
    //   - Any interface's AirPort state (SSID, BSSID, link state).
    //   - Global IPv4 (default route changes).
    //   - Per-interface link state (Ethernet up/down without IP).
    CFMutableArrayRef patterns = CFArrayCreateMutable(NULL, 0,
                                                      &kCFTypeArrayCallBacks);
    if (!patterns) {
        CFRelease(storeRef);
        storeRef = NULL;
        return -2;
    }
    CFArrayAppendValue(patterns,
        CFSTR("State:/Network/Interface/[^/]+/AirPort"));
    CFArrayAppendValue(patterns,
        CFSTR("State:/Network/Global/IPv4"));
    CFArrayAppendValue(patterns,
        CFSTR("State:/Network/Interface/[^/]+/Link"));

    Boolean ok = SCDynamicStoreSetNotificationKeys(storeRef, NULL, patterns);
    CFRelease(patterns);
    if (!ok) {
        CFRelease(storeRef);
        storeRef = NULL;
        return -3;
    }

    storeRunLoopSource = SCDynamicStoreCreateRunLoopSource(NULL, storeRef, 0);
    if (!storeRunLoopSource) {
        CFRelease(storeRef);
        storeRef = NULL;
        return -4;
    }
    CFRunLoopAddSource(CFRunLoopGetCurrent(),
                       storeRunLoopSource,
                       kCFRunLoopDefaultMode);
    return 0;
}

static void stopDynamicStoreWatch() {
    if (storeRunLoopSource && runLoopRef) {
        CFRunLoopRemoveSource(runLoopRef,
                              storeRunLoopSource,
                              kCFRunLoopDefaultMode);
        CFRelease(storeRunLoopSource);
        storeRunLoopSource = NULL;
    }
    if (storeRef) {
        CFRelease(storeRef);
        storeRef = NULL;
    }
}

static int startReachabilityWatch() {
    struct sockaddr_in addr;
    memset(&addr, 0, sizeof(addr));
    addr.sin_len = sizeof(addr);
    addr.sin_family = AF_INET;

    refGlobal = SCNetworkReachabilityCreateWithAddress(NULL, (struct sockaddr *)&addr);
    if (!refGlobal) return -1;

    SCNetworkReachabilityContext ctx = {0, NULL, NULL, NULL, NULL};
    if (!SCNetworkReachabilitySetCallback(refGlobal, reachabilityCallback, &ctx)) {
        CFRelease(refGlobal);
        refGlobal = NULL;
        return -2;
    }

    runLoopRef = CFRunLoopGetCurrent();
    if (!SCNetworkReachabilityScheduleWithRunLoop(refGlobal, runLoopRef, kCFRunLoopDefaultMode)) {
        CFRelease(refGlobal);
        refGlobal = NULL;
        return -3;
    }

    // Add the dynamic-store watcher to the same run loop. Best-effort:
    // failures here are logged Go-side but do not abort the
    // reachability watcher.
    int storeRet = startDynamicStoreWatch();
    // (storeRet is informational; reachability still runs either way.)
    (void)storeRet;

    CFRunLoopRun(); // blocks until stopped
    return 0;
}

static void stopReachabilityWatch() {
    stopDynamicStoreWatch();
    if (refGlobal && runLoopRef) {
        SCNetworkReachabilityUnscheduleFromRunLoop(refGlobal, runLoopRef, kCFRunLoopDefaultMode);
        CFRelease(refGlobal);
        refGlobal = NULL;
    }
    if (runLoopRef) {
        CFRunLoopStop(runLoopRef);
        runLoopRef = NULL;
    }
}
*/
import "C"

import (
	"log"
	"os/exec"
	"strings"
	"sync"
)

// darwinCallback stores the Go callback for the CGo reachability handler.
var darwinCallback struct {
	mu sync.Mutex
	fn func()
}

//export goNetworkChanged
func goNetworkChanged() {
	// Panic recovery on the cgo callback path. SCNetworkReachability
	// invokes this from a CFRunLoop callback running on a non-Go
	// thread; an unrecovered panic in fn() (e.g. anywhere in the
	// COD-driven connectActiveTarget chain — protocol handlers,
	// helper IPC, settings, pool registry) crashes the whole app
	// instead of just terminating the goroutine. User-visible
	// symptom: clicking Disconnect with COD on triggered a reconnect
	// that hard-crashed the app while the same Connect path worked
	// fine after a fresh app launch. Recover + log so we get a
	// diagnostic instead of a vanishing window.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Network monitor (darwin): callback panic recovered: %v", r)
		}
	}()

	darwinCallback.mu.Lock()
	fn := darwinCallback.fn
	darwinCallback.mu.Unlock()

	if fn != nil {
		log.Println("Network monitor: SCNetworkReachability change detected")
		fn()
	}
}

// startPlatformWatcher uses the macOS SystemConfiguration framework to
// receive immediate reachability change notifications.
func startPlatformWatcher(callback func()) (stopFn func(), err error) {
	darwinCallback.mu.Lock()
	darwinCallback.fn = callback
	darwinCallback.mu.Unlock()

	started := make(chan struct{})
	var once sync.Once

	go func() {
		close(started)
		ret := C.startReachabilityWatch() // blocks
		if ret != 0 {
			log.Printf("Network monitor: SCNetworkReachability watch ended with code %d", ret)
		}
	}()

	<-started

	log.Println("Network monitor: listening for macOS reachability changes")
	return func() {
		once.Do(func() {
			darwinCallback.mu.Lock()
			darwinCallback.fn = nil
			darwinCallback.mu.Unlock()
			C.stopReachabilityWatch()
		})
	}, nil
}

// getCurrentSSIDPlatform returns the current WiFi SSID on macOS
// using the airport utility.
func getCurrentSSIDPlatform() string {
	out, err := exec.Command(
		"/System/Library/PrivateFrameworks/Apple80211.framework/Versions/Current/Resources/airport",
		"-I",
	).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SSID:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "SSID:"))
		}
	}
	return ""
}

// getNetworkTypePlatform returns "wifi", "ethernet", or "none" on macOS.
//
// VPN-aware: when a VPN tunnel is up, `route -n get default` returns
// the utun (or ipsec0) gateway and the pre-v0.9.14.73 check then
// lied "ethernet" even though the underlying physical transport was
// gone. v0.9.14.73 reads `interface:` from the route output and
// rejects tun*/utun*/ipsec* / wg* names, falling through to "none"
// so the rules engine doesn't trigger COD on a tunnel-only state.
func getNetworkTypePlatform() string {
	ssid := getCurrentSSIDPlatform()
	if ssid != "" {
		return "wifi"
	}
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return "none"
	}
	hasGateway := false
	iface := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			hasGateway = true
		}
		if strings.HasPrefix(line, "interface:") {
			iface = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	if !hasGateway {
		return "none"
	}
	if isVpnInterfaceNameMacOS(iface) {
		return "none"
	}
	return "ethernet"
}

// isVpnInterfaceNameMacOS reports whether `iface` looks like a VPN
// virtual interface on macOS — utunN / ipsec0 / wg0 / etc. Used to
// avoid mis-classifying a tunnel-only state as "ethernet" when the
// underlying physical transport is actually gone.
func isVpnInterfaceNameMacOS(iface string) bool {
	low := strings.ToLower(iface)
	return strings.HasPrefix(low, "utun") ||
		strings.HasPrefix(low, "tun") ||
		strings.HasPrefix(low, "ipsec") ||
		strings.HasPrefix(low, "wg") ||
		strings.HasPrefix(low, "wireguard")
}
