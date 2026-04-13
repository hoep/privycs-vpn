# Privycs VPN Client

**One App. Three Protocols. Your Server.**

Multi-protocol VPN client for Android and iOS that supports WireGuard, OpenVPN, and IPSec/IKEv2 in a single application. Works with any VPN server -- not limited to Privycs.

## Features

- **WireGuard** -- Import .conf files, instant connect
- **OpenVPN** -- Import .ovpn files with embedded certificates
- **IPSec/IKEv2** -- Import .sswan (Android) or .mobileconfig (iOS) profiles
- **Auto-Connect** -- Reconnect on WiFi change or app start
- **Kill Switch** -- Block traffic when VPN disconnects (in-app toggle)
- **Always-On VPN** -- In-app toggle, not buried in OS settings
- **Per-App VPN** -- Exclude apps from the VPN tunnel (Android)
- **Config Sync** -- Download configs from your server via API key
- **Cloud Backup** -- E2E encrypted backup to iCloud / Google Drive
- **Protocol Switching** -- Switch between WG/OVPN/IPSec with one tap
- **No Subscription** -- Pay once, use forever
- **No Ads** -- No tracking, no telemetry
- **Open Source** -- GPL-3.0 licensed

## Download

- **Android:** [Google Play Store](#) (coming soon)
- **iOS:** [App Store](#) (coming soon)

## Free vs Pro

| Feature | Free | Pro (6.99 EUR) |
|---------|------|----------------|
| All 3 protocols | Yes | Yes |
| Manual config import | Yes | Yes |
| 1 VPN connection | Yes | Yes |
| Unlimited connections | -- | Yes |
| Auto-Connect | -- | Yes |
| Kill Switch | -- | Yes |
| Always-On VPN | -- | Yes |
| Per-App VPN (Android) | -- | Yes |
| Config Sync (API Key) | -- | Yes |
| Cloud Backup (E2E) | -- | Yes |
| Widgets | -- | Yes |

## Compatibility

Works with any VPN server that supports standard protocols:

- Any WireGuard server (self-hosted, cloud, NAS)
- Any OpenVPN server (pfSense, OPNsense, OpenWrt, Synology, QNAP)
- Any IPSec/IKEv2 server (strongSwan, Cisco, Fortinet, native OS)
- Privycs Gateway (with API key config sync)
- Unifi Dream Machine / Dream Router
- Fritz!Box VPN
- Tailscale / Headscale (WireGuard configs)

## Building

### Android

```bash
cd android
./gradlew assembleDebug
```

Requires Android Studio Iguana or later, Android SDK 34, JDK 17.

### iOS

```bash
cd ios
open PrivycsVPN.xcodeproj
```

Requires Xcode 15+, iOS 16+ deployment target.

## Architecture

```
android/                  # Android app (Kotlin, Jetpack Compose)
  app/                    # Main application module
  vpn-wireguard/          # WireGuard VPN service (wireguard-android, MIT)
  vpn-openvpn/            # OpenVPN VPN service (ics-openvpn, GPL-2.0)
  vpn-ipsec/              # IPSec VPN service (strongSwan, GPL-2.0)

ios/                      # iOS app (Swift, SwiftUI)
  PrivycsVPN/             # Main app target
  PacketTunnel-WG/        # WireGuard Network Extension (WireGuardKit, MIT)
  PacketTunnel-OVPN/      # OpenVPN Network Extension (OpenVPNAdapter, AGPL-3.0)
  (IPSec uses native NEVPNProtocolIKEv2 -- no extension needed)

shared/                   # Shared documentation and assets
  AppKonzept.md           # Product and marketing plan
```

## License

This project is licensed under the **GNU General Public License v3.0** (GPL-3.0).

This is required because the Android app links against GPL-licensed libraries (ics-openvpn, strongSwan) and the iOS app links against AGPL-licensed OpenVPNAdapter.

The WireGuard components (wireguard-android, WireGuardKit) are MIT-licensed.
The iOS IPSec implementation uses Apple's native NEVPNProtocolIKEv2 API (no third-party library).

## Related Projects

- [Privycs Gateway](https://github.com/hoep/privycs) -- Self-hosted VPN management platform (server-side)
- [Privycs Desktop](https://github.com/hoep/privycs/tree/main/desktop) -- Desktop VPN client (Windows, macOS, Linux)

## Contributing

Contributions welcome. Please open an issue first to discuss significant changes.
