# Privycs VPN — Android App Privacy Policy

Information pursuant to **Art. 13 and 14 of the General Data Protection Regulation (GDPR)** for the *Privycs VPN* Android application (package `com.privycs.vpn`), distributed via Google Play and as a direct download.

**Effective date:** 17 May 2026

This policy is specific to the **Android application**. It complements, and is consistent with, the general [Privacy Policy](/docs/privacy) (website and management platform) and the [Legal Notice](/docs/legal-notice). Where this app-specific policy and the general policy differ in scope, the relevant one applies to the respective product. This document describes the app's actual behaviour as implemented in its source code; if a future app version changes data handling, this policy is updated before that version is released.

---

## 1. Data Controller

**Peter Höllwarth**
Email: **support@privycs.com**
Web: https://www.privycs.com

Full identifying and address details: see the [Legal Notice](/docs/legal-notice). No Data Protection Officer is required for processing of this scale; data-protection enquiries are handled via the address above.

---

## 2. Summary and core principle

Privycs VPN is a **self-hosted VPN management client**, **not** a VPN service/provider. It runs no VPN servers that your traffic passes through, provides no exit IP, and sells no tunnels or bandwidth. The encrypted VPN tunnel is established **between your device and a gateway that you configure** (your own self-hosted server, or a Privycs-operated gateway you separately control and have access to).

By design the controller **operates no server that receives your personal data through the app**, except for the narrowly-listed, mostly optional cases in Section 4. The app contains **no analytics, tracking, advertising, telemetry, or crash-reporting SDKs**. It does **not** log, inspect, store, transmit, or sell the contents of your VPN traffic.

---

## 3. Data stored exclusively on your device

The following is held only in the app's private, OS-sandboxed storage and is **not transmitted to the controller**. The controller therefore has no copy and performs no server-side processing of it:

- Imported VPN configuration files (WireGuard/AmneziaWG `.conf`, OpenVPN `.ovpn`, IPSec `.sswan`/`.mobileconfig`), **including the private keys, certificates and credentials they contain**.
- Optional Pro feature: the gateway URL and API key you enter (stored via Android Jetpack DataStore, encrypted at rest through the Android Keystore).
- Connection/pool/rule settings: connection names, protocols, per-connection DNS overrides, Wi-Fi SSID/BSSID lists you enter for on-demand rules, kill-switch / split-tunnel / theme / on-demand preferences, pool definitions and state.
- Local diagnostic and crash logs (rotating, capped) for your own troubleshooting; viewable/clearable in-app; **never uploaded** by the app.
- A bundled offline IP-to-country database (contains no personal data).

Wi-Fi SSID/BSSID values are evaluated **only locally** for your on-demand rules and are never transmitted off the device.

**Legal basis:** Not applicable as controller processing — this data does not leave your device and is under your sole control (Art. 5/Art. 4(2) GDPR: no processing by the controller occurs).

---

## 4. Data that leaves the device — purposes, recipients, legal bases

| # | Processing | Data | Recipient | When | Legal basis |
|---|---|---|---|---|---|
| 4.1 | VPN tunnel + optional config pull/refresh | Your API key (Bearer), config requests; tunnel traffic is end-to-end between you and your gateway | **Only the gateway you configure** (no hardcoded/default Privycs host) | When you connect or use the Pro pull feature | Art. 6(1)(b) — performance of the function you requested |
| 4.2 | Public-IP → country lookup for the optional "Geo-Nearest" pool policy | Your public IP address (only the network request; no identifiers, nothing about your device or VPN) | Third parties: **Cloudflare (1.1.1.1)**, **ipify.org**, **ifconfig.me** (first success wins) | Only if you use Pools with the Geo-Nearest policy; degrades silently on failure | Art. 6(1)(f) — legitimate interest in selecting a nearby endpoint; you can avoid this entirely by not using Pools/Geo-Nearest |
| 4.3 | In-app Help content | Standard HTTPS request metadata (IP, timestamp, user-agent), as for any website visit | **Privycs web server** (www.privycs.com) | Only when you open the Help screen | Art. 6(1)(f) — providing documentation |
| 4.4 | QR-code import | Camera frames processed **on-device**; the scanner module is downloaded from Google | **Google** (Google Play Services) | First QR use | Art. 6(1)(f) — providing QR import via the platform component |

These third parties are independent controllers for the data they receive and process it under their own privacy policies (Cloudflare, ipify, ifconfig.me, and Google/Google Play Services & Google Play). The data shared is minimal and, for 4.2–4.4, incidental to a normal network request.

**No other network connections are made by the app.**

---

## 5. International transfers

The recipients in Sections 4.2 and 4.4 (Cloudflare, ipify, ifconfig.me, Google) operate globally and may process the request — at most your IP address and standard request metadata — outside the EEA, including in the United States. The data involved is minimal and, for the optional Geo-Nearest feature, can be avoided entirely by not using it. These providers maintain their own transfer safeguards (e.g. EU Standard Contractual Clauses / Data Privacy Framework participation, as applicable to each provider). The controller itself does not transfer your personal data internationally, because the app does not send your personal data to the controller.

---

## 6. Data the app does NOT collect or process

No collection, transmission, sale, ad-sharing, tracking or profiling of: **device location/GPS** (see the location note below); advertising ID; Android ID, IMEI or serial; contacts, call logs, SMS, calendar, microphone, photos; the list of installed apps (the split-tunnel screen lists apps **locally** only; no broad package-query permission is declared); behavioural/usage analytics; VPN traffic contents. There is **no user account or sign-up** in the app.

**Location permissions — important clarification.** The app requests fine/background location and "nearby Wi-Fi devices" **solely because, on Android 10+, the operating system will not reveal the current Wi-Fi network *name* (SSID) to an app unless it holds these permissions** — and the SSID is what Connect-on-Demand rules match against (e.g. "VPN off on my home Wi-Fi"). **Background** location is requested only so this still works while the app is closed or the screen is off; you may decline it (then SSID-based rules only apply while the app is open). Privycs uses these permissions **exclusively to read the Wi-Fi name locally on the device for rule matching**. It does **not** read GPS, does **not** derive, store, or transmit your geographic location, and sends nothing off the device. This is reflected in the Google Play Data-safety declaration.

(Independently of the app, Google Play and the Android OS process some data — e.g. for app distribution, Google Play Services, Play App Signing, and aggregate OS-level crash statistics — under Google's own policies; this is a platform layer outside the app's data handling.)

---

## 7. Android permissions

| Permission | Purpose |
|---|---|
| Internet / Network state / Wi-Fi state | Establish the tunnel; detect network type and Wi-Fi SSID for on-demand rules |
| Location (Fine/Coarse), Nearby Wi-Fi devices | **Only** to let Android reveal the current Wi-Fi name (SSID) for on-demand rules; `neverForLocation`; never for geolocation |
| Background location (`ACCESS_BACKGROUND_LOCATION`) | Optional. **Only** so Android keeps revealing the Wi-Fi name while the app is in the background/screen-off, so on-demand Wi-Fi rules keep working. Requested at first run after a prominent in-app disclosure and only after foreground location is granted. Decline-able; never used for geolocation |
| Foreground service (+ special use) | Keep the VPN/monitor service running while connected |
| Post notifications | VPN status + connection-event notifications |
| Receive boot completed | Optionally re-arm on-demand monitoring after reboot |
| Ignore battery optimizations (user-granted) | Optional: reliable on-demand reaction in standby |
| Schedule/Use exact alarm | Precise pool-rotation timing |

Camera, Contacts, Call Log, SMS, Microphone and broad installed-apps-query permissions are **not** declared.

---

## 8. Retention and security

All app data lives on your device. **Uninstalling the app deletes all of it.** You can clear logs and delete connections/pools/configs in-app at any time. The controller stores none of it server-side, because the app does not send it to the controller. Storage is in the app's OS-sandboxed private area, isolated by Android from other apps; settings holding the gateway URL/API key are encrypted at rest via the Android Keystore; on devices with file-based encryption the storage is additionally OS-encrypted at rest. All outbound requests use HTTPS/TLS. The third parties in Section 4 apply their own retention to anything they receive.

---

## 9. Your rights (GDPR)

You have the rights of **access (Art. 15)**, **rectification (Art. 16)**, **erasure (Art. 17)**, **restriction (Art. 18)**, **data portability (Art. 20)** and **objection (Art. 21)**, and the right to **withdraw consent** at any time where processing is based on consent (without affecting prior lawful processing). There is **no automated decision-making or profiling within the meaning of Art. 22**. In practice these rights are largely satisfied by your direct, local control of the data and by uninstalling the app, since the controller holds no app-collected personal data.

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

Privycs VPN is a technical administration tool intended for adults. It is **not directed to children**, is rated for an adult audience, and we do not knowingly process children's data.

---

## 12. Changes

This policy is updated whenever the app's data handling changes, before the affected version ships. The current version, with its effective date, is always available at this URL.

---

## 13. Contact

Privacy / data-protection enquiries: **support@privycs.com**
Legal-entity and address details: [Legal Notice](/docs/legal-notice)
General website/platform privacy: [Privacy Policy](/docs/privacy)

*Last updated: 17 May 2026*
