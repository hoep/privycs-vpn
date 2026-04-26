#!/bin/bash
# Fetches the GeoLite2 Country MMDB used by the Pool feature.
# Run before `wails build` or `wails dev`. Idempotent.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# The MMDB sits inside the geoip package directory so go:embed can
# pull it into the production binary at build time. Local-dev or
# air-gapped operators can override at runtime via PRIVYCS_GEOIP_DB.
ASSET_DIR="$SCRIPT_DIR/../geoip"
DB_PATH="$ASSET_DIR/Country.mmdb"
SOURCE_URL="https://github.com/sapics/ip-location-db/raw/main/geolite2-country-mmdb/geolite2-country-ipv4.mmdb"

mkdir -p "$ASSET_DIR"

if [ -f "$DB_PATH" ]; then
    AGE_DAYS=$(( ( $(date +%s) - $(stat -c %Y "$DB_PATH" 2>/dev/null || stat -f %m "$DB_PATH") ) / 86400 ))
    if [ "$AGE_DAYS" -lt 7 ]; then
        echo "GeoIP DB is $AGE_DAYS days old - skipping fetch"
        exit 0
    fi
fi

echo "Fetching GeoLite2 Country MMDB..."
TMP_FILE=$(mktemp)
trap 'rm -f "$TMP_FILE"' EXIT

if ! curl -fL --max-time 60 -o "$TMP_FILE" "$SOURCE_URL"; then
    echo "ERROR: failed to fetch $SOURCE_URL"
    if [ -f "$DB_PATH" ]; then
        echo "Keeping existing DB at $DB_PATH"
        exit 0
    fi
    exit 1
fi

if [ ! -s "$TMP_FILE" ]; then
    echo "ERROR: downloaded file is empty"
    exit 1
fi

mv "$TMP_FILE" "$DB_PATH"
SIZE=$(du -h "$DB_PATH" | cut -f1)
echo "Saved $DB_PATH ($SIZE)"
