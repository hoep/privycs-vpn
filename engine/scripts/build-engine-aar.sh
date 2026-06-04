#!/usr/bin/env bash
#
# build-engine-aar.sh — gomobile-bind the Smart Decision Engine FFI surface
# (engine/ffi) into an Android AAR consumed by the app as a flat-dir dependency.
#
# Output: android/app/libs/engine.aar  (gitignored — rebuilt in CI before gradle)
#
# Requirements: Go 1.25+, Android NDK (ANDROID_NDK_ROOT or ANDROID_NDK_HOME),
# gomobile + gobind (installed here if missing).
#
# Run locally:  ./engine/scripts/build-engine-aar.sh
# CI: invoked from android-build.yml / android-release.yml before ./gradlew.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENGINE_DIR="$(cd "$HERE/.." && pwd)"            # engine/
REPO="$(cd "$ENGINE_DIR/.." && pwd)"
OUT_DIR="$REPO/android/app/libs"
OUT_AAR="$OUT_DIR/engine.aar"

# Match the app's minSdk (android/app/build.gradle.kts → minSdk = 26).
ANDROID_API="${ANDROID_API:-26}"
# Java package root for the generated bindings → Kotlin sees
# com.privycs.engine.ffi.{Ffi,Session}. Keep in sync with EngineShadow.kt.
JAVA_PKG="com.privycs.engine"

echo "==> installing gomobile/gobind"
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
export PATH="$PATH:$(go env GOPATH)/bin"

mkdir -p "$OUT_DIR"
cd "$ENGINE_DIR"
echo "==> gomobile init"
gomobile init

echo "==> gomobile bind (android, api $ANDROID_API) -> $OUT_AAR"
gomobile bind \
  -target=android \
  -androidapi "$ANDROID_API" \
  -javapkg "$JAVA_PKG" \
  -o "$OUT_AAR" \
  ./ffi

echo "==> built $OUT_AAR"
ls -la "$OUT_AAR"
