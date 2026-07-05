#!/usr/bin/env bash
#
# Privycs VPN — Linux installer ("setup.exe for Linux").
#
# Installs the Privycs VPN desktop app AND its VPN protocol dependencies
# (WireGuard, OpenVPN, strongSwan/IPSec) in one shot, across the common distro
# families. AmneziaWG userland (awg-quick) has no package in the default repos,
# so it is opt-in (--with-amneziawg builds it from source).
#
# Quick start (end users):
#   curl -fsSL https://www.privycs.com/install-linux.sh | sudo bash
#
# Options:
#   --with-amneziawg     also build+install amneziawg-tools + amneziawg-go
#   --version X.Y.Z.W    install a specific version (default: latest)
#   --base URL           download base (default: $PRIVYCS_DOWNLOAD_BASE or
#                        https://www.privycs.com/downloads)
#   --deb PATH           install a local .deb instead of downloading (apt only)
#   --deps-only          install dependencies but not the app
#   --no-deps            install the app but not the VPN dependencies
#
# Env overrides: PRIVYCS_DOWNLOAD_BASE, PRIVYCS_VERSION.
set -euo pipefail

# ---- config / args --------------------------------------------------------
DOWNLOAD_BASE="${PRIVYCS_DOWNLOAD_BASE:-https://www.privycs.com/downloads}"
VERSION="${PRIVYCS_VERSION:-}"
WITH_AWG=0
LOCAL_DEB=""
DO_DEPS=1
DO_APP=1

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --with-amneziawg) WITH_AWG=1 ;;
    --version) VERSION="${2:?}"; shift ;;
    --base)    DOWNLOAD_BASE="${2:?}"; shift ;;
    --deb)     LOCAL_DEB="${2:?}"; shift ;;
    --deps-only) DO_APP=0 ;;
    --no-deps)   DO_DEPS=0 ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
  shift
done

# ---- must be root ---------------------------------------------------------
if [ "$(id -u)" != "0" ]; then
  log "Re-running with sudo…"
  exec sudo -E bash "$0" \
    $([ "$WITH_AWG" = 1 ] && echo --with-amneziawg) \
    $([ -n "$VERSION" ] && echo --version "$VERSION") \
    --base "$DOWNLOAD_BASE" \
    $([ -n "$LOCAL_DEB" ] && echo --deb "$LOCAL_DEB") \
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

# ---- AmneziaWG userland (opt-in, source build) ----------------------------
install_amneziawg() {
  log "Installing AmneziaWG userland (awg-quick + amneziawg-go)…"
  case "$PM" in
    apt)    apt-get install -y git make gcc golang ;;
    dnf)    dnf install -y git make gcc golang ;;
    pacman) pacman -Sy --needed --noconfirm git make gcc go ;;
    zypper) zypper --non-interactive install -y git make gcc go ;;
  esac
  command -v go >/dev/null 2>&1 || { warn "Go toolchain not available — skipping AmneziaWG. Install 'go' and re-run with --with-amneziawg."; return 0; }

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
  VERSION="$(curl -fsSL "$DOWNLOAD_BASE/latest_version_linux.txt" 2>/dev/null | tr -d '[:space:]' || true)"
  [ -n "$VERSION" ] || die "could not determine latest version from $DOWNLOAD_BASE/latest_version_linux.txt — pass --version X.Y.Z.W"
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
    curl -fSL -o "$deb" "$DOWNLOAD_BASE/privycs-vpn-linux-amd64-$VERSION.deb" \
      || die "download failed from $DOWNLOAD_BASE"
  fi
  log "Installing the .deb (pulls any remaining recommends)…"
  apt-get install -y "$deb"
  [ -n "$LOCAL_DEB" ] || rm -f "$deb"
}

install_app_binary() {
  resolve_version
  log "Downloading standalone binary privycs-vpn-linux-amd64-$VERSION…"
  curl -fSL -o /usr/local/bin/privycs-vpn "$DOWNLOAD_BASE/privycs-vpn-linux-amd64-$VERSION" \
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
  if [ "$PM" = "apt" ]; then install_app_apt; else install_app_binary; fi
}

# ---- run ------------------------------------------------------------------
[ "$DO_DEPS" = 1 ] && install_deps
[ "$WITH_AWG" = 1 ] && install_amneziawg
[ "$DO_APP"  = 1 ] && install_app

echo
log "Done. Launch 'Privycs VPN' from your applications menu, or run: privycs-vpn"
if [ "$WITH_AWG" != 1 ]; then
  warn "AmneziaWG was NOT installed. WireGuard/OpenVPN/IPSec work now. For the"
  warn "DPI-resistant AmneziaWG protocol, re-run with --with-amneziawg (needs a"
  warn "Go toolchain) or follow the manual steps in the Desktop Client docs."
fi
