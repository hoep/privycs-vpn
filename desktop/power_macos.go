//go:build darwin && cgo

package main

// macOS power-event bridge — subscribe to NSWorkspace's
// will-sleep / did-wake notifications so Privycs can react to system
// sleep/wake within 1-3 s instead of waiting for charon's DPD
// (~90 s, see protocol_ipsec_macos_swanctl.go) or the tunnel-health
// ICMP probe (~60 s, tunnel_health_monitor.go) to detect a dead
// tunnel after the system woke up with stale NAT mappings.
//
// Why NSWorkspace and not IOKit/IOPMScheduleNotificationsForSystemPower:
// NSWorkspace is the high-level public API, hands events on the main
// run loop, and is what every well-behaved macOS app uses for sleep/
// wake awareness. IOKit gives lower-level control over WHEN to allow
// sleep (you can vote against it) which Privycs does not need —
// we just want to know AFTER the fact. NSWorkspace is the right tool.
//
// Threading: NSWorkspace dispatches on AppKit's main thread. We hop
// into Go via cgo-export, then immediately send onto a buffered chan
// so the Go runtime takes over and the AppKit thread is freed.
// dispatchPowerEvents drains the chan and calls the registered
// handler — the handler runs on a Go goroutine, never blocks AppKit.

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AppKit
#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>

extern void privycsOnSystemWillSleep(void);
extern void privycsOnSystemDidWake(void);

static void privycsRegisterPowerNotifications(void) {
    NSNotificationCenter *nc = [[NSWorkspace sharedWorkspace] notificationCenter];
    [nc addObserverForName:NSWorkspaceWillSleepNotification
                    object:nil
                     queue:nil
                usingBlock:^(NSNotification * _Nonnull note) {
                    privycsOnSystemWillSleep();
                }];
    [nc addObserverForName:NSWorkspaceDidWakeNotification
                    object:nil
                     queue:nil
                usingBlock:^(NSNotification * _Nonnull note) {
                    privycsOnSystemDidWake();
                }];
}
*/
import "C"

import (
	"log"
	"sync"
)

// powerEvent enumerates which system-power event fired. We pipe these
// through a single channel + dispatcher rather than calling handlers
// from the cgo callback directly so the AppKit thread is never
// blocked by Go-side work.
type powerEvent int

const (
	powerEventWillSleep powerEvent = iota
	powerEventDidWake
)

var (
	powerEventCh   chan powerEvent
	powerEventOnce sync.Once

	// Handlers installed by the App at init time. Both nullable —
	// handler-less platforms (or test contexts) become no-ops.
	powerWillSleepHandler func()
	powerDidWakeHandler   func()
)

// RegisterMacOSPowerEvents wires NSWorkspace notifications to the
// supplied Go handlers. Idempotent — calling more than once preserves
// the FIRST registration. App-startup is the right place; the
// notification observer survives until process exit.
//
// willSleep / didWake handlers run on a dedicated goroutine, NOT on
// the AppKit main thread. Long-running work (e.g. swanctl --terminate
// + --initiate which can take seconds) is therefore safe.
func RegisterMacOSPowerEvents(willSleep, didWake func()) {
	powerEventOnce.Do(func() {
		powerWillSleepHandler = willSleep
		powerDidWakeHandler = didWake
		// Buffer 4: a sleep+wake pair plus margin. The dispatcher
		// drains continuously; this is just to absorb the blip
		// between the cgo callback and the goroutine pickup.
		powerEventCh = make(chan powerEvent, 4)
		go dispatchPowerEvents()
		C.privycsRegisterPowerNotifications()
		log.Printf("PowerEvents: NSWorkspace observer registered (willSleep + didWake)")
	})
}

// dispatchPowerEvents drains the power-event channel and invokes the
// registered handler. Runs on its own goroutine for the lifetime of
// the process. Recovers from handler panics so a buggy handler can't
// kill the dispatcher and leave subsequent events unhandled.
func dispatchPowerEvents() {
	for ev := range powerEventCh {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("PowerEvents: handler panic recovered: %v", r)
				}
			}()
			switch ev {
			case powerEventWillSleep:
				log.Printf("PowerEvents: system will sleep")
				if powerWillSleepHandler != nil {
					powerWillSleepHandler()
				}
			case powerEventDidWake:
				log.Printf("PowerEvents: system did wake")
				if powerDidWakeHandler != nil {
					powerDidWakeHandler()
				}
			}
		}()
	}
}

//export privycsOnSystemWillSleep
func privycsOnSystemWillSleep() {
	if powerEventCh == nil {
		return
	}
	// Non-blocking send. If the dispatcher is somehow stuck (handler
	// panic-loop blocked the recover) we'd rather drop the event
	// than hang AppKit's main thread.
	select {
	case powerEventCh <- powerEventWillSleep:
	default:
		log.Printf("PowerEvents: willSleep channel full, dropping")
	}
}

//export privycsOnSystemDidWake
func privycsOnSystemDidWake() {
	if powerEventCh == nil {
		return
	}
	select {
	case powerEventCh <- powerEventDidWake:
	default:
		log.Printf("PowerEvents: didWake channel full, dropping")
	}
}
