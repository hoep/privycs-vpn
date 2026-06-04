#!/usr/bin/env bash
#
# Build the Smart Decision Engine FFI surface (engine/ffi) as an XCFramework via
# gomobile-bind, mirroring build-amneziawg-xcframework.sh. The engine source is
# local to this repo (engine/ffi), so there is no clone step.
#
# Output: ios/ThirdParty/Engine/Engine.xcframework  (gitignored, rebuilt in CI)
# Generated Swift surface (prefix=Pvcs):
#   PvcsNewSession(_:) -> PvcsSession?
#   PvcsSession.observeConnect() / observeDisconnect() / observeHealth(_:)
#   PvcsSession.pollDecisions() -> String   ; .close()
#
# Requires: macOS + Xcode CLT, Go 1.25+, gomobile (auto-installed if missing).
# Run from CI: included in ios-release.yml / ios-build.yml before xcodebuild.
set -euo pipefail

WORKDIR="$(cd "$(dirname "$0")/.." && pwd)"          # ios/
REPO="$(cd "$WORKDIR/.." && pwd)"
ENGINE_DIR="$REPO/engine"
OUTDIR="$WORKDIR/ThirdParty/Engine"
OUT_XC="$OUTDIR/Engine.xcframework"

echo "==> Privycs Smart-Decision-Engine XCFramework builder"
echo "    src = $ENGINE_DIR/ffi"
echo "    out = $OUTDIR"

command -v go >/dev/null || { echo "ERR: go not installed"; exit 1; }
command -v xcodebuild >/dev/null || { echo "ERR: Xcode CLT not installed"; exit 1; }

if ! command -v gomobile >/dev/null; then
    echo "==> Installing gomobile"
    go install golang.org/x/mobile/cmd/gomobile@latest
    go install golang.org/x/mobile/cmd/gobind@latest
    export PATH="$(go env GOPATH)/bin:$PATH"
fi

mkdir -p "$OUTDIR"
rm -rf "$OUT_XC"

gomobile init

# iOS device + simulator only. The engine is linked exclusively by the iOS App
# target (it lives in the app, never the NetworkExtension, and no tvOS target
# depends on it), so tvOS slices aren't needed — and this gomobile pin doesn't
# accept a "tvos" target anyway.
echo "==> Running gomobile bind"
(cd "$ENGINE_DIR" && gomobile bind \
    -target=ios,iossimulator \
    -prefix=Pvcs \
    -o "$OUT_XC" \
    ./ffi)

echo "==> Done: $OUT_XC"
ls -la "$OUT_XC" 2>/dev/null || true
