#!/usr/bin/env bash
# Prepares the amneziawg-android submodule to coexist with the
# wireguard-android Maven artifact in a single APK.
#
# Problem: both libraries ship native libs with identical filenames
# (libwg-go.so, libwg.so, libwg-quick.so) into jniLibs/<abi>/. Both
# Java GoBackends call System.loadLibrary("wg-go") to dlopen the
# .so. At APK merge time AGP errors with "Duplicate JNI library"
# OR silently picks one — either way the AmneziaWG backend wouldn't
# be able to load its own binary.
#
# Solution: rename AmneziaWG's native artefacts to `*-awg.so` and
# patch its Java loader to call System.loadLibrary("wg-go-awg")
# etc. WireGuard's `wg-go.so` keeps its name; AWG's `wg-go-awg.so`
# coexists in the same jniLibs/<abi>/ folder.
#
# Files patched (all under android/vendor/amneziawg-android/tunnel):
#   - build.gradle.kts                                     (CMake targets list)
#   - tools/CMakeLists.txt                                 (add_executable / add_custom_target names)
#   - tools/libwg-go/Makefile                              (output filename + -soname)
#   - src/main/java/org/amnezia/awg/backend/GoBackend.java (loadSharedLibrary arg)
#
# Idempotent — re-running skips files that already have the renamed
# symbols (grep-test).
#
# Must run BEFORE the first `./gradlew build` after a fresh
# submodule init, and after every `git submodule update` that pulls
# new upstream commits which might re-introduce the names.
#
# Pattern stolen from android/scripts/prepare-strongswan.sh.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AWG_DIR="${REPO_ROOT}/vendor/amneziawg-android/tunnel"

if [ ! -d "${AWG_DIR}" ]; then
    echo "ERROR: ${AWG_DIR} not found — run 'git submodule update --init --recursive' first" >&2
    exit 1
fi

# Per-file patch helpers ----------------------------------------------------

patch_gradle() {
    local f="${AWG_DIR}/build.gradle.kts"
    if grep -q '"libwg-go-awg.so"' "$f"; then
        echo "  build.gradle.kts: already patched"
        return
    fi
    sed -i 's|"libwg-go.so", "libwg.so", "libwg-quick.so"|"libwg-go-awg.so", "libwg-awg.so", "libwg-quick-awg.so"|' "$f"
    echo "  build.gradle.kts: patched (cmake targets)"
}

patch_cmake() {
    local f="${AWG_DIR}/tools/CMakeLists.txt"
    if grep -q 'libwg-go-awg.so' "$f"; then
        echo "  CMakeLists.txt: already patched"
        return
    fi
    sed -i \
        -e 's|libwg-quick\.so|libwg-quick-awg.so|g' \
        -e 's|libwg-go\.so|libwg-go-awg.so|g' \
        -e 's|libwg\.so|libwg-awg.so|g' \
        "$f"
    echo "  CMakeLists.txt: patched (target names)"
}

patch_libwg_go_makefile() {
    local f="${AWG_DIR}/tools/libwg-go/Makefile"
    if grep -q 'libwg-go-awg.so' "$f"; then
        echo "  libwg-go/Makefile: already patched"
        return
    fi
    sed -i 's|libwg-go\.so|libwg-go-awg.so|g' "$f"
    echo "  libwg-go/Makefile: patched (output filename + soname)"
}

patch_gobackend_loader() {
    local f="${AWG_DIR}/src/main/java/org/amnezia/awg/backend/GoBackend.java"
    if grep -q '"wg-go-awg"' "$f"; then
        echo "  GoBackend.java: already patched"
        return
    fi
    sed -i 's|loadSharedLibrary(context, "wg-go")|loadSharedLibrary(context, "wg-go-awg")|' "$f"
    echo "  GoBackend.java: patched (System.loadLibrary call)"
}

# Apply ---------------------------------------------------------------------

echo "Patching amneziawg-android tunnel module for coexistence with"
echo "wireguard-android in the same APK (rename libwg-* to libwg-*-awg):"

patch_gradle
patch_cmake
patch_libwg_go_makefile
patch_gobackend_loader

echo "Done. Vanilla WG and AmneziaWG can now coexist in the merged APK."
