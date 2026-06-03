# Privycs VPN — iOS App Privacy Policy

Information pursuant to **Art. 13 and 14 of the General Data Protection Regulation (GDPR)** for the *Privycs VPN* iOS/iPadOS application (bundle `com.privycs.vpn`), distributed via Apple TestFlight and the App Store.

**Effective date:** 1 June 2026

This policy is specific to the **iOS application**. It complements, and is consistent with, the general [Privacy Policy](/docs/privacy) (website and management platform) and the [Legal Notice](/docs/legal-notice). It describes the app's actual behaviour as implemented in its source code; if a future app version changes data handling, this policy is updated before that version is released.

---

## 1. Data Controller

**Peter Höllwarth**
Email: **support@privycs.com**
Web: https://www.privycs.com

Full identifying and address details: see the [Legal Notice](/docs/legal-notice). No Data Protection Officer is required for processing of this scale; data-protection enquiries are handled via the address above.

---

## 2. Summary and core principle

Privycs VPN is a **self-hosted VPN management client**, **not** a VPN service/provider. It runs no VPN servers that your traffic passes through, provides no exit IP, and sells no tunnels or bandwidth. The encrypted VPN tunnel is established **between your device and a gateway that you configure** (your own self-hosted server, or a Privycs-operated gateway you separately control and have access to).

By design the controller **operates no server that receives your personal data through the app**, except for the narrow, mostly optional cases in Section 4. The app contains **no analytics, tracking, advertising or telemetry SDKs, and no crash-reporting SDK**. It does **not** log, inspect, store, transmit, or sell the contents of your VPN traffic. There is **no user account or sign-up**.

---

## 3. Data stored exclusively on your device

Held only in the app's private, OS-sandboxed storage (iOS Keychain + the app's App Group container) and **not transmitted to the controller**:

- Imported VPN configuration files (WireGuard/AmneziaWG `.conf`, OpenVPN `.ovpn`, IPSec `.sswan`), **including the private keys, certificates and credentials they contain** — stored encrypted via the iOS Keychain.
- Optional Pro feature: the gateway URL and API key you enter (Keychain-stored).
- Connection / pool / rule settings: connection names, protocols, per-connection and per-pool DNS overrides, Wi-Fi SSID/BSSID lists you enter for on-demand rules, kill-switch / split-tunnel / theme / on-demand preferences, pool definitions and runtime state.
- Local diagnostic logs (rotating, capped) for your own troubleshooting; viewable/clearable in-app; **never uploaded** by the app.
- The encrypted backup file you choose to export (AES-256-GCM; you control where it is saved).

Wi-Fi SSID/BSSID values are evaluated **only locally** for your on-demand rules and are never transmitted off the device.

**Legal basis:** Not applicable as controller processing — this data does not leave your device and is under your sole control (Art. 4(2) GDPR: no processing by the controller occurs).

---

## 4. Data that leaves the device — purposes, recipients, legal bases

| # | Processing | Data | Recipient | When | Legal basis |
|---|---|---|---|---|---|
| 4.1 | VPN tunnel + optional config pull/refresh | Your API key (Bearer), config requests; tunnel traffic is end-to-end between you and your gateway | **Only the gateway you configure** (no hardcoded/default Privycs host) | When you connect or use the Pro pull feature | Art. 6(1)(b) — performance of the function you requested |
| 4.2 | Pro in-app purchase | Purchase/transaction handled entirely by Apple's StoreKit; the app receives only a signed transaction it can verify | **Apple** (App Store / StoreKit) | Only if you buy Pro | Art. 6(1)(b) |
| 4.3 | Optional cross-platform license redeem | The signed Apple transaction is exchanged for an ed25519 license at your gateway | **Only the gateway you configure** | Only if you redeem Pro across platforms | Art. 6(1)(b) |

QR-code import is processed **entirely on-device** by Apple's AVFoundation camera framework — no third-party scanner module, no network request. The optional "Geo-Nearest" pool policy selects a nearby server using your **device region setting** (`Locale`) — it makes **no public-IP lookup and no external geolocation request**.

**No other network connections are made by the app.**

---

## 5. International transfers

The only third party in Section 4 is **Apple** (App Store / StoreKit / TestFlight), which processes purchase and app-distribution data under Apple's own privacy policy and may do so outside the EEA. The controller itself does not transfer your personal data internationally, because the app does not send your personal data to the controller.

---

## 6. Data the app does NOT collect or process

No collection, transmission, sale, ad-sharing, tracking or profiling of: **device location/GPS** (see the Wi-Fi note below); advertising identifier (IDFA); device identifiers; contacts, call logs, messages, calendar, microphone, photos; the list of installed apps; behavioural/usage analytics; crash reports; VPN traffic contents. There is **no user account or sign-up**, and the app integrates **no analytics, advertising or crash-reporting SDK**.

**Wi-Fi name — important clarification.** To match On-Demand & Network Rules against the current Wi-Fi network (e.g. "VPN off on my home Wi-Fi"), the app reads the current SSID/BSSID via Apple's `NEHotspotNetwork`, which requires the **Access Wi-Fi Information** capability and **Location-When-In-Use** permission (iOS ties Wi-Fi-name visibility to location). Privycs uses these **exclusively to read the Wi-Fi name/BSSID locally on the device for rule matching while the app is in the foreground**. It does **not** read GPS, does **not** derive, store, or transmit your geographic location, and sends nothing off the device. You may decline location/Wi-Fi access — then SSID/BSSID rules don't match, but network-type rules still work.

(Independently of the app, Apple and iOS process some data — e.g. for app distribution, TestFlight, StoreKit and OS-level diagnostics — under Apple's own policies; this is a platform layer outside the app's data handling.)

---

## 7. iOS permissions & entitlements

| Permission / entitlement | Purpose |
|---|---|
| Personal VPN / NetworkExtension (`com.apple.developer.networking.vpn.api`, `…networkextension`) | Establish and control the VPN tunnel |
| Access Wi-Fi Information (`…networking.wifi-info`) + Location-When-In-Use | **Only** to read the current Wi-Fi name (SSID) / BSSID locally for on-demand rules; never for geolocation |
| Camera (`NSCameraUsageDescription`) | On-device QR-code scanning for config import; no images stored or transmitted |
| Background modes (fetch / processing) | Best-effort background pool rotation (`BGTaskScheduler`) |
| App Group + Keychain access group | Share encrypted config/state between the app and its NetworkExtension |

Contacts, photos, microphone, calendar, motion, and tracking (App Tracking Transparency) are **not** requested — the app performs no tracking.

---

## 8. Retention and security

All app data lives on your device. **Deleting the app deletes all of it.** You can clear logs and delete connections/pools/configs in-app at any time. The controller stores none of it server-side, because the app does not send it to the controller. Configs, keys and the gateway URL/API key are stored encrypted in the **iOS Keychain**; the app runs in Apple's per-app sandbox with iOS file-level encryption at rest. Backups you export are encrypted with AES-256-GCM under a passphrase you choose. All outbound requests use HTTPS/TLS. Apple applies its own retention to purchase/distribution data it receives.

---

## 9. Your rights (GDPR)

You have the rights of **access (Art. 15)**, **rectification (Art. 16)**, **erasure (Art. 17)**, **restriction (Art. 18)**, **data portability (Art. 20)** and **objection (Art. 21)**, and the right to **withdraw consent** at any time where processing is based on consent (without affecting prior lawful processing). There is **no automated decision-making or profiling within the meaning of Art. 22**. In practice these rights are largely satisfied by your direct, local control of the data and by deleting the app, since the controller holds no app-collected personal data.

To exercise your rights: **support@privycs.com**.

---

## 10. Right to lodge a complaint

You have the right to lodge a complaint with the competent supervisory authority:

**Austrian Data Protection Authority**
Barichgasse 40–42, 1030 Vienna, Austria
Phone: +43 1 52 152-0
Email: dsb@dsb.gv.at
Web: https://www.dsb.gv.at

---

## 11. Children

Privycs VPN is a technical administration tool intended for adults. It is **not directed to children** and we do not knowingly process children's data.

---

## 12. Changes

This policy is updated whenever the app's data handling changes, before the affected version ships. The current version, with its effective date, is always available at this URL.

---

## 13. Contact

Privacy / data-protection enquiries: **support@privycs.com**
Legal-entity and address details: [Legal Notice](/docs/legal-notice)
General website/platform privacy: [Privacy Policy](/docs/privacy)

*Last updated: 1 June 2026*
