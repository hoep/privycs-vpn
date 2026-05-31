# Lizenz-Strategie & Third-Party-Dependencies

Privycs VPN iOS Client steht unter **GPL-3.0** (gleicher Lizenztyp wie der Privycs Android- und Desktop-Client). Diese Datei dokumentiert die genaue Lizenz-Stack jeder verwendeten 3rd-party-Bibliothek und warum die Kombination App-Store-kompatibel ist.

## App-Store-Kompatibilität

Apple's App Store Licensed Application End User License Agreement (EULA) erzwingt Restrictions die mit **GPL-2** in Konflikt stehen (§6 "no further restrictions"). Der historische VLC-Precedent zeigt das Risiko.

**Privycs umgeht das Risiko vollständig**:

1. **Eigener App-Code = GPL-3.0** (nicht GPL-2). GPL-3 hat in §7 expliziten "Additional Permissions" Mechanismus den Apps-Store-Distributors nutzen können. Privycs ist als alleiniger Copyright-Holder berechtigt, eine zusätzliche Permission für App Store Distribution selbst festzulegen.
2. **Alle 3rd-Party-Dependencies sind permissiv** (MIT / Apache 2 / BSD) — keine copyleft-Klauseln die mit App-Store-EULA kollidieren würden.

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
| OpenVPNAdapter | MIT | https://github.com/ss-abramchuk/OpenVPNAdapter | OpenVPN3-Wrapper für iOS |
| OpenVPN3 (transitiv via OpenVPNAdapter) | Apache 2.0 | https://github.com/OpenVPN/openvpn3 | OpenVPN-Protokoll (modern Apache-2 reimpl) |
| mbedTLS (transitiv) | Apache 2.0 | OpenVPN3-Build-Dep | TLS-Crypto |

**Bewusste Entscheidung**: NICHT canonical OpenVPN 2.x (GPL-2 mit linking exception). Begründung:

- Apple App Store EULA fügt Restrictions hinzu die §6 von GPL-2 nominell verbietet
- Historischer VLC-Precedent (App-Store-Pull 2011 nach FSF-Beschwerde)
- OpenVPN Inc. hat **selbst** OpenVPN3 als Apache 2.0 reimpl entwickelt, genau um diesen Konflikt zu vermeiden
- OpenVPN3 wird produktiv von ProtonVPN iOS, NordVPN iOS, OpenVPN Connect (Apple's own app) genutzt — etabliert

### UI / Storage / Telemetry

| Package | Lizenz | Quelle | Zweck |
|---|---|---|---|
| sentry-cocoa | MIT | https://github.com/getsentry/sentry-cocoa | Crash-Reporting (Backend: self-hosted Bugsink) |
| StoreKit 2 | Apple | (system framework) | In-App Purchases für Pro-Tier |
| swift-system | Apache 2.0 | https://github.com/apple/swift-system | Low-level POSIX wrappers |
| swift-collections | Apache 2.0 | https://github.com/apple/swift-collections | OrderedSet etc. für Pool-Logik |

## Verbotene Dependencies

Diese 3rd-party Libraries dürfen NICHT in den iOS-Bundle:

- **Original OpenVPN 2.x** (GPL-2.0 mit linking exception) — siehe oben
- **ics-openvpn** (Android-Port, GPL-2) — nicht für iOS portiert, und Lizenz-Problem
- **strongSwan** (GPL-2.0) — wir nutzen Apple's NEVPNManager + NEIKEv2 stattdessen
- **wg-quick** (GPL-2.0 shell script) — nutzen WireGuardKit's Swift API stattdessen
- Anything **GPL-2-only** ohne ausdrückliche "linking exception"
- **AGPL** anywhere — incompatible mit App-Store-EULA

## Compat-Layer-Code

Der iOS-spezifische **OpenVPN 2.x → 3.x Config-Preprocessor** in `Core/Sources/PrivycsCore/OVPNCompat/` ist Eigen-Code unter GPL-3 (Teil des Privycs-Clients). Strippt Direktiven die in OpenVPN3 nicht verfügbar sind (`script-security`, `up`/`down`, `plugin`) und normalisiert deprecated Optionen.

## Wenn Apple bei Review meckert

Falls App Store Review die GPL-3-Lizenz unseres eigenen Codes als Issue flaggt:

1. Verweis auf §7 "Additional Permissions": "*Du kannst diesem Programm-Code zusätzliche Berechtigungen hinzufügen, die für dich vorteilhaft sind*"
2. Verweis auf `EXCEPTIONS.md` (siehe unten): "*Hiermit gewährt Privycs allen Empfängern dieser App, die diese über den Apple App Store erhalten haben, zusätzlich die Erlaubnis, die App unter den Bedingungen der Apple App Store EULA zu nutzen, ungeachtet GPL-3 §6 die diese Bedingungen anderweitig einschränken könnte.*"
3. App Review hat in Praxis GPL-3-Apps mit Apache/MIT-Dependencies routinemäßig akzeptiert

## EXCEPTIONS.md (Template)

Bei Production-Release dieses Repo wird folgender Text in `EXCEPTIONS.md` aufgenommen:

```
Privycs VPN (iOS) — Additional Permission per GPL-3 §7

This program is licensed under the GNU General Public License version 3
(see LICENSE).

As a special exception under section 7 of the GPLv3, the copyright
holders of this Program grant you permission to convey the resulting
work as part of an Apple App Store distribution, subject to the
Apple App Store EULA, notwithstanding the GPLv3 §6 restrictions on
"further restrictions imposed by the recipient".

This additional permission is given by the sole copyright holder of
the Privycs VPN iOS Client, Privycs.

Effective: [release date]
```

## Audit-Trail

Bei jeder neuen Dependency:

1. Lizenz prüfen — `cat ${DEP}/LICENSE` oder Repo-Root
2. Diese Datei updaten — Tabelle ergänzen
3. Falls neue Lizenz-Klasse: Apple-Review-Risiko prüfen (Apache 2.0, MIT, BSD-2/3 = unbedenklich; GPL-2/3 only via §7-permission; AGPL = NEVER)
4. CI-Job validiert keine verbotenen Dependencies (`Scripts/check-licenses.sh`)
