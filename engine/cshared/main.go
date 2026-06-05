//go:build cshared

// Command cshared exposes the Smart Decision Engine ffi.Session through a C ABI
// for the iOS/macOS Engine.xcframework (built with -buildmode=c-shared
// -tags cshared). Swift never sees a Go pointer: sessions live in a Go-side
// registry keyed by an int64 handle. Build:
//
//	GOOS=ios GOARCH=arm64 CGO_ENABLED=1 \
//	  go build -tags cshared -buildmode=c-archive -o libengine_ios.a ./cshared
//
// (an iOS/macOS C toolchain via Xcode clang is required; see
// ios/Scripts/build-engine-xcframework.sh)
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"sync"
	"unsafe"

	"github.com/hoep/privycs-vpn/engine/ffi"
)

var (
	reg   = map[int64]*ffi.Session{}
	regMu sync.Mutex
	next  int64
)

//export PvcsEngineNew
func PvcsEngineNew(profilesJSON *C.char) C.int64_t {
	s := ffi.NewSession(C.GoString(profilesJSON))
	regMu.Lock()
	next++
	h := next
	reg[h] = s
	regMu.Unlock()
	return C.int64_t(h)
}

func lookup(h C.int64_t) *ffi.Session {
	regMu.Lock()
	defer regMu.Unlock()
	return reg[int64(h)]
}

//export PvcsEngineObserveConnect
func PvcsEngineObserveConnect(h C.int64_t, protocol, country *C.char, awgAvailable C.int) {
	lookup(h).ObserveConnect(C.GoString(protocol), C.GoString(country), awgAvailable != 0)
}

//export PvcsEngineObserveDisconnect
func PvcsEngineObserveDisconnect(h C.int64_t) { lookup(h).ObserveDisconnect() }

//export PvcsEngineObserveHealth
func PvcsEngineObserveHealth(h C.int64_t, state *C.char) {
	lookup(h).ObserveHealth(C.GoString(state))
}

// PvcsEnginePollDecisions returns a malloc'd C string (JSON array). The caller
// MUST free it with PvcsEngineFreeString.
//
//export PvcsEnginePollDecisions
func PvcsEnginePollDecisions(h C.int64_t) *C.char {
	s := lookup(h)
	if s == nil {
		return C.CString("[]")
	}
	return C.CString(s.PollDecisions())
}

//export PvcsEngineFreeString
func PvcsEngineFreeString(p *C.char) { C.free(unsafe.Pointer(p)) }

//export PvcsEngineClose
func PvcsEngineClose(h C.int64_t) {
	regMu.Lock()
	s := reg[int64(h)]
	delete(reg, int64(h))
	regMu.Unlock()
	if s != nil {
		s.Close()
	}
}

func main() {}
