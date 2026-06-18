# Lizenz-Strategie & Third-Party-Dependencies

Der Privycs VPN iOS/macOS-Client ist **Freie Software**. Der Privycs-eigene Code
steht unter **GPL-3.0** (gleicher Lizenztyp wie der Privycs Android- und
Desktop-Client). Da der Client den **AGPL-3.0**-lizenzierten `OpenVPNAdapter`
einlinkt, wird die **kombinierte App als AGPL-3.0 ausgeliefert** (GPL-3 und
AGPL-3 sind über §13 kompatibel; AGPL ist die stärkere und damit maßgebliche
Lizenz). Der vollständige korrespondierende Quellcode ist öffentlich:
https://github.com/hoep/privycs-vpn

> **Korrektur 2026-06-18 (Audit):** Frühere Versionen dieser Datei behaupteten,
> OpenVPNAdapter sei MIT und OpenVPN3 Apache-2.0, und „AGPL = NEVER". Das war
> **falsch**: `OpenVPNAdapter` ist **AGPL-3.0** (README, kein kommerzielles
> Dual-License-Angebot), OpenVPN3-Core ist **MPL-2.0 OR AGPL-3.0**. Die App ist
> daher AGPL-3.0. Da der Quellcode ohnehin öffentlich ist (FOSS), ist das
> compliant — analog ProtonVPN iOS.

## App-Store-Kompatibilität (FOSS/AGPL-Modell, wie ProtonVPN)

Apples App Store EULA fügt Restriktionen hinzu, die mit GPL/AGPL §6 ("no further
restrictions") kollidieren (historischer VLC-Precedent). Privycs löst das über
das offene FOSS-Modell:

1. **Öffentlicher Quellcode** erfüllt die GPL/AGPL-Quellpflicht (corresponding
   source) für *alle* Empfänger — auch App-Store-Nutzer. Das ist der Kern der
   AGPL-Konformität.
2. **§7-Zusatzerlaubnis** (`EXCEPTIONS.md`): Als alleiniger Copyright-Holder des
   Privycs-eigenen Codes gewährt Privycs die App-Store-Distribution ungeachtet
   §6. Greift für den Privycs-Code; Drittkomponenten behalten ihre Lizenz.
3. **Etablierter Präzedenzfall**: ProtonVPN iOS (Open-Source, AGPL) liefert
   OpenVPNAdapter/OpenVPN3 genau so über den App Store aus.

> ⚠️ Die App-Store-Distribution AGPL-lizenzierter **Dritt**komponenten (ohne
> deren eigene §7-Erlaubnis) ist die verbleibende rechtliche Grauzone. Der
> Standard-Weg ist „FOSS + öffentliche Quelle" (ProtonVPN). **Finale Abnahme
> durch einen Anwalt vor Public Release.** Alternativen, falls Closed-Source
> gewünscht wäre: eigenen MPL-2.0-OpenVPN3-Wrapper schreiben (statt des
> AGPL-Adapters) oder kommerzielle OpenVPN-Lizenz — beides aktuell NICHT
> verfolgt, da der Client ohnehin FOSS ist.

## Dependency-Stack mit Lizenzen

### Core Framework

| Package | Lizenz | Quelle | Zweck |
|---|---|---|---|
| GRDB.swift | MIT | https://github.com/groue/GRDB.swift | SQLite ORM für Pool-Mitglieder |
| swift-crypto | Apache 2.0 | https://github.com/apple/swift-crypto | ed25519 license-key verify |

### Tunnel (Network Extension)

| Package | Lizenz | Quelle | Zweck |
|---|---|---|---|
| WireGuardKit | MIT | https://git.zx2c4.com/wireguard-apple | WireGuard-Protokoll |
| amneziawg-go (XCFramework) | MIT | https://github.com/amnezia-vpn/amneziawg-go | AmneziaWG (= WG + Obfuskation) |
| **OpenVPNAdapter** | **AGPL-3.0** | https://github.com/ss-abramchuk/OpenVPNAdapter | OpenVPN3-Wrapper für iOS — **macht die App AGPL-3.0** |
| OpenVPN3 (transitiv via OpenVPNAdapter) | MPL-2.0 OR AGPL-3.0 (WITH openvpn3-openssl-exception) | https://github.com/OpenVPN/openvpn3 | OpenVPN-Protokoll-Core |
| mbedTLS / OpenSSL (transitiv) | Apache 2.0 / Apache 2.0 | OpenVPN3-Build-Dep | TLS-Crypto |

### UI / Storage / Telemetry

| Package | Lizenz | Quelle | Zweck |
|---|---|---|---|
| sentry-cocoa | MIT | https://github.com/getsentry/sentry-cocoa | Crash-Reporting (Backend: self-hosted Bugsink) |
| StoreKit 2 | Apple | (system framework) | In-App Purchases für Pro-Tier |
| swift-system | Apache 2.0 | https://github.com/apple/swift-system | Low-level POSIX wrappers |
| swift-collections | Apache 2.0 | https://github.com/apple/swift-collections | OrderedSet etc. für Pool-Logik |

## Lizenz-Klassen-Politik (für neue Dependencies)

- **MIT / Apache-2.0 / BSD / MPL-2.0** = unbedenklich (permissiv bzw. schwaches,
  datei-basiertes Copyleft) — kombinierbar, App-Store-unkritisch.
- **GPL-3.0** = ok für eigenen Code; App-Store via `EXCEPTIONS.md` (§7).
- **AGPL-3.0** = ok, SOLANGE die App FOSS/AGPL bleibt und die Quelle öffentlich
  ist (aktuell durch `OpenVPNAdapter` der Fall). Eine NEUE AGPL-Dep ist kein
  K.o., ändert aber nichts mehr (App ist bereits AGPL) — trotzdem bewusst prüfen.
- **GPL-2-only ohne linking exception** = vermeiden (nicht mit der GPL-3/AGPL-3
  der App kombinierbar). Daher weiterhin NICHT: canonical OpenVPN 2.x,
  ics-openvpn, strongSwan, wg-quick — auf iOS via WireGuardKit / OpenVPNAdapter /
  Apple-NEVPNManager-IKEv2 gelöst.

## Compat-Layer-Code

Der iOS-spezifische **OpenVPN 2.x → 3.x Config-Preprocessor** in
`Core/Sources/PrivycsCore/OVPNCompat/` ist Eigen-Code unter GPL-3 (Teil des
Privycs-Clients). Strippt Direktiven, die in OpenVPN3 nicht verfügbar sind
(`script-security`, `up`/`down`, `plugin`) und normalisiert deprecated Optionen.

## Audit-Trail

Bei jeder neuen Dependency:

1. Lizenz prüfen — `cat ${DEP}/LICENSE` oder Repo-Root (NICHT aus dem Kopf —
   der MIT/Apache-Fehlannahme oben verdanken wir dieses Audit).
2. Diese Datei updaten — Tabelle ergänzen.
3. Lizenz-Klasse gegen die Politik oben prüfen.
4. In-App OSS-Screen (`App/Views/OssLicensesView.swift`) konsistent halten.
