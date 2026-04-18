#!/usr/bin/env bash
# Prepares the strongSwan submodule for ndk-build.
#
#   1. Runs autogen.sh + configure to generate Android.common.mk and other
#      autotools artifacts referenced by the Android.mk includes.
#   2. Runs `make` + `make distclean` because strongSwan's build emits a
#      handful of generated .c sources (keyword maps, oid tables) that
#      ndk-build then consumes; distclean removes host-platform .o files
#      so cross-compilation starts fresh.
#   3. Downloads the pinned OpenSSL source tree (cached across runs).
#   4. Invokes strongSwan's openssl/build.sh to produce libcrypto_static.a
#      for each ABI, plus the shared include/ tree.
#
# Required environment:
#   ANDROID_NDK_ROOT - absolute path to the Android NDK.
#
# Optional environment:
#   OPENSSL_VERSION   - OpenSSL tag to build (default below).
#   OPENSSL_CACHE_DIR - where to cache extracted OpenSSL source tree.
#   ABIS              - space-separated ABI list (default: all four).
#
# Idempotent: re-running skips work that has already completed.

set -euo pipefail

# ----------------------------------------------------------------------------
# Defaults and paths
# ----------------------------------------------------------------------------

OPENSSL_VERSION="${OPENSSL_VERSION:-3.5.6}"
OPENSSL_SHA256="${OPENSSL_SHA256:-deae7c80cba99c4b4f940ecadb3c3338b13cb77418409238e57d7f31f2a3b736}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ANDROID_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
STRONGSWAN_DIR="${ANDROID_DIR}/vendor/strongswan"
ANDROID_FRONTEND="${STRONGSWAN_DIR}/src/frontends/android"
JNI_DIR="${ANDROID_FRONTEND}/app/src/main/jni"
OPENSSL_OUT="${JNI_DIR}/openssl"

OPENSSL_CACHE_DIR="${OPENSSL_CACHE_DIR:-${ANDROID_DIR}/.build-cache/openssl}"
OPENSSL_SRC="${OPENSSL_CACHE_DIR}/openssl-${OPENSSL_VERSION}"
OPENSSL_TARBALL="${OPENSSL_CACHE_DIR}/openssl-${OPENSSL_VERSION}.tar.gz"
OPENSSL_URL="https://github.com/openssl/openssl/releases/download/openssl-${OPENSSL_VERSION}/openssl-${OPENSSL_VERSION}.tar.gz"

export ABIS="${ABIS:-arm64-v8a armeabi-v7a x86_64 x86}"

log() { printf '[prepare-strongswan] %s\n' "$*"; }
die() { printf '[prepare-strongswan] FATAL: %s\n' "$*" >&2; exit 1; }

# ----------------------------------------------------------------------------
# Preconditions
# ----------------------------------------------------------------------------

[ -n "${ANDROID_NDK_ROOT:-}" ] || die "ANDROID_NDK_ROOT is not set"
[ -d "${ANDROID_NDK_ROOT}" ]   || die "ANDROID_NDK_ROOT is not a directory: ${ANDROID_NDK_ROOT}"
[ -d "${STRONGSWAN_DIR}" ]     || die "strongSwan submodule missing. Run: git submodule update --init --recursive"

for tool in git make perl python3 curl tar sha256sum autoconf automake libtool pkg-config flex bison gperf; do
  command -v "${tool}" >/dev/null 2>&1 || die "required tool missing: ${tool}"
done

log "ANDROID_NDK_ROOT=${ANDROID_NDK_ROOT}"
log "strongSwan     =${STRONGSWAN_DIR}"
log "OpenSSL target =${OPENSSL_VERSION}"
log "ABIs           =${ABIS}"

# ----------------------------------------------------------------------------
# Stage 1: autogen + configure + make (generates Android.common.mk etc.)
# ----------------------------------------------------------------------------

# Always run autogen/configure/make. The autotools step emits a set of
# generated .c files (proposal_keywords_static.c, oid.c, several *_keywords.c)
# under src/**/ which are hard to enumerate and therefore hard to cache
# reliably; a partial snapshot of the tree leads to "No rule to make target"
# failures during ndk-build. Regenerating unconditionally takes ~60s and keeps
# the state consistent.
log "Running autogen.sh + configure + make inside submodule"
(
  cd "${STRONGSWAN_DIR}"
  ./autogen.sh
  ./configure \
    --disable-defaults \
    --enable-monolithic \
    --enable-ikev1 \
    --enable-ikev2 \
    --enable-openssl \
    --enable-pem \
    --enable-pkcs1 \
    --enable-pkcs12 \
    --enable-x509 \
    --enable-kernel-netlink \
    --enable-socket-default \
    --enable-nonce \
    --enable-xcbc \
    --enable-kdf \
    --enable-revocation \
    --enable-eap-identity \
    --enable-eap-mschapv2 \
    --enable-eap-md5 \
    --enable-eap-gtc \
    --enable-eap-tls \
    --disable-scripts \
    --disable-tools
  make -j"$(nproc 2>/dev/null || echo 2)"
  # Intentionally do NOT run `make distclean` here: it would scrub the
  # generated .c files (proposal_keywords_static.c, oid.c, etc.) that
  # ndk-build's Android.mk depends on. distclean is only appropriate when
  # you are re-invoking configure for a different target; since ndk-build
  # consumes the .c files directly and has its own build dir, leaving the
  # host .o files around is harmless and the generated .c files are what
  # we actually need.
)

# ----------------------------------------------------------------------------
# Stage 2: fetch OpenSSL source (cached)
# ----------------------------------------------------------------------------

mkdir -p "${OPENSSL_CACHE_DIR}"

if [ ! -f "${OPENSSL_TARBALL}" ]; then
  log "Downloading OpenSSL ${OPENSSL_VERSION}"
  curl -fsSL -o "${OPENSSL_TARBALL}.tmp" "${OPENSSL_URL}"
  mv "${OPENSSL_TARBALL}.tmp" "${OPENSSL_TARBALL}"
fi

log "Verifying OpenSSL tarball sha256"
echo "${OPENSSL_SHA256}  ${OPENSSL_TARBALL}" | sha256sum -c - >/dev/null

if [ ! -d "${OPENSSL_SRC}" ]; then
  log "Extracting OpenSSL source"
  tar -xf "${OPENSSL_TARBALL}" -C "${OPENSSL_CACHE_DIR}"
fi

# ----------------------------------------------------------------------------
# Stage 3: build libcrypto_static for each ABI
# ----------------------------------------------------------------------------

MISSING_ABIS=""
for abi in ${ABIS}; do
  if [ ! -f "${OPENSSL_OUT}/${abi}/libcrypto.a" ]; then
    MISSING_ABIS="${MISSING_ABIS} ${abi}"
  fi
done

if [ -n "${MISSING_ABIS# }" ] || [ "${FORCE_OPENSSL:-0}" = 1 ]; then
  log "Building libcrypto_static for ABIs:${MISSING_ABIS:-(all, forced)}"
  ANDROID_NDK_ROOT="${ANDROID_NDK_ROOT}" \
  OPENSSL_SRC="${OPENSSL_SRC}" \
  NO_DOCKER=1 \
  ABIS="${ABIS}" \
  bash "${ANDROID_FRONTEND}/openssl/build.sh"
else
  log "libcrypto.a present for all requested ABIs; skipping OpenSSL build (set FORCE_OPENSSL=1 to rerun)"
fi

log "Prepared strongSwan for ndk-build."
log "  generated: ${STRONGSWAN_DIR}/Android.common.mk"
log "  libcrypto: ${OPENSSL_OUT}/<abi>/libcrypto.a"
log "  headers:   ${OPENSSL_OUT}/include/"
