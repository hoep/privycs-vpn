#!/usr/bin/env bash
# Applies the Privycs vendor patches to the strongSwan submodule.
#
# Patches under android/vendor/strongswan-patches/*.patch carry Privycs-only
# changes that we cannot push upstream (e.g. RFC 8784 PPK plumbing through
# the Android frontend's Java + JNI layers). Because the submodule remote
# points at upstream strongSwan we cannot commit into it; instead we ship
# the patches as tracked files in the parent repo and re-apply them on
# every build.
#
# git is the ONLY requirement. Deliberately kept free of the NDK/autotools
# preconditions that prepare-strongswan.sh needs: Gradle calls this on every
# Java compile so that a plain `./gradlew :app:assembleDebug` on a developer
# box cannot produce an unpatched build. An unpatched CharonVpnService still
# COMPILES — it just silently falls back to upstream behaviour — so nothing
# downstream would catch the omission.
#
# Idempotent: already-applied patches are skipped, so re-running is a no-op.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANDROID_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
STRONGSWAN_DIR="${ANDROID_DIR}/vendor/strongswan"
PATCH_DIR="${ANDROID_DIR}/vendor/strongswan-patches"

log() { printf '[apply-strongswan-patches] %s\n' "$*"; }
die() { printf '[apply-strongswan-patches] FATAL: %s\n' "$*" >&2; exit 1; }

command -v git >/dev/null 2>&1 || die "required tool missing: git"

[ -d "${STRONGSWAN_DIR}/.git" ] || [ -f "${STRONGSWAN_DIR}/.git" ] || \
  die "strongSwan submodule missing or not a git checkout. Run: git submodule update --init --recursive"

if [ ! -d "${PATCH_DIR}" ]; then
  log "no patch directory at ${PATCH_DIR}; nothing to do"
  exit 0
fi

shopt -s nullglob
patches=( "${PATCH_DIR}"/*.patch )
shopt -u nullglob

if [ ${#patches[@]} -eq 0 ]; then
  log "no patches in ${PATCH_DIR}; nothing to do"
  exit 0
fi

log "Applying ${#patches[@]} vendor patch(es) from ${PATCH_DIR}"
for p in "${patches[@]}"; do
  name="$(basename "${p}")"
  # Order matters: --check first. A patch that applies cleanly is applied;
  # one that reverse-applies cleanly is already in the tree. Any other
  # outcome means the patch has rotted against an upstream bump and a human
  # must refresh it — never silently continue, the build would be missing
  # the change with no other symptom.
  if (cd "${STRONGSWAN_DIR}" && git apply --check "${p}" >/dev/null 2>&1); then
    log "  apply  ${name}"
    (cd "${STRONGSWAN_DIR}" && git apply "${p}")
  elif (cd "${STRONGSWAN_DIR}" && git apply --reverse --check "${p}" >/dev/null 2>&1); then
    log "  skip   ${name} (already applied)"
  else
    die "patch ${name} does not apply and is not already applied — refresh it (see android/vendor/strongswan-patches/README.md)"
  fi
done

log "strongSwan submodule carries all Privycs vendor patches."
