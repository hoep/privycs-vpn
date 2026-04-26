# GeoIP MMDB Asset

This directory ships the IP-to-country MMDB database used by the Pool
feature for resolving endpoint IPs to ISO 3166-1 alpha-2 country codes.

## File

`Country.mmdb` - MaxMind-format binary, ~6 MB.

The file is **not committed** to the repo to keep clones cheap. The Wails
build process fetches it as a step before bundling.

## Source

We use [sapics/ip-location-db](https://github.com/sapics/ip-location-db)'s
`geolite2-country-mmdb` distribution. License: CC-BY-4.0 / MIT permissive
mix - free to redistribute in commercial software with attribution in the
About screen.

Direct URL (latest):
```
https://github.com/sapics/ip-location-db/raw/main/geolite2-country-mmdb/geolite2-country-ipv4.mmdb
```

## Fetch Script

```bash
curl -fL -o assets/geoip/Country.mmdb \
  https://github.com/sapics/ip-location-db/raw/main/geolite2-country-mmdb/geolite2-country-ipv4.mmdb
```

The Wails build manifest invokes this before `wails build`. CI does the
same. For local development:

```bash
cd desktop && bash scripts/fetch-geoip.sh
```

## Updates

The upstream DB is regenerated weekly. We do not auto-update at runtime
- the user updates by upgrading the app. App-update cadence (~monthly) is
the practical refresh interval.

## Override

Operators can point at an alternative MMDB via the `PRIVYCS_GEOIP_DB`
environment variable. Useful for air-gapped deployments shipping their
own commercial MaxMind license.

## Attribution

Per CC-BY-4.0, the About screen of the desktop app must include:

> IP-to-country data: GeoLite2 Country © MaxMind, distributed via
> sapics/ip-location-db (CC-BY-4.0).
