#!/usr/bin/env bash
#
# deploy-web-clients.sh — publish the client-app docs (docs/*.md) AND the Linux
# installer + its version pointer to the live marketing site, INDEPENDENT of the
# privycs (gateway) repo.
#
# Why this works without a web rebuild:
#   The site renders each doc by fetching /docs/<file>.md at RUNTIME
#   (privycs/web/src/components/MarkdownContent.vue → `fetch('/docs/'+file)`).
#   So a content update only needs the .md replaced in the deployed docs dir —
#   no `npm run build`, no gateway-repo checkout, no touching the
#   refactor/gateway branch.
#
# SCOPE — what this DOES:
#   • Updates the CONTENT of already-published client docs (Android / Desktop /
#     iOS client guides + their privacy policies + the Connect guide). Their
#     routes already exist on the live site.
#   • Publishes the Linux installer (desktop/scripts/install-linux.sh) to
#     <downloads>/install-linux-client.sh and the Linux version pointer
#     (desktop/latest_version_linux.txt) that the installer reads to resolve
#     "latest". THIS repo is the single source of truth for both — publishing
#     them from here means the deployed copy can't drift from the repo (same
#     principle as the docs). The .deb / .dmg / .exe artefacts themselves are
#     mirrored from the GitHub release by the gateway-side deploy; we do NOT
#     touch those.
#
# SCOPE — what this does NOT do (still needs a full `privycs/deploy-web.sh`):
#   • Create a NEW doc/slug — that needs a `{slug,title,file}` entry in
#     privycs/web/src/docs.js plus a prerender build.
#   • Refresh the prerendered SEO HTML at /docs/<slug>/index.html — that
#     snapshot stays until the next full web build. The live SPA content (the
#     runtime-fetched .md) is fresh immediately, so users see the update; only
#     the no-JS/crawler snapshot lags.
#
# Never copies README.md: docs/README.md here is the client published-slug
# index, NOT a published page — the live /docs/README.md is the SERVER admin
# guide and must not be clobbered.
#
# Usage:  ./deploy-web-clients.sh        (needs sudo; the docs dir is www-data)

set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/docs" && pwd)"
DEST="/var/www/privycs/docs"

if [ ! -d "$DEST" ]; then
    echo "ERROR: $DEST not found — the marketing site isn't deployed on this host." >&2
    exit 1
fi

echo "Publishing client docs:"
echo "  from: $SRC"
echo "  to:   $DEST"
echo

count=0
for f in "$SRC"/*.md; do
    name="$(basename "$f")"
    [ "$name" = "README.md" ] && continue   # client index, not a published page
    sudo cp "$f" "$DEST/$name"
    sudo chown www-data:www-data "$DEST/$name"
    sudo chmod 644 "$DEST/$name"
    echo "  ✓ $name"
    count=$((count + 1))
done

echo
echo "Done — $count client doc(s) updated. The site fetches /docs/*.md at"
echo "runtime, so the live content is already current."
echo "Note: prerendered SEO HTML refreshes on the next full privycs/deploy-web.sh."

# ---------------------------------------------------------------------------
# Linux installer + version pointer → the public downloads area.
#
# The installer resolves "latest" by fetching <base>/latest_version_linux.txt,
# then downloads <base>/privycs-vpn-linux-amd64-<ver>.deb. Both the script and
# the version file live in THIS repo, so publishing them from here keeps the
# deployed copies from drifting. The binaries themselves are mirrored from the
# GitHub release by the gateway-side deploy — untouched here.
#
# Deployed name is install-linux-client.sh (that's the URL the docs + the
# script's own header advertise); the repo file is desktop/scripts/install-linux.sh.
# ---------------------------------------------------------------------------
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DL_DEST="/var/www/privycs/downloads"
INSTALLER_SRC="$REPO_ROOT/desktop/scripts/install-linux.sh"
VERSION_SRC="$REPO_ROOT/desktop/latest_version_linux.txt"

echo
if [ ! -d "$DL_DEST" ]; then
    echo "Skipping downloads publish — $DL_DEST not found on this host."
elif [ ! -f "$INSTALLER_SRC" ]; then
    echo "Skipping downloads publish — $INSTALLER_SRC missing."
else
    echo "Publishing Linux installer + version pointer:"
    echo "  to: $DL_DEST"

    sudo cp "$INSTALLER_SRC" "$DL_DEST/install-linux-client.sh"
    sudo chown www-data:www-data "$DL_DEST/install-linux-client.sh"
    sudo chmod 755 "$DL_DEST/install-linux-client.sh"
    echo "  ✓ install-linux-client.sh"

    # The version pointer is a CONTRACT: the installer reads it, then fetches
    # privycs-vpn-linux-amd64-<ver>.deb. Advertising a version whose artefacts
    # aren't there yet gives the user a hard 404 — which is exactly what happened
    # when this script bumped the pointer (from the repo, at release time) while
    # the binaries were still being mirrored by the slower gateway-side job.
    # So: make sure the artefacts for THIS version exist — mirroring them from
    # the GitHub release if needed — and only then advance the pointer.
    VER="$(tr -d '[:space:]' < "$VERSION_SRC" 2>/dev/null || true)"
    if [ -z "$VER" ]; then
        echo "  !! $VERSION_SRC unreadable — pointer not advanced."
    else
        if [ ! -f "$DL_DEST/privycs-vpn-linux-amd64-$VER.deb" ]; then
            echo "  … Linux artefacts for $VER not present — mirroring from release v$VER"
            tmp="$(mktemp -d)"
            if gh release download "v$VER" --repo hoep/privycs-vpn --dir "$tmp" \
                   --pattern "privycs-vpn-linux-amd64-*" >/dev/null 2>&1; then
                for f in "$tmp"/*; do
                    b="$(basename "$f")"
                    sudo cp "$f" "$DL_DEST/$b"
                    sudo chown www-data:www-data "$DL_DEST/$b"
                    sudo chmod 644 "$DL_DEST/$b"
                    echo "  ✓ $b"
                done
            fi
            rm -rf "$tmp"
        fi

        if [ -f "$DL_DEST/privycs-vpn-linux-amd64-$VER.deb" ]; then
            sudo cp "$VERSION_SRC" "$DL_DEST/latest_version_linux.txt"
            sudo chown www-data:www-data "$DL_DEST/latest_version_linux.txt"
            sudo chmod 644 "$DL_DEST/latest_version_linux.txt"
            echo "  ✓ latest_version_linux.txt ($VER)"
            echo
            echo "The one-line installer is live:"
            echo "  curl -fsSL -u 'dl:TOKEN' https://www.privycs.com/downloads/install-linux-client.sh | sudo bash -s -- --token TOKEN"
        else
            echo "  !! privycs-vpn-linux-amd64-$VER.deb is NOT in $DL_DEST and could not be"
            echo "     mirrored (release v$VER not published yet?). Pointer left at"
            echo "     $(cat "$DL_DEST/latest_version_linux.txt" 2>/dev/null || echo '<none>') so the installer keeps working."
        fi
    fi
fi
