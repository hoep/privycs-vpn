#!/usr/bin/env bash
#
# Build AmneziaWG as an XCFramework via gomobile-bind. Output goes
# to ios/ThirdParty/AmneziaWG/AmneziaWG.xcframework — gitignored,
# rebuilt on CI + locally as needed.
#
# Source upstream: https://github.com/amnezia-vpn/amneziawg-go
# License: MIT (same as wireguard-go)
#
# Requires (on the build host):
#   - macOS with Xcode CommandLineTools
#   - Go 1.22+
#   - gomobile (auto-installed if missing)
#
# Usage:
#   cd ios
#   ./Scripts/build-amneziawg-xcframework.sh
#
# Run from CI: included in ios-release.yml before xcodebuild archive.

set -euo pipefail

PINNED_REVISION="${AMNEZIAWG_REVISION:-main}"  # set in CI for reproducibility
WORKDIR="$(cd "$(dirname "$0")/.." && pwd)"
OUTDIR="$WORKDIR/ThirdParty/AmneziaWG"
SRCDIR="$OUTDIR/src"

echo "==> Privycs AmneziaWG XCFramework builder"
echo "    pin = $PINNED_REVISION"
echo "    out = $OUTDIR"

# 1. Toolchain check
command -v go >/dev/null || { echo "ERR: go not installed"; exit 1; }
command -v xcodebuild >/dev/null || { echo "ERR: Xcode CLT not installed"; exit 1; }

# 2. gomobile install
if ! command -v gomobile >/dev/null; then
    echo "==> Installing gomobile"
    go install golang.org/x/mobile/cmd/gomobile@latest
    go install golang.org/x/mobile/cmd/gobind@latest
    export PATH="$(go env GOPATH)/bin:$PATH"
fi

# 3. Clone / refresh amneziawg-go source
mkdir -p "$OUTDIR"
if [ ! -d "$SRCDIR/.git" ]; then
    echo "==> Cloning amneziawg-go"
    git clone https://github.com/amnezia-vpn/amneziawg-go.git "$SRCDIR"
fi
(cd "$SRCDIR" && git fetch --all --tags && git checkout "$PINNED_REVISION")

# 4. gomobile init (idempotent)
gomobile init

# 5. Build XCFramework
#    Targets: ios (arm64), iossimulator (arm64), iossimulator (amd64)
#    The output is a single .xcframework that Xcode can link from
#    the PrivycsVPNTunnel target.
echo "==> Running gomobile bind"
(cd "$SRCDIR" && gomobile bind \
    -target=ios,iossimulator \
    -o "$OUTDIR/AmneziaWG.xcframework" \
    -prefix=Amnezia \
    ./tun \
    ./device)

echo "==> Done: $OUTDIR/AmneziaWG.xcframework"
ls -la "$OUTDIR/AmneziaWG.xcframework" 2>/dev/null || true
