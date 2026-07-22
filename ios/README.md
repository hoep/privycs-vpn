# Privycs VPN — iOS Client

iOS-Port des Privycs VPN Clients mit 100% Feature-Parity zur Android-Version. Swift + SwiftUI + Network Extension.

Lebt im **Monorepo** [`privycs-vpn`](https://github.com/hoep/privycs-vpn) neben `desktop/` + `android/`. Per-platform CI workflows path-gated.

## Status

**Feature-complete, in TestFlight.** iOS, iPadOS, macOS und tvOS werden
per Tag aus `ios-release.yml` gebaut und nach TestFlight geladen; die
Versionen stehen in `latest_version.txt`, `macos_latest_version.txt` und
`tvos_latest_version.txt`.

Apple hat das Network-Extension-Entitlement erteilt — der App-Store-
Eintrag ist damit eine Frage der Einreichung, nicht der Genehmigung.

Phasen-Detail: [`PHASE_STATUS.md`](PHASE_STATUS.md).

## Architektur

```
ios/                              ← du bist hier
├── App/                          Main app (SwiftUI)
│   ├── Views/                    SwiftUI views
│   ├── ViewModels/               @Observable view models
│   └── Resources/                Assets + Localizable.strings × 6
│
├── Tunnel/                       Network Extension PacketTunnelProvider
│   ├── WireGuardBridge/          WireGuardKit integration
│   ├── AmneziaWGBridge/          amneziawg-go via gomobile XCFramework
│   └── OpenVPNBridge/            OpenVPNAdapter integration
│       (IPSec NICHT hier — NEVPNManager direkt aus App/)
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

## Tech-Stack

| Layer | Pick | Lizenz |
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

## Lizenz-Strategie

```
Privycs iOS App-Code (Swift, von uns)   →  GPL-3
   └── OpenVPNAdapter (Swift wrapper)   →  MIT
         └── OpenVPN3 (C++ core)        →  Apache 2.0
   └── WireGuardKit                     →  MIT
   └── amneziawg-go XCFramework         →  MIT
```

Alle 3rd-party-deps permissiv (MIT/Apache). Eigener Code GPL-3 — kompatibel mit App-Store-EULA via §7 "Additional Permissions" Mechanismus. **NICHT** canonical OpenVPN 2.x (GPL-2 → potenzieller §6-Konflikt).

Siehe [`LICENSE_NOTES.md`](LICENSE_NOTES.md) für Detail.

## Bundle-IDs + Apple Setup

| Asset | Identifier |
|---|---|
| Main App | `com.privycs.vpn` |
| Tunnel Extension | `com.privycs.vpn.tunnel` |
| App Group | `group.com.privycs.vpn` |
| Apple Developer Team | (TBD nach Enrolment-Check) |
| Entitlement | `com.apple.developer.networking.networkextension` |

Apple-Setup-Checklist liegt in den **internen Notes** (`privycs-vpn-private-docs/ios-apple-setup.md`), nicht im public Repo. Siehe `docs/README.md` für den Verweis. Für externe Contributors irrelevant — der Code in diesem Repo funktioniert ohne Apple-Account-spezifische Setup-Schritte.

## Min-iOS-Version

**iOS 17.0+** — covers ~85% installed base in 2026, gibt Zugang zu modernem `@Observable`, neuesten StoreKit 2 APIs, performant SwiftUI.

## Build (only on macOS with Xcode)

```bash
git clone https://github.com/privycs/privycs-vpn-ios.git
cd privycs-vpn-ios
# Open in Xcode 16+
open PrivycsVPN.xcodeproj
```

Cross-compile von Linux unmöglich — Xcode + Swift-Toolchain + iOS SDK Pflicht (Apple-restriktion). Build-Server: macOS-only.

## CI

- TestFlight: every commit to `main` → upload to internal track (manual promote to external)
- App Store: tag `v*` → submit for review
- Crash reporting: sentry-cli upload of dSYMs post-build

## Roadmap

Phasen 0-7 sind abgeschlossen — Scaffold, PrivycsCore, PacketTunnelProvider,
SwiftUI-Views, OpenVPN + AmneziaWG, StoreKit-2/Pro, Crash-Reporting,
Real-Device-Tests und TestFlight. Die Tabelle in
[`PHASE_STATUS.md`](PHASE_STATUS.md) ist die gepflegte Fassung.

Offen ist nur noch die App-Store-Einreichung; das Network-Extension-
Entitlement liegt vor.

## Verwandte Repos

- [privycs-vpn](https://github.com/privycs/privycs-vpn) — Android + Desktop (Wails) Clients
- [privycs](https://github.com/privycs/privycs) — Gateway (server-side, internal)
