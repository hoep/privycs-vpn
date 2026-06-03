#!/usr/bin/env python3
"""Set the TestFlight "What to Test" (betaBuildLocalizations.whatsNew, en-US)
for the build we just uploaded, via the App Store Connect API.

Uses the SAME API key the workflow already uses for `altool --upload-app`
(APPLE_NOTARY_API_KEY_BASE64 / _KEY_ID / _ISSUER_ID) — no new secret.

Best-effort: any failure (still processing, app not found, API error) prints
and exits 0 so it never blocks a release.

Usage:
  set_testflight_notes.py <build_number> <bundle_id> <notes_file> <p8_key_file> <key_id> <issuer_id>
"""
import sys, time, json, urllib.request, urllib.error

try:
    import jwt  # PyJWT (with the [crypto] extra for ES256)
except Exception as e:  # pragma: no cover
    print(f"[testflight-notes] PyJWT unavailable ({e}) — skipping (non-fatal)")
    sys.exit(0)

API = "https://api.appstoreconnect.apple.com"


def main() -> int:
    build_number, bundle_id, notes_file, key_file, key_id, issuer_id = sys.argv[1:7]
    with open(key_file) as f:
        private_key = f.read()
    with open(notes_file) as f:
        whats_new = f.read().strip()
    if not whats_new:
        print("[testflight-notes] notes file empty — skipping")
        return 0
    # ASC caps whatsNew at 4000 chars.
    whats_new = whats_new[:4000]

    def token() -> str:
        now = int(time.time())
        return jwt.encode(
            {"iss": issuer_id, "iat": now, "exp": now + 1200, "aud": "appstoreconnect-v1"},
            private_key, algorithm="ES256", headers={"kid": key_id, "typ": "JWT"},
        )

    def api(method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(API + path, data=data, method=method)
        req.add_header("Authorization", "Bearer " + token())
        req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req) as r:
                return r.status, json.loads(r.read() or b"{}")
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read() or b"{}")

    # 1. Resolve the app id from the bundle id.
    st, data = api("GET", f"/v1/apps?filter[bundleId]={bundle_id}")
    apps = data.get("data", [])
    if not apps:
        print(f"[testflight-notes] app {bundle_id} not found (status {st}) — skipping")
        return 0
    app_id = apps[0]["id"]

    # 2. Poll until the just-uploaded build finishes processing (VALID).
    build_id = None
    for attempt in range(60):  # up to ~30 min at 30s
        st, data = api(
            "GET",
            f"/v1/builds?filter[app]={app_id}&filter[version]={build_number}&limit=1",
        )
        builds = data.get("data", [])
        if builds:
            b = builds[0]
            build_id = b["id"]
            state = b.get("attributes", {}).get("processingState")
            if state == "VALID":
                print(f"[testflight-notes] build {build_number} VALID")
                break
            print(f"[testflight-notes] build {build_number} processing: {state} ({attempt})")
        else:
            print(f"[testflight-notes] build {build_number} not visible yet ({attempt})")
        time.sleep(30)

    if not build_id:
        print(f"[testflight-notes] build {build_number} never appeared — skipping (non-fatal)")
        return 0

    # 3. Create or update the en-US beta build localization.
    st, data = api("GET", f"/v1/builds/{build_id}/betaBuildLocalizations")
    existing = {
        loc.get("attributes", {}).get("locale"): loc["id"]
        for loc in data.get("data", [])
    }
    if "en-US" in existing:
        loc_id = existing["en-US"]
        body = {"data": {"type": "betaBuildLocalizations", "id": loc_id,
                         "attributes": {"whatsNew": whats_new}}}
        st, data = api("PATCH", f"/v1/betaBuildLocalizations/{loc_id}", body)
    else:
        body = {"data": {"type": "betaBuildLocalizations",
                         "attributes": {"locale": "en-US", "whatsNew": whats_new},
                         "relationships": {"build": {"data": {"type": "builds", "id": build_id}}}}}
        st, data = api("POST", "/v1/betaBuildLocalizations", body)

    if st >= 400:
        print(f"[testflight-notes] set failed (status {st}): {json.dumps(data)[:500]} — non-fatal")
    else:
        print(f"[testflight-notes] What-to-Test set for build {build_number} (status {st})")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as e:  # never block a release on notes
        print(f"[testflight-notes] unexpected error: {e} — skipping (non-fatal)")
        sys.exit(0)
