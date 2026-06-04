//go:build tools

// Package engine: this file exists only to pin golang.org/x/mobile into the
// module graph so `gomobile bind` (Android AAR / iOS xcframework) works — since
// x/mobile now requires itself to be a dependency of the bound module. The
// `tools` build tag excludes it from every normal build/test/vet, so the engine
// stays artefact-clean; nothing here is compiled into any binary.
package engine

import (
	_ "golang.org/x/mobile/bind"
)
