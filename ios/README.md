# Privycs VPN — iOS Client

iOS port of the Privycs VPN client with 100% feature parity to the Android version. Swift + SwiftUI + Network Extension.

Lives in the **monorepo** [`privycs-vpn`](https://github.com/hoep/privycs-vpn) alongside `desktop/` + `android/`. Per-platform CI workflows are path-gated.

## Status

**Feature-complete, in TestFlight.** iOS, iPadOS, macOS and tvOS are built
per tag from `ios-release.yml` and uploaded to TestFlight; the versions
live in `latest_version.txt`, `macos_latest_version.txt` and
`tvos_latest_version.txt`.

Apple has granted the Network Extension entitlement, so the App Store
listing is a matter of submission rather than approval.

Phase detail: [`PHASE_STATUS.md`](PHASE_STATUS.md).

## Architecture

```
ios/                              ← you are here
├── App/                          Main app (SwiftUI)
│   ├── Views/                    SwiftUI views
│   ├── ViewModels/               @Observable view models
│   └── Resources/                Assets + Localizable.strings × 6
│
├── Tunnel/                       Network Extension PacketTunnelProvider
│   ├── WireGuardBridge/          WireGuardKit integration
│   ├── AmneziaWGBridge/          amneziawg-go via gomobile XCFramework
│   └── OpenVPNBridge/            OpenVPNAdapter integration
│       (IPSec NOT here — NEVPNManager directly from App/)
│
├── Core/                         Shared business logic (Swift Package)
│   └── Sources/PrivycsCore/
│       ├── Models/               Codable: SavedConnection, Pool, etc.
│       ├── Storage/              Keychain-backed persistence
│       ├── Network/              NWPathMonitor abstraction
│       ├── Pool/                 PoolRotator + Geo-Nearest
│       ├── Crypto/               ed25519 license verify
│       ├── Entitlement/          StoreKit 2 integration
│       ├── CrashReporting/       sentry-cocoa (Bugsink endpoint)
│       └── I18n/                 String catalog helpers
│
├── ThirdParty/                   Vendored sources (gitignored binaries)
│   └── AmneziaWG/                gomobile XCFramework build artefact
│
├── Scripts/                      Build & deploy helpers
│   ├── build-amneziawg-xcframework.sh
│   └── prepare-bundle.sh
│
└── .github/workflows/            CI (build + TestFlight + App Store submission)
```

## Tech stack

| Layer | Pick | Licence |
|---|---|---|
| UI | SwiftUI + `@Observable` (iOS 17+ baseline) | Apple |
| Reactive | async/await + Combine where needed | Apple |
| Persistence | Keychain Services + `UserDefaults(suiteName:)` (App Group) | Apple |
| Pool storage | GRDB.swift (SQLite) | MIT |
| WireGuard | [WireGuardKit](https://git.zx2c4.com/wireguard-apple) | MIT |
| AmneziaWG | gomobile XCFramework from [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) | MIT |
| OpenVPN | [OpenVPNAdapter](https://github.com/ss-abramchuk/OpenVPNAdapter) | MIT (wraps Apache 2 OpenVPN3) |
| IPSec | NEVPNManager + NEIKEv2VPNConfiguration | Apple |
| StoreKit | StoreKit 2 (iOS 15+) | Apple |
| Crash reporting | [sentry-cocoa](https://github.com/getsentry/sentry-cocoa) → Bugsink at crashes.privycs.com (project 3) | MIT |

## Licensing strategy

```
Privycs iOS app code (Swift, ours)      →  GPL-3
   └── OpenVPNAdapter (Swift wrapper)   →  MIT
         └── OpenVPN3 (C++ core)        →  Apache 2.0
   └── WireGuardKit                     →  MIT
   └── amneziawg-go XCFramework         →  MIT
```

All third-party deps are permissive (MIT/Apache). Our own code is GPL-3 — compatible with the App Store EULA via the §7 "Additional Permissions" mechanism. **Not** canonical OpenVPN 2.x (GPL-2 → potential §6 conflict).

See [`LICENSE_NOTES.md`](LICENSE_NOTES.md) for detail.

## Bundle IDs + Apple setup

| Asset | Identifier |
|---|---|
| Main App | `com.privycs.vpn` |
| Tunnel Extension | `com.privycs.vpn.tunnel` |
| App Group | `group.com.privycs.vpn` |
| Apple Developer Team | (TBD after enrolment check) |
| Entitlement | `com.apple.developer.networking.networkextension` |

The Apple setup checklist lives in the **internal notes** (`privycs-vpn-private-docs/ios-apple-setup.md`), not in the public repo. See `docs/README.md` for the pointer. Irrelevant to external contributors — the code in this repo builds without any Apple-account-specific setup steps.

## Minimum iOS version

**iOS 17.0+** — covers ~85% of the installed base in 2026, and gives access to modern `@Observable`, the latest StoreKit 2 APIs, and performant SwiftUI.

## Build (only on macOS with Xcode)

```bash
git clone https://github.com/hoep/privycs-vpn.git
cd privycs-vpn/ios
# Open in Xcode 16+
open PrivycsVPN.xcodeproj
```

Cross-compiling from Linux is impossible — Xcode + the Swift toolchain + the iOS SDK are mandatory (Apple restriction). Build server: macOS only.

## CI

- TestFlight: every commit to `main` → upload to internal track (manual promote to external)
- App Store: tag `v*` → submit for review
- Crash reporting: sentry-cli upload of dSYMs post-build

## Roadmap

Phases 0-7 are complete — scaffold, PrivycsCore, PacketTunnelProvider,
SwiftUI views, OpenVPN + AmneziaWG, StoreKit 2 / Pro, crash reporting,
real-device testing and TestFlight. The table in
[`PHASE_STATUS.md`](PHASE_STATUS.md) is the maintained version.

The only thing still open is the App Store submission; the Network
Extension entitlement is granted.

## Related repos

- [privycs-vpn](https://github.com/hoep/privycs-vpn) — Android + Desktop (Wails) + Apple clients (this monorepo)
- [privycs](https://github.com/hoep/privycs) — Gateway (server-side, internal)
