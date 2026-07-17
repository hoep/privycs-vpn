# strongSwan vendor patches

The Android client embeds strongSwan as a git submodule under
`android/vendor/strongswan/`. The submodule's `origin` points at upstream
strongSwan, so we cannot commit changes into it. Instead this directory
holds Privycs-only patches as tracked diff files; the build script
applies them every time.

## How the build picks them up

`android/scripts/apply-strongswan-patches.sh` is the single place that
applies them. For every `*.patch` file in this directory it tries:

1. `git apply --check <patch>` — applies cleanly → `git apply <patch>`
2. `git apply --reverse --check <patch>` — already applied → skip
3. Anything else → abort with a clear error

That makes it idempotent: re-running on an already-patched tree is a
no-op, and a fresh `git submodule update` followed by the script always
produces the same patched tree.

Two callers, both mandatory in their context:

- **Gradle** — `:strongswan-lib:applyStrongswanPatches` (an `Exec` task in
  `android/strongswan-lib/build.gradle.kts`). `preBuild`, `syncStrongswanJava`
  and `syncStrongswanRes` all depend on it, so any `./gradlew` build — down to
  a plain `:app:assembleDebug` — patches the submodule first. It needs nothing
  but `git`.
- **CI** — `android/scripts/prepare-strongswan.sh` calls it as Stage 0 before
  the autotools/OpenSSL/ndk-build stages. That script additionally requires
  `ANDROID_NDK_ROOT` plus autoconf/automake/libtool/flex/bison/gperf, which is
  exactly why patch application does not live inside it.

**Why Gradle has to do this at all:** an unpatched `CharonVpnService`
still compiles. Nothing downstream errors — the app just silently falls
back to upstream behaviour at runtime. The Gradle dependency is the only
thing that makes an unpatched build impossible.

## Local development loop

A working clone includes the submodule already. To iterate on a vendor
change:

```bash
# 1. Apply the existing patches to the submodule WT (git-only, no NDK):
bash android/scripts/apply-strongswan-patches.sh

# 2. Edit files inside android/vendor/strongswan/...

# 3. Build the APK. Gradle re-runs apply-strongswan-patches.sh, which sees
#    the existing patches as "already applied" and skips them, so your
#    uncommitted edits on top stay in place.
cd android && ./gradlew :app:assembleDebug

# 4. When you're ready to commit, regenerate the patch from the live WT.
#    Valid ONLY when you are extending the single existing patch: the diff
#    spans everything in the WT, which is 0001 + your edits.
#    Starting a SEPARATE patch instead? See "Adding a new patch" below —
#    a bare `git diff` would swallow 0001's hunks into it.
cd android/vendor/strongswan
git diff > ../strongswan-patches/0001-privycs-rfc8784-ppk.patch

# 5. Commit ONLY the patch file in the parent repo. Submodule WT changes
#    are NOT recorded by `git commit` — they would be lost on
#    `git submodule update` / fresh clone anyway.
```

After a `git submodule update --init` (e.g. fresh clone) the submodule WT
is reset to upstream and your patches are gone until the script runs
again — which the next Gradle build does for you.

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

### `0002-privycs-inmemory-ipsec-identity.patch`

Lets charon use a client identity parsed in-process from the PKCS#12
embedded in a `.sswan`, instead of one installed into the Android
KeyChain. Motivation: Android exposes no API to *remove* an installed
KeyChain credential, so the app could create one but never clean it up —
uninstalling the app leaves it behind for the user to delete by hand.

This works because strongSwan's native layer treats the private key as an
opaque signing oracle: `android_private_key.c` only ever calls
`Signature.initSign(key)` and its `get_encoding` is hard-wired to return
FALSE. Any `PrivateKey` that `initSign` accepts therefore satisfies the
contract — no hardware backing or KeyChain provenance is required.

Touches 1 file, 4 hunks, all inside existing method bodies or the field
block (minimal anchor surface, so upstream churn is unlikely to rot it):

- `logic/CharonVpnService.java`
  - `mCurrentPrivycsCredentials` field, plus a `PRIVYCS_PATCH_MARKER`
    constant the app detects reflectively to prove the patch is present
  - snapshots `PrivycsCredentials.get(profile.getUUID())` next to the
    existing cert-alias snapshot, for the same documented reason
    (avoiding a deinit deadlock on charon's thread)
  - `getUserCertificate()` returns the leaf DER when an in-memory entry
    exists — **leaf only**, because `android_creds.c` registers every
    further element of that array as a TRUSTED cert, which would widen
    trust beyond the profile's own `remote.cert_chain`
  - `getUserKey()` returns the in-memory key

Both getters fall through to the unmodified KeyChain path when no entry
is present: a hand-imported profile with no embedded PKCS#12 is a
legitimate upstream shape and keeps working.

The identity itself is held by `PrivycsCredentials`, which despite its
package is **Privycs-owned and not in the submodule** — it lives at
`android/strongswan-lib/src/main/java/org/strongswan/android/logic/`.
`build.gradle.kts` lists that tree alongside the Synced submodule
sources, so it compiles into the same package and the patch can call it.
Keeping the state, lifecycle and PKCS#12 parsing there is what holds this
patch to four hunks. Entries are process-lifetime only and must never
reach `strongswan.db` or the `privycs_ipsec` prefs, which are plaintext.

Deliberately adds **no** `VpnProfile` column and does not touch
`DATABASE_VERSION`. Patch 0001 already squats version 20 while upstream is
at 19, and the migration gate is `column.Since > oldVersion`, so a user
already on our 20 would never receive upstream's future `since=20`
columns. An in-memory holder needs no schema, sidestepping that
pre-existing bug rather than deepening it.

## Refreshing after an upstream strongSwan bump

When the submodule pointer is bumped, existing patches may stop applying
because upstream re-formatted the file around our hunk anchors.

```bash
cd android/vendor/strongswan
git checkout <new-strongswan-tag>

# Try the apply script first — it will refuse with a clear message if
# any patch has rotted (git-only, so no NDK needed to find out):
bash ../../scripts/apply-strongswan-patches.sh
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

Pick the next free `NNNN-` prefix and capture ONLY the new logical
change.

The trap: patches stack. By the time you start editing, the WT already
carries 0001, so a bare `git diff` against the submodule's HEAD (upstream
6.0.5) emits **0001's hunks plus yours**. Committing that as 0002 makes
0001 and 0002 both fail `--check` and `--reverse --check` on a fresh
tree — the apply script would then abort every build with "does not
apply and is not already applied".

Diff against the patched-but-unedited tree instead, by stashing your
edits and using the stash as the baseline:

```bash
cd android/vendor/strongswan

# 0. Start from a tree that has the existing patches and nothing else:
bash ../../scripts/apply-strongswan-patches.sh

# 1. Make your edits, then snapshot ONLY them:
git stash push --keep-index --include-untracked -m "wip-0002"
#    WT is now 0001-applied only. Re-apply your edits on top and diff:
git stash show -p stash@{0} > ../strongswan-patches/0002-privycs-<topic>.patch
git stash pop
```

Then verify the new patch stacks cleanly from scratch — this is the
check that catches a contaminated diff:

```bash
cd android/vendor/strongswan
git checkout .                                   # back to pristine upstream
bash ../../scripts/apply-strongswan-patches.sh   # must apply 0001 AND 0002
bash ../../scripts/apply-strongswan-patches.sh   # must skip both, exit 0
```

Both runs must exit 0, the first reporting `apply` for each patch and the
second `skip ... (already applied)`.

Avoid mega-patches that mix unrelated work — small focused patches are
easier to re-apply and easier to drop if upstream eventually
implements the feature itself.

## Never bump DATABASE_VERSION for a Privycs column

`DatabaseHelper.DATABASE_VERSION` belongs to upstream. Raise it only in step with
them, never to carry an addition of ours.

The counter is shared and `getAlterTables()` gates on `column.Since > oldVersion`.
Patch 0001 used to set it to 20 while upstream sat at 19; once a user's database
reached OUR 20, upstream's own future `Since = 20` columns would be skipped
forever — `20 > 20` is false — and the app would die with "no such column". No
other number helps: whatever we occupy swallows the identically-numbered upstream
migration. And the counter cannot simply be lowered again, because
SQLiteOpenHelper's default `onDowngrade` throws.

So 0001 now (a) declares its columns at upstream's current version, (b) accepts a
newer stamp in `onDowngrade` and lets SQLiteOpenHelper restamp it back, and (c)
adds `healMissingColumns()`, which adds any declared column the table lacks —
asking the table instead of the counter, on every open. Add future Privycs columns
the same way: declare them at upstream's current version and let the heal carry
them. Pinned by `app/src/test/java/com/privycs/vpn/data/StrongswanDbMigrationTest.kt`
(Robolectric, real SQLite).
