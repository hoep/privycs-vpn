//go:build darwin

package main

/*
#cgo LDFLAGS: -framework SystemConfiguration -framework CoreFoundation
#include <netinet/in.h>
#include <string.h>
#include <SystemConfiguration/SystemConfiguration.h>
#include <CoreFoundation/CoreFoundation.h>

extern void goNetworkChanged();

static void reachabilityCallback(SCNetworkReachabilityRef target,
                                  SCNetworkReachabilityFlags flags,
                                  void *info) {
    goNetworkChanged();
}

static SCNetworkReachabilityRef refGlobal = NULL;
static CFRunLoopRef runLoopRef = NULL;

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

    CFRunLoopRun(); // blocks until stopped
    return 0;
}

static void stopReachabilityWatch() {
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
func getNetworkTypePlatform() string {
	ssid := getCurrentSSIDPlatform()
	if ssid != "" {
		return "wifi"
	}

	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return "none"
	}
	if strings.Contains(string(out), "gateway:") {
		return "ethernet"
	}
	return "none"
}
