# strongSwan vendor patches

The Android client embeds strongSwan as a git submodule under
`android/vendor/strongswan/`. The submodule's `origin` points at upstream
strongSwan, so we cannot commit changes into it. Instead this directory
holds Privycs-only patches as tracked diff files; the build script
applies them every time.

## How the build picks them up

`android/scripts/prepare-strongswan.sh` runs as Stage 0 of the Android
CI workflow (right after `actions/checkout` with `submodules: recursive`).
For every `*.patch` file in this directory it tries:

1. `git apply --check <patch>` — applies cleanly → `git apply <patch>`
2. `git apply --reverse --check <patch>` — already applied → skip
3. Anything else → abort the build with a clear error

This makes the script idempotent: re-running on a partially-applied
tree keeps things consistent, and a fresh `git submodule update` followed
by `prepare-strongswan.sh` always produces the same patched tree.

## Local development loop

Working clone of the repo includes the submodule already. To iterate on
a vendor change:

```bash
# 1. Run the prep script once so the submodule WT carries existing patches:
./android/scripts/prepare-strongswan.sh

# 2. Edit files inside android/vendor/strongswan/...

# 3. Build the APK — Gradle runs prep-strongswan automatically, but if
#    the WT already has your edits the existing patch will skip ("already
#    applied") and your fresh edits stay in place.

# 4. When you're ready to commit, regenerate the patch from the live WT:
cd android/vendor/strongswan
git diff > ../strongswan-patches/0001-privycs-rfc8784-ppk.patch
# (or the next free NNNN- prefix for an unrelated topic)

# 5. Commit ONLY the patch file in the parent repo. Submodule WT changes
#    are NOT recorded by `git commit` — they would be lost on
#    `git submodule update` / fresh clone anyway.
```

After a `git submodule update --init` (e.g. fresh clone) the submodule
WT is reset to upstream and your patches are gone until prep-strongswan
runs again.

## Patches

### `0001-privycs-rfc8784-ppk.patch`

Adds RFC 8784 Postquantum Preshared Key (PPK) plumbing through the
Java/JNI/native layers so Privycs `pq_safe` interfaces can drive the
PPK_IDENTITY / PPK mixin during IKE_AUTH.

Touches 8 files:

**Java (Android frontend)**
- `data/VpnProfileDataSource.java` — `KEY_PPK_ID` / `KEY_PPK_PSK` constants
- `data/DatabaseHelper.java` — DB schema bumped 19 → 20, two new columns
  with `since=20` so `addNewColumns()` ALTERs existing DBs on upgrade
- `data/VpnProfile.java` — `mPPKId` / `mPPKPsk` fields with getters/setters
- `data/VpnProfileSqlDataSource.java` — read/write the new columns;
  reader uses `getColumnIndex` (no `OrThrow`) to tolerate cursors that
  predate the migration
- `logic/CharonVpnService.java` — propagates `connection.ppk_id` and
  `connection.ppk_psk` through the SettingsWriter blob to the JNI side

**JNI / Native (libandroidbridge)**
- `backend/android_creds.h` / `android_creds.c` — new `add_ppk(id, psk_hex)`
  method that decodes hex, registers a `SHARED_PPK` shared_key keyed by id
- `backend/android_service.c` — reads the two new settings keys before
  `peer_cfg_create()`, calls `add_ppk()`, sets `peer.ppk_id` and
  `OPT_PPK_REQUIRED`

The matching client-side wiring lives in
`android/app/src/main/java/com/privycs/vpn/service/IpSecTunnel.kt`
(SswanLocal fields + buildVpnProfile setters) and the gateway emits the
`ppk_id` / `ppk_psk` fields in the `.sswan` JSON when the source
interface has `pq_safe = true`.

## Refreshing after an upstream strongSwan bump

When the submodule pointer is bumped, existing patches may stop applying
because upstream re-formatted the file around our hunk anchors.

```bash
cd android/vendor/strongswan
git checkout <new-strongswan-tag>

# Try the prep script first — it will refuse with a clear message if
# any patch has rotted:
../../scripts/prepare-strongswan.sh
```

If a patch fails, recover the change by hand:

```bash
cd android/vendor/strongswan
git apply --reject ../strongswan-patches/0001-privycs-rfc8784-ppk.patch
# Resolve the *.rej files manually, then regenerate:
git diff > ../strongswan-patches/0001-privycs-rfc8784-ppk.patch
# Sanity-check the regenerated patch round-trips:
git checkout .
git apply --check ../strongswan-patches/0001-privycs-rfc8784-ppk.patch
```

Commit the regenerated patch in the parent repo.

## Adding a new patch

Pick the next free `NNNN-` prefix and capture only the new logical
change:

```bash
# After making the new edits inside the submodule:
cd android/vendor/strongswan
git diff > ../strongswan-patches/0002-privycs-<topic>.patch
```

Avoid mega-patches that mix unrelated work — small focused patches are
easier to re-apply and easier to drop if upstream eventually
implements the feature itself.
