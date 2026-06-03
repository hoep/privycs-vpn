# Privycs VPN — Desktop App Privacy Policy

Information pursuant to **Art. 13 and 14 of the General Data Protection Regulation (GDPR)** for the *Privycs VPN* Desktop application — the **Windows, macOS and Linux** builds — distributed as a direct download. This single policy applies to all three desktop operating systems; their data handling is identical (one shared codebase) and only the platform mechanism for tunnels, DNS and the kill switch differs.

**Effective date:** 18 May 2026

This policy is specific to the **desktop application**. It complements, and is consistent with, the general [Privacy Policy](/docs/privacy) (website and management platform) and the [Legal Notice](/docs/legal-notice). The separate [Android App Privacy Policy](/docs/android-client-privacy) covers the Android build. Where an app-specific policy and the general policy differ in scope, the relevant one applies to the respective product. This document describes the app's actual behaviour as implemented in its source code; if a future app version changes data handling, this policy is updated before that version is released.

---

## 1. Data Controller

**Peter Höllwarth**
Email: **support@privycs.com**
Web: https://www.privycs.com

Full identifying and address details: see the [Legal Notice](/docs/legal-notice). No Data Protection Officer is required for processing of this scale; data-protection enquiries are handled via the address above.

---

## 2. Summary and core principle

Privycs VPN is a **self-hosted VPN management client**, **not** a VPN service/provider. It runs no VPN servers that your traffic passes through, provides no exit IP, and sells no tunnels or bandwidth. The encrypted VPN tunnel is established **between your computer and a gateway that you configure** (your own self-hosted server, a commercial provider whose config you import, or a Privycs-operated gateway you separately control and have access to).

By design the controller **operates no server that receives your personal data through the app**, except for the narrowly-listed, mostly optional cases in Section 4. The app contains **no analytics, tracking, advertising, telemetry, or crash-reporting SDKs**, and performs **no automatic update or "phone-home" checks**. It does **not** log, inspect, store, transmit, or sell the contents of your VPN traffic.

---

## 3. Data stored exclusively on your device

The following is held only in the app's per-user storage directory on your computer and is **not transmitted to the controller**. The controller therefore has no copy and performs no server-side processing of it:

- Imported VPN configuration files (WireGuard/AmneziaWG `.conf`, OpenVPN `.ovpn`, IPSec `.sswan`/`.mobileconfig`), **including the private keys, certificates and credentials they contain**.
- Optional Pro feature: the gateway URL and API key you enter.
- Connection/pool/rule settings: connection names, protocols, per-connection DNS overrides, Wi-Fi SSID/BSSID lists you enter for on-demand rules, kill-switch / split-tunnel / theme / on-demand preferences, pool definitions and state.
- Local diagnostic logs for your own troubleshooting; viewable/clearable in-app; **never uploaded** by the app.
- A bundled offline IP-to-country database (contains no personal data; used locally, makes no network request).
- Optional **encrypted backup** files you create: the connections registry and settings, serialized and encrypted locally with **AES-256-GCM** using a key derived from your passphrase (≥600,000 KDF iterations). The backup is created on your explicit action, stays where you save it, and is **never uploaded** by the app. Only you hold the passphrase.

Wi-Fi SSID/BSSID values are evaluated **only locally** for your on-demand rules and are never transmitted off the device.

**Legal basis:** Not applicable as controller processing — this data does not leave your device and is under your sole control (Art. 5/Art. 4(2) GDPR: no processing by the controller occurs).

---

## 4. Data that leaves the device — purposes, recipients, legal bases

| # | Processing | Data | Recipient | When | Legal basis |
|---|---|---|---|---|---|
| 4.1 | VPN tunnel + optional config pull/refresh (Pro) | Your API key (Bearer) and config requests; tunnel traffic is end-to-end between you and your gateway | **Only the gateway you configure** (no hardcoded/default Privycs host) | When you connect, or use the optional Pro pull feature with a gateway URL + API key you entered | Art. 6(1)(b) — performance of the function you requested |
| 4.2 | Public-IP → country lookup for the optional "Geo-Nearest" pool policy | Your public IP address (only the network request itself; no identifiers, nothing about your device or VPN) | Third parties, first success wins: **Cloudflare** (`1.1.1.1/cdn-cgi/trace`), **Cloudflare** (`icanhazip.com`), **Mullvad** (`am.i.mullvad.net`). On total failure an **offline timezone heuristic** is used (no request) | Only if you use Pools with the Geo-Nearest policy and have not disabled auto-detect; result cached ~1 h | Art. 6(1)(f) — legitimate interest in selecting a nearby endpoint; avoidable entirely by not using Geo-Nearest or by setting the country manually |
| 4.3 | Opening documentation / help | A normal website visit (your browser sends standard HTTPS metadata: IP, timestamp, user-agent) | **Privycs web server** (www.privycs.com), opened in **your default browser** — not fetched inside the app | Only when you click a docs/help link | Art. 6(1)(f) — providing documentation; governed by the general [Privacy Policy](/docs/privacy) |
| 4.4 | Pro license purchase (optional) | Purchase is made **on our website**, handled by our payment provider; the desktop app itself validates the resulting cryptographically-signed (ed25519) license key **offline** and does **not** transmit it | **LemonSqueezy** (merchant of record / payment processor) for the website purchase only | Only if you buy Pro | Art. 6(1)(b) — performing the purchase you initiated; LemonSqueezy processes payment data as its own controller under its policy |

These third parties are independent controllers for the data they receive and process it under their own privacy policies (Cloudflare, Mullvad, and LemonSqueezy). The data shared is minimal and, for 4.2, incidental to a single network request that you can avoid.

**No other network connections are made by the app.** In particular, there is **no automatic update check** and no background connection to any Privycs server.

---

## 5. International transfers

The recipients in Section 4.2 and 4.4 may process the request outside the EEA, including in the United States (Cloudflare and LemonSqueezy are US-based; Mullvad is EU/Sweden-based). For 4.2 the data is at most your public IP address from a single, avoidable request. These providers maintain their own transfer safeguards (e.g. EU Standard Contractual Clauses / Data Privacy Framework participation, as applicable to each provider). The controller itself does not transfer your personal data internationally, because the app does not send your personal data to the controller.

---

## 6. Data the app does NOT collect or process

No collection, transmission, sale, ad-sharing, tracking or profiling of: device location/GPS; advertising or device identifiers; machine name, MAC, serial or hardware fingerprint; contacts, files outside the configs you import, microphone or camera; the list of installed applications (the per-app split-tunnel screen enumerates processes **locally** only); behavioural/usage analytics; VPN traffic contents. There is **no user account or sign-up** in the app, and **no automatic update telemetry**.

---

## 7. OS integration and the privileged helper

To create VPN tunnels and enforce the kill switch, the desktop client installs a small **privileged helper** that runs with elevated rights, local to your machine:

| Platform | Helper mechanism | Kill switch |
|---|---|---|
| Windows | Service via the Service Control Manager (SCM) | Windows Filtering Platform (WFP) |
| macOS | `LaunchDaemon` (root), approved by you on first connect | packet filter (pf) |
| Linux | `systemd`-managed privileged helper | `iptables` sinkhole chain (snapshot + rollback) |

The helper exists **solely** to configure the network interface, routes, DNS and firewall rules for the tunnel you start, and to tear them down again. It communicates only over **local inter-process channels** (local socket / named pipe), processes no personal data beyond what is needed locally for the active tunnel, and **transmits nothing off the device**. You can uninstall the helper from the app's Settings.

---

## 8. Retention and security

All app data lives on your computer. **Uninstalling the app (and the privileged helper) removes the app's data**; backup files you exported yourself remain wherever you saved them until you delete them. You can clear logs and delete connections/pools/configs in-app at any time. The controller stores none of it server-side, because the app does not send it to the controller. Storage is in your per-user profile directory under the operating system's normal file permissions; encrypted backups use AES-256-GCM with a high-iteration key derivation and are only as recoverable as the passphrase you choose. All outbound requests in Section 4 use HTTPS/TLS. The third parties in Section 4 apply their own retention to anything they receive.

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

Privycs VPN is a technical administration tool intended for adults. It is **not directed to children** and we do not knowingly process children's data.

---

## 12. Changes

This policy is updated whenever the app's data handling changes, before the affected version ships. The current version, with its effective date, is always available at this URL.

---

## 13. Contact

Privacy / data-protection enquiries: **support@privycs.com**
Legal-entity and address details: [Legal Notice](/docs/legal-notice)
General website/platform privacy: [Privacy Policy](/docs/privacy)
Android build: [Android App Privacy Policy](/docs/android-client-privacy)

*Last updated: 18 May 2026*
