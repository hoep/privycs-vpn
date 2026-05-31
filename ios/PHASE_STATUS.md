# iOS Port — Phase Status

Live status table — updates per commit.

| Phase | Beschreibung | Status |
|---|---|---|
| 0 | Repo-scaffold, Apple-setup-docs, project structure | ✅ Complete |
| 1 | PrivycsCore: Models + Storage + Network + Pool + Crypto + Entitlement + CrashReporter + I18n | ✅ Complete |
| 2 | PacketTunnelProvider scaffold + WireGuard bridge | ✅ Complete |
| 3 | SwiftUI App skeleton: 5-tab nav, all main views, VPNTunnelManager | ✅ Complete |
| 4 | AmneziaWG XCFramework build script + bridge wiring + OVPN delegate impl + Compat-Preprocessor | ✅ Complete |
| 5 | NetworkRulesView + PoolDetailView + AddPoolView + GatewayAPIClient + QR scanner + License-key import | ✅ Complete |
| 6 | SSID provider + AddRule sheet + lifecycle polishing | ✅ Complete |
| 7 | Real-device testing + TestFlight beta + App-Store submission | ⏳ **Awaiting Apple Dev secrets + Entitlement approval** |

## Apple Dev Setup — Long Pole

User-required (alle drei dauern unterschiedlich lang):

- [ ] Bundle-IDs `com.privycs.vpn` + `com.privycs.vpn.tunnel` registrieren (30 min)
- [ ] App Group `group.com.privycs.vpn` (10 min)
- [ ] **Network Extension Entitlement Request** an Apple — Approval ~1-2 Wochen (kritischer Pfad)
- [ ] App Store Connect: New App + IAP `com.privycs.vpn.pro_lifetime`
- [ ] Bugsink: neues Project "privycs-vpn-ios" → DSN .../3 ersetzen in `CrashReporter.swift`
- [ ] Production ed25519 Pubkey in `LicenseVerifier.productionPubkey` ersetzen (aus `privycs-vpn-private-docs/license-keypair.txt`)

## Lokal-Build auf Mac

```bash
cd ios
brew install xcodegen
./Scripts/build-amneziawg-xcframework.sh   # baut amneziawg-go → XCFramework
xcodegen generate                            # erzeugt PrivycsVPN.xcodeproj
open PrivycsVPN.xcodeproj
```

In Xcode:
1. Select target → Signing & Capabilities → Team
2. Beide Targets (App + Tunnel) → Enable Network Extensions + App Groups
3. Run on Simulator oder device

## Linux Unit-Tests (für CI)

```bash
cd ios/Core
swift test                                   # runs ModelsCodable / PoolRotator /
                                             # NetworkRulesEngine / OVPNCompat
                                             # tests on any platform
```

## Was NICHT in PrivycsCore-Tests läuft

- KeychainSecretStore (braucht Apple-Security framework)
- NetworkMonitor (braucht NWPathMonitor)
- CrashReporter (braucht sentry-cocoa)
- Repositories (brauchen Keychain/UserDefaults)
- App-Layer Views (brauchen SwiftUI)

Real-device-Tests kommen mit Phase 7 (Mac CI runner + TestFlight beta).
