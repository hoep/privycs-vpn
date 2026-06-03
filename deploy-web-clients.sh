#!/usr/bin/env bash
#
# deploy-web-clients.sh — publish the client-app docs (docs/*.md) to the live
# marketing site, INDEPENDENT of the privycs (gateway) repo.
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
