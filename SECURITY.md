# Security Policy

Privycs VPN is a VPN client — security reports are taken seriously and
handled with priority.

## Reporting a vulnerability

**Please do not open a public issue for security vulnerabilities.**

Report privately via GitHub's **[Report a vulnerability](https://github.com/hoep/privycs-vpn/security/advisories/new)**
feature (the *Security* tab → *Advisories* → *Report a vulnerability*).
This keeps the report confidential until a fix is available.

Please include:

- the affected platform (Android / Windows / macOS / Linux) and the app version
- a description of the issue and its security impact
- steps to reproduce, ideally with a minimal example
- any relevant logs (Settings → View Logs), with secrets redacted

We aim to acknowledge a report within a few days and to keep you
updated on remediation progress. Responsible disclosure is
appreciated — please give us a reasonable window to ship a fix before
any public discussion.

## Scope

**In scope** — the Privycs VPN client apps in this repository: tunnel
setup and teardown, the kill switch, at-rest encryption of stored
configs and credentials, the desktop privileged helper and its
privilege boundary, and config-import parsing.

**Out of scope** — vulnerabilities in the bundled upstream projects
(WireGuard, ics-openvpn, strongSwan) should be reported to those
projects directly. Issues in a VPN *server* you connect to are not
part of this client.

## Supported versions

Security fixes are released against the latest version. Always run
the most recent release.
