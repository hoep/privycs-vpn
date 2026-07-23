# iOS Port — Phase Status

Live status table — updated per commit.

| Phase | Description | Status |
|---|---|---|
| 0 | Repo scaffold, Apple setup docs, project structure | ✅ Complete |
| 1 | PrivycsCore: Models + Storage + Network + Pool + Crypto + Entitlement + CrashReporter + I18n | ✅ Complete |
| 2 | PacketTunnelProvider scaffold + WireGuard bridge | ✅ Complete |
| 3 | SwiftUI app skeleton: 5-tab nav, all main views, VPNTunnelManager | ✅ Complete |
| 4 | AmneziaWG XCFramework build script + bridge wiring + OVPN delegate impl + compat preprocessor | ✅ Complete |
| 5 | NetworkRulesView + PoolDetailView + AddPoolView + GatewayAPIClient + QR scanner + license-key import | ✅ Complete |
| 6 | SSID provider + AddRule sheet + lifecycle polishing | ✅ Complete |
| 7 | Real-device testing + TestFlight beta | ✅ Complete |
| 8 | App Store submission | ⏳ Entitlement granted, submission pending |

## Apple Dev setup — long pole

Done (the TestFlight upload in `ios-release.yml` proves it — without these
three, no build gets past altool validation):

- [x] Bundle IDs `com.privycs.vpn` + `com.privycs.vpn.tunnel` registered
- [x] App Group `group.com.privycs.vpn`
- [x] **Network Extension entitlement** — granted by Apple
- [ ] App Store Connect: new app + IAP `com.privycs.vpn.pro_lifetime`
- [ ] Bugsink: new project "privycs-vpn-ios" → replace DSN .../3 in `CrashReporter.swift`
- [ ] Replace the production ed25519 pubkey in `LicenseVerifier.productionPubkey` (from `privycs-vpn-private-docs/license-keypair.txt`)

## Local build on a Mac

```bash
cd ios
brew install xcodegen
./Scripts/build-amneziawg-xcframework.sh   # builds amneziawg-go → XCFramework
xcodegen generate                            # generates PrivycsVPN.xcodeproj
open PrivycsVPN.xcodeproj
```

In Xcode:
1. Select target → Signing & Capabilities → Team
2. Both targets (App + Tunnel) → Enable Network Extensions + App Groups
3. Run on a Simulator or device

## Linux unit tests (for CI)

```bash
cd ios/Core
swift test                                   # runs ModelsCodable / PoolRotator /
                                             # NetworkRulesEngine / OVPNCompat
                                             # tests on any platform
```

## What does NOT run in the PrivycsCore tests

- KeychainSecretStore (needs Apple's Security framework)
- NetworkMonitor (needs NWPathMonitor)
- CrashReporter (needs sentry-cocoa)
- Repositories (need Keychain/UserDefaults)
- App-layer views (need SwiftUI)

Real-device tests are covered by phase 7 (Mac CI runner + TestFlight beta).
