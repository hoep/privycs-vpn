# Contributing to Privycs VPN

Thanks for your interest in improving Privycs VPN — contributions are
welcome.

## Before you start

For anything beyond a small fix, **open an issue first** to discuss
the approach. It keeps both sides from writing code that doesn't fit
the roadmap.

For security vulnerabilities, do **not** open a public issue — follow
[SECURITY.md](SECURITY.md) instead.

## Building

- **Desktop** (Go + Wails + Vue 3) — see [`desktop/README.md`](desktop/README.md)
- **Android** (Kotlin + Jetpack Compose) — see the *Building from source*
  section in the main [`README.md`](README.md)

A quick sanity check before you push:

- Desktop: `cd desktop && go build ./...` — fast, catches compile errors
- Android: `cd android && ./gradlew assembleDebug`

## Pull requests

- Branch off `main`.
- Keep each PR focused on a single change.
- Match the style of the surrounding code — consistency beats personal
  preference.
- Write clear commit messages that explain *what* changed and *why*.
- Make sure the build passes before opening the PR.

## Reporting bugs

Open an issue using the bug-report template. Logs help a lot —
Settings → View Logs, with any secrets (keys, server names) redacted.

## License of contributions

Privycs VPN is licensed under the **GNU GPL-3.0** (see [LICENSE](LICENSE)).
By submitting a contribution, you agree that it is licensed under the
same terms.
