# Client-facing documentation (canonical source)

This directory holds the **canonical, source-of-truth** Markdown for the
client-app documentation that is published on the public website at
`https://www.privycs.com/docs/<slug>`.

These docs describe the **client applications** that live in this repository
(Desktop / Android / iOS), so they are versioned alongside the apps they
document rather than in the gateway/server repository.

## Files

| File                         | Published slug              | Covers                                  |
|------------------------------|-----------------------------|-----------------------------------------|
| `connect-guide.md`           | `/docs/connect-guide`       | Privycs Connect onboarding              |
| `desktop-client.md`          | `/docs/desktop-client`      | Desktop VPN client (Windows/macOS/Linux)|
| `android-client.md`          | `/docs/android-client`      | Android VPN client                      |
| `desktop-client-privacy.md`  | `/docs/desktop-client-privacy` | Desktop app GDPR privacy policy      |
| `android-client-privacy.md`  | `/docs/android-client-privacy` | Android app GDPR privacy policy      |

## How these reach the website

The marketing website lives in the **`privycs`** repository under `web/`. Its
build does **not** keep its own copy of these files — it pulls them from here at
build time:

1. `web/scripts/sync-client-docs.sh` (run automatically as the `prebuild`/`predev`
   npm hook and explicitly by `deploy-web.sh`) copies every `*.md` in this
   directory (except this `README.md`) into `privycs/web/public/docs/`.
2. `vite-ssg build` bundles them into `web/dist/docs/`, which `deploy-web.sh`
   publishes to nginx.

Those synced copies are **git-ignored** in the `privycs` repo, so this directory
is the only editable copy. Edit the docs **here**.

## Adding a new client doc

1. Add the `*.md` file to this directory.
2. Register its `{ slug, title, file }` entry in `privycs/web/src/docs.js` so the
   site navigation and static-site routes pick it up.

The sync step copies all `*.md` here automatically, so no script change is needed.
