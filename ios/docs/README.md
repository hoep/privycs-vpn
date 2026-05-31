# iOS docs

Internal setup + operational docs leben **nicht** in diesem
public Repo, sondern in `privycs-vpn-private-docs/` (lokales
internes Verzeichnis am Dev-Host):

- `ios-apple-setup.md` — Apple Developer Console Setup-Checklist
  (Bundle-IDs, Network Extension Entitlement-Antrag, App Store
  Connect, TestFlight, Provisioning Profiles, CI-Secrets-Liste)

Falls du als Dev hier landest und das Dokument brauchst:

- Privycs-Team-internal: privycs-vpn-private-docs/ (Verzeichnis
  auf dem Dev-Host, sync via dem Mechanismus aus dem privycs
  Meta-Repo).
- Externe Contributors: nicht relevant für PRs an dieses Repo
  — der App-Code hier funktioniert ohne Apple-Account-spezifische
  Setup-Schritte.

Public iOS-Architektur-Doku → `ios/README.md`
Lizenz-Strategie → `ios/LICENSE_NOTES.md`
