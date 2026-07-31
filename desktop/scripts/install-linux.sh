#!/usr/bin/env bash
#
# Privycs VPN — Linux installer ("setup.exe for Linux").
#
# Installs the Privycs VPN desktop app AND its VPN protocol dependencies
# (WireGuard, OpenVPN, strongSwan/IPSec) in one shot, across the common distro
# families. AmneziaWG has no package in the default repos, so it is opt-in —
# pick ONE:
#   --with-amneziawg-kernel   native DKMS kernel module (faster; via the upstream
#                             Ubuntu/Debian PPA or Fedora COPR). awg-quick always
#                             tries the native path first, so it is used
#                             automatically once the module is present.
#   --with-amneziawg          userspace backend (amneziawg-go), built from source.
#                             No kernel module, unaffected by kernel upgrades,
#                             costs a little CPU.
# --with-amneziawg-kernel falls back to the userspace backend if the module
# can't be installed, so you always end up with a working AmneziaWG.
#
# Quick start (end users) — the downloads area is password-protected, so pass
# the download token to BOTH curl (for this script) and the script (--token):
#   curl -fsSL -u 'dl:TOKEN' https://www.privycs.com/downloads/install-linux-client.sh | sudo bash -s -- --token TOKEN
#
# Options:
#   --with-amneziawg     AmneziaWG userspace backend (amneziawg-tools + amneziawg-go,
#                        source build; no kernel module, survives kernel updates)
#   --with-amneziawg-kernel
#                        AmneziaWG NATIVE kernel module via DKMS (faster; Ubuntu/
#                        Debian PPA, Fedora COPR). Falls back to userspace if
#                        unavailable.
#   --version X.Y.Z.W    install a specific version (default: latest)
#   --token TOKEN        download auth token (or $PRIVYCS_DOWNLOAD_TOKEN)
#   --base URL           download base (default: $PRIVYCS_DOWNLOAD_BASE or
#                        https://www.privycs.com/downloads)
#   --deb PATH           install a local .deb instead of downloading (apt only)
#   --rpm PATH           install a local .rpm instead of downloading (dnf/zypper)
#   --deps-only          install dependencies but not the app
#   --no-deps            install the app but not the VPN dependencies
#
# Env overrides: PRIVYCS_DOWNLOAD_BASE, PRIVYCS_VERSION.
set -euo pipefail

# ---- config / args --------------------------------------------------------
DOWNLOAD_BASE="${PRIVYCS_DOWNLOAD_BASE:-https://www.privycs.com/downloads}"
VERSION="${PRIVYCS_VERSION:-}"
WITH_AWG=0
WITH_AWG_KERNEL=0
LOCAL_DEB=""
LOCAL_RPM=""
DO_DEPS=1
DO_APP=1
DOWNLOAD_TOKEN="${PRIVYCS_DOWNLOAD_TOKEN:-}"

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --with-amneziawg) WITH_AWG=1 ;;
    --with-amneziawg-kernel) WITH_AWG_KERNEL=1 ;;
    --version) VERSION="${2:?}"; shift ;;
    --base)    DOWNLOAD_BASE="${2:?}"; shift ;;
    --token)   DOWNLOAD_TOKEN="${2:?}"; shift ;;
    --token=*) DOWNLOAD_TOKEN="${1#*=}" ;;
    --deb)     LOCAL_DEB="${2:?}"; shift ;;
    --rpm)     LOCAL_RPM="${2:?}"; shift ;;
    --deps-only) DO_APP=0 ;;
    --no-deps)   DO_DEPS=0 ;;
    -h|--help) sed -n '2,38p' "$0"; exit 0 ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
  shift
done

# Download auth: the privycs.com downloads area is password-protected (user
# 'dl'), exactly like the server install.sh / update.sh. Pass the same token via
# --token / $PRIVYCS_DOWNLOAD_TOKEN so the internal downloads below authenticate.
CURL_AUTH=""
[ -n "$DOWNLOAD_TOKEN" ] && CURL_AUTH="-u dl:$DOWNLOAD_TOKEN"

# ---- must be root ---------------------------------------------------------
if [ "$(id -u)" != "0" ]; then
  log "Re-running with sudo…"
  exec sudo -E bash "$0" \
    $([ "$WITH_AWG" = 1 ] && echo --with-amneziawg) \
    $([ "$WITH_AWG_KERNEL" = 1 ] && echo --with-amneziawg-kernel) \
    $([ -n "$VERSION" ] && echo --version "$VERSION") \
    --base "$DOWNLOAD_BASE" \
    $([ -n "$DOWNLOAD_TOKEN" ] && echo --token "$DOWNLOAD_TOKEN") \
    $([ -n "$LOCAL_DEB" ] && echo --deb "$LOCAL_DEB") \
    $([ -n "$LOCAL_RPM" ] && echo --rpm "$LOCAL_RPM") \
    $([ "$DO_APP" = 0 ] && echo --deps-only) \
    $([ "$DO_DEPS" = 0 ] && echo --no-deps)
fi

command -v curl >/dev/null 2>&1 || die "curl is required (install it first: your-package-manager install curl)"

# ---- detect package manager ----------------------------------------------
if   command -v apt-get >/dev/null 2>&1; then PM=apt
elif command -v dnf     >/dev/null 2>&1; then PM=dnf
elif command -v pacman  >/dev/null 2>&1; then PM=pacman
elif command -v zypper  >/dev/null 2>&1; then PM=zypper
else die "unsupported distro: need apt, dnf, pacman or zypper. Install deps manually (see docs) and drop the binary in /usr/local/bin."
fi
log "Detected package manager: $PM"

# ---- install VPN + runtime dependencies -----------------------------------
install_deps() {
  log "Installing VPN protocol tools + GUI runtime libraries…"
  case "$PM" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      apt-get update -y
      # webkit 4.1 (newer) with a 4.0 fallback for older releases (20.04).
      local webkit="libwebkit2gtk-4.1-0"
      apt-cache show "$webkit" >/dev/null 2>&1 || webkit="libwebkit2gtk-4.0-37"
      # resolvconf provides the `resolvconf` command wg-quick needs to apply the
      # DNS= lines from Privycs WireGuard configs — without it the whole WG DNS
      # stack silently fails to set the tunnel resolver.
      apt-get install -y \
        wireguard-tools openvpn strongswan strongswan-swanctl libcharon-extra-plugins resolvconf \
        libgtk-3-0 "$webkit" libayatana-appindicator3-1 || \
        warn "some packages failed to install — check the output above"
      ;;
    dnf)
      dnf install -y \
        wireguard-tools openvpn strongswan openresolv \
        gtk3 webkit2gtk4.1 libappindicator-gtk3 || \
        warn "some packages failed — on RHEL you may need EPEL (dnf install epel-release)"
      ;;
    pacman)
      pacman -Sy --needed --noconfirm \
        wireguard-tools openvpn strongswan openresolv \
        gtk3 webkit2gtk-4.1 libayatana-appindicator || \
        warn "some packages failed to install"
      ;;
    zypper)
      zypper --non-interactive install -y \
        wireguard-tools openvpn strongswan openresolv \
        gtk3 libwebkit2gtk-4_1-0 libayatana-appindicator3-1 || \
        warn "some packages failed to install"
      ;;
  esac
  log "Dependencies done (WireGuard / OpenVPN / IPSec)."
}

# ---- Go toolchain (for the AmneziaWG source build) ------------------------
# amneziawg-go's go.mod pins a RECENT Go (currently `go 1.25.x`). Distro
# packages are far older — Ubuntu 22.04's `golang` is Go 1.18, which dies with
#   go.mod:3: invalid go version '1.25.0': must match format 1.23
# So never rely on the distro package: use the system Go only when it's new
# enough, otherwise fetch the official toolchain into /usr/local/go.
GO_MIN_MINOR=23        # need >= 1.23
GO_PIN="1.25.5"        # official toolchain installed when the system one is too old
GO_BIN=""

ensure_modern_go() {
  if command -v go >/dev/null 2>&1; then
    local v maj min
    v="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
    maj="${v%%.*}"; min="${v#*.}"; min="${min%%.*}"
    if [ "${maj:-0}" -gt 1 ] || { [ "${maj:-0}" -eq 1 ] && [ "${min:-0}" -ge "$GO_MIN_MINOR" ]; }; then
      GO_BIN="$(command -v go)"
      log "Using system Go $v"
      return 0
    fi
    warn "System Go $v is too old for amneziawg-go (needs >= 1.$GO_MIN_MINOR) — fetching the official toolchain."
  else
    log "No Go toolchain found — fetching the official one."
  fi

  local arch
  case "$(uname -m)" in
    x86_64)        arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) warn "unsupported architecture $(uname -m) for the Go toolchain"; return 1 ;;
  esac

  local tgz; tgz="$(mktemp --suffix=.tar.gz)"
  log "Downloading Go $GO_PIN ($arch)…"
  if ! curl -fSL -o "$tgz" "https://go.dev/dl/go${GO_PIN}.linux-${arch}.tar.gz"; then
    warn "Go download failed"; rm -f "$tgz"; return 1
  fi
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$tgz"
  rm -f "$tgz"
  GO_BIN=/usr/local/go/bin/go
  [ -x "$GO_BIN" ] || { warn "Go install to /usr/local/go failed"; return 1; }
  log "Installed Go $GO_PIN to /usr/local/go"
}

# ---- AmneziaWG NATIVE kernel module (opt-in, DKMS) ------------------------
# awg-quick ALWAYS tries the native path first (`ip link add … type amneziawg`)
# and only falls back to the amneziawg-go userspace daemon when the kernel
# module is missing. Native is faster (crypto + packet path in-kernel); the
# userspace backend costs some CPU but needs no kernel module and survives
# kernel updates untouched.
#
# The upstream "manual build" route is nasty on kernel >= 5.6 (it wants the FULL
# kernel source tree, not just headers), so use the distro channels the project
# actually publishes: a PPA on Ubuntu/Debian derivatives, COPR on Fedora/RHEL.
# Both ship it as DKMS, so it rebuilds itself on every kernel upgrade.
#
# Returns non-zero if the native path isn't available — the caller then falls
# back to the userspace backend rather than leaving the user with nothing.
install_amneziawg_kernel() {
  log "Installing AmneziaWG KERNEL module (native, DKMS)…"
  case "$PM" in
    apt)
      export DEBIAN_FRONTEND=noninteractive
      apt-get install -y software-properties-common python3-launchpadlib gnupg2 dkms \
        "linux-headers-$(uname -r)" || warn "some prerequisites failed to install"
      log "Adding ppa:amnezia/ppa…"
      add-apt-repository -y ppa:amnezia/ppa || { warn "could not add ppa:amnezia/ppa"; return 1; }
      apt-get update -y
      # `amneziawg` is the DKMS kernel module; it pulls amneziawg-tools along,
      # which replaces any source-built awg/awg-quick with apt-managed ones.
      apt-get install -y amneziawg || { warn "apt install amneziawg failed"; return 1; }
      ;;
    dnf)
      dnf install -y dnf-plugins-core dkms "kernel-devel-$(uname -r)" || warn "some prerequisites failed"
      dnf copr enable -y amneziavpn/amneziawg || { warn "could not enable the amneziawg COPR"; return 1; }
      dnf install -y amneziawg-dkms amneziawg-tools || { warn "dnf install amneziawg-dkms failed"; return 1; }
      ;;
    pacman)
      warn "Arch/Manjaro: the module lives in the AUR and needs an AUR helper — run it yourself:"
      warn "  yay -S amneziawg-dkms amneziawg-tools"
      return 1
      ;;
    zypper)
      warn "openSUSE: upstream ships no kernel package — using the userspace backend instead."
      return 1
      ;;
  esac

  if modprobe amneziawg 2>/dev/null || lsmod | grep -q '^amneziawg'; then
    log "AmneziaWG kernel module is active — awg-quick will take the NATIVE path."
    return 0
  fi
  warn "The amneziawg module did not load (the DKMS build may have failed — check: dkms status)."
  return 1
}

# ---- AmneziaWG userland (opt-in, source build) ----------------------------
install_amneziawg() {
  log "Installing AmneziaWG userland (awg-quick + amneziawg-go)…"
  # NOTE: no `golang`/`go` here on purpose — ensure_modern_go handles the
  # toolchain (the distro one is too old to build amneziawg-go).
  case "$PM" in
    apt)    apt-get install -y git make gcc ;;
    dnf)    dnf install -y git make gcc ;;
    pacman) pacman -Sy --needed --noconfirm git make gcc ;;
    zypper) zypper --non-interactive install -y git make gcc ;;
  esac

  ensure_modern_go || { warn "No usable Go toolchain — skipping AmneziaWG. WireGuard/OpenVPN/IPSec still work."; return 0; }
  export PATH="$(dirname "$GO_BIN"):$PATH"

  local tmp; tmp="$(mktemp -d)"
  (
    cd "$tmp"
    git clone --depth 1 https://github.com/amnezia-vpn/amneziawg-tools
    make -C amneziawg-tools/src
    make -C amneziawg-tools/src install                          # awg + awg-quick -> /usr/bin
    git clone --depth 1 https://github.com/amnezia-vpn/amneziawg-go
    make -C amneziawg-go
    install -m 0755 amneziawg-go/amneziawg-go /usr/bin/amneziawg-go
  ) || { warn "AmneziaWG build failed — WireGuard/OpenVPN/IPSec still work. See docs for manual steps."; rm -rf "$tmp"; return 0; }
  rm -rf "$tmp"
  log "AmneziaWG installed (awg-quick + amneziawg-go userspace backend)."
}

# ---- resolve version ------------------------------------------------------
resolve_version() {
  [ -n "$VERSION" ] && return 0
  log "Resolving latest version…"

  # Capture the HTTP status so we can tell "needs a token" (401/403) apart from
  # "genuinely unreachable" — the downloads area is password-protected, and a
  # bare "not reachable" message sent people hunting a non-existent outage.
  local tmpf code
  tmpf="$(mktemp)"
  code="$(curl -sS -o "$tmpf" -w '%{http_code}' $CURL_AUTH "$DOWNLOAD_BASE/latest_version_linux.txt" 2>/dev/null || echo 000)"
  [ "$code" = "200" ] && VERSION="$(tr -d '[:space:]' < "$tmpf")"
  rm -f "$tmpf"

  if [ -z "${VERSION:-}" ]; then
    case "$code" in
      401|403)
        warn "The downloads area is password-protected (HTTP $code) — the URL is fine, the request just wasn't authenticated."
        if [ -n "$DOWNLOAD_TOKEN" ]; then
          warn "A token WAS supplied but the server rejected it — double-check it."
        else
          warn "Pass the download token — curl needs it too:"
          warn "  curl -fsSL -u 'dl:TOKEN' $DOWNLOAD_BASE/install-linux-client.sh | sudo bash -s -- --token TOKEN"
          warn "(or export PRIVYCS_DOWNLOAD_TOKEN=TOKEN)"
        fi
        ;;
      000) warn "Could not reach $DOWNLOAD_BASE (network / DNS / TLS)." ;;
      404) warn "$DOWNLOAD_BASE/latest_version_linux.txt not found (HTTP 404)." ;;
      *)   warn "Unexpected HTTP $code from $DOWNLOAD_BASE/latest_version_linux.txt" ;;
    esac
    warn "Alternatives:"
    warn "  • install a locally-downloaded package:  --deb ./privycs-vpn-linux-amd64-<ver>.deb"
    warn "  • point at another host:                 PRIVYCS_DOWNLOAD_BASE=https://host"
    die "could not determine the latest version"
  fi
  log "Latest version: $VERSION"
}

# ---- install the app ------------------------------------------------------
install_app_apt() {
  local deb
  if [ -n "$LOCAL_DEB" ]; then
    deb="$LOCAL_DEB"
    [ -f "$deb" ] || die "local .deb not found: $deb"
  else
    resolve_version
    deb="$(mktemp --suffix=.deb)"
    log "Downloading privycs-vpn-linux-amd64-$VERSION.deb…"
    curl -fSL $CURL_AUTH -o "$deb" "$DOWNLOAD_BASE/privycs-vpn-linux-amd64-$VERSION.deb" \
      || die "download failed from $DOWNLOAD_BASE"
  fi
  log "Installing the .deb (pulls any remaining recommends)…"
  apt-get install -y "$deb"
  [ -n "$LOCAL_DEB" ] || rm -f "$deb"
}

install_app_rpm() {
  local rpm
  if [ -n "$LOCAL_RPM" ]; then
    rpm="$LOCAL_RPM"
    [ -f "$rpm" ] || die "local .rpm not found: $rpm"
  else
    resolve_version
    rpm="$(mktemp --suffix=.rpm)"
    log "Downloading privycs-vpn-linux-amd64-$VERSION.rpm…"
    curl -fSL $CURL_AUTH -o "$rpm" "$DOWNLOAD_BASE/privycs-vpn-linux-amd64-$VERSION.rpm" \
      || die "download failed from $DOWNLOAD_BASE"
  fi
  log "Installing the .rpm (resolves deps + recommends)…"
  if [ "$PM" = dnf ]; then
    # A local .rpm is not GPG-checked by dnf (localpkg_gpgcheck defaults off),
    # and dnf install <file> resolves the soname deps against the repos.
    dnf install -y "$rpm"
  else
    # zypper refuses an unsigned rpm non-interactively without this flag; ours
    # is unsigned (nfpm builds no signature).
    zypper --non-interactive install -y --allow-unsigned-rpm "$rpm"
  fi
  [ -n "$LOCAL_RPM" ] || rm -f "$rpm"
}

install_app_binary() {
  resolve_version
  log "Downloading standalone binary privycs-vpn-linux-amd64-$VERSION…"
  curl -fSL $CURL_AUTH -o /usr/local/bin/privycs-vpn "$DOWNLOAD_BASE/privycs-vpn-linux-amd64-$VERSION" \
    || die "download failed from $DOWNLOAD_BASE"
  chmod 0755 /usr/local/bin/privycs-vpn
  # Desktop launcher entry (the .deb ships this; for rpm/arch/suse we write it).
  cat > /usr/share/applications/privycs-vpn.desktop <<'DESK'
[Desktop Entry]
Name=Privycs VPN
Comment=Multi-protocol VPN client (WireGuard, OpenVPN, IPSec)
Exec=/usr/local/bin/privycs-vpn
Icon=privycs-vpn
Terminal=false
Type=Application
Categories=Network;Security;
DESK
  command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database /usr/share/applications/ 2>/dev/null || true
  log "Binary installed to /usr/local/bin/privycs-vpn."
}

install_app() {
  case "$PM" in
    apt)         install_app_apt ;;
    dnf|zypper)  install_app_rpm ;;
    *)           install_app_binary ;;   # pacman + anything else: raw binary
  esac
}

# ---- run ------------------------------------------------------------------
[ "$DO_DEPS" = 1 ] && install_deps
# AmneziaWG: native kernel module if asked for, else (or on failure) userspace.
if [ "$WITH_AWG_KERNEL" = 1 ]; then
  if ! install_amneziawg_kernel; then
    warn "Native kernel module unavailable — installing the userspace backend instead."
    install_amneziawg
  fi
elif [ "$WITH_AWG" = 1 ]; then
  install_amneziawg
fi
[ "$DO_APP"  = 1 ] && install_app

echo
log "Done. Launch 'Privycs VPN' from your applications menu, or run: privycs-vpn"
if [ "$WITH_AWG" != 1 ] && [ "$WITH_AWG_KERNEL" != 1 ]; then
  warn "AmneziaWG was NOT installed. WireGuard/OpenVPN/IPSec work now. For the"
  warn "DPI-resistant AmneziaWG protocol, re-run with --with-amneziawg (needs a"
  warn "Go toolchain) or follow the manual steps in the Desktop Client docs."
fi
