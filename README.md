# echoip
![Build Status](https://github.com/ccharon/echoip/workflows/ci/badge.svg)
[![Docker pulls](https://img.shields.io/docker/pulls/ccharon/echoip.svg?label=docker+pulls)](https://hub.docker.com/r/ccharon/echoip)
[![Docker stars](https://img.shields.io/docker/stars/ccharon/echoip.svg?label=docker+stars)](https://hub.docker.com/r/ccharon/echoip)

HTTP service that returns the caller's IP address, enriched with location and
ASN data from the MaxMind GeoLite2 databases. The response format follows the
`Accept` header and the user agent: plain text for CLI clients, JSON for
`application/json`, an HTML page for browsers.

Fork of [leafcloudhq/echoip](https://github.com/leafcloudhq/echoip), which is
a fork of [mpolden/echoip](https://github.com/mpolden/echoip).

![Screenshot](https://raw.githubusercontent.com/ccharon/echoip/master/doc/screenshot.png)

## Run

The container downloads the GeoLite2 databases on first start and checks for
new ones once a day. They live in a mounted volume, which keeps them across
restarts and out of the image. This needs a MaxMind account, free after
registration at [maxmind.com](https://www.maxmind.com). Both the account ID and
a license key are required, because the download authenticates with them.

Put them in a `.env` file next to `docker-compose.yml`:

```
MAXMIND_ACCOUNT_ID=your-account-id
GEOIP_LICENSE_KEY=your-key
```

```bash
docker compose up -d
```

The first start downloads about 80 MB before the server accepts requests. The
server also runs without a database. It answers `/`, `/ip` and `/json` without
geo data, and the other endpoints answer `404` until their database is in
place.

```yaml
services:
  echoip:
    # Pinned to a minor, so a pull cannot cross a major version.
    image: ccharon/echoip:2.0
    ports:
      - "127.0.0.1:8082:8080"
    environment:
      MAXMIND_ACCOUNT_ID: ${MAXMIND_ACCOUNT_ID:?set MAXMIND_ACCOUNT_ID in .env}
      GEOIP_LICENSE_KEY: ${GEOIP_LICENSE_KEY:?set GEOIP_LICENSE_KEY in .env}
    deploy:
      resources:
        limits:
          cpus: "0.10"
          memory: 256M
        reservations:
          cpus: "0.05"
          memory: 128M
    container_name: echoip
    restart: unless-stopped
    read_only: true
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    volumes:
      - geodata:/opt/echoip/data
    networks:
      - internal

volumes:
  geodata: {}

networks:
  internal: {}
```

The service speaks plain HTTP and belongs behind a proxy that terminates TLS,
sets the trusted header and limits the request rate. A working configuration is
in [doc/nginx.md](https://raw.githubusercontent.com/ccharon/echoip/master/doc/nginx.md).

## Endpoints

| Path | Answer |
| --- | --- |
| `/` | the address for a CLI client, the browser page otherwise |
| `/ip` | the address |
| `/json` | every field, also served on `/` for `Accept: application/json` |
| `/country`, `/country-iso` | country name, ISO code |
| `/city`, `/coordinates` | city, latitude and longitude |
| `/asn` | AS number |
| `/health` | `{"status":"OK"}` |

```
$ curl echoip.example.com/country
Elbonia
```

```
$ curl echoip.example.com/json
{
  "ip": "203.0.113.9",
  "ip_decimal": 3405803785,
  "country": "Elbonia",
  "country_iso": "EB",
  "city": "Bornyasherk",
  "asn": "AS59795",
  "asn_org": "Hosting4Real"
}
```

`?ip=` answers for another address, which has to be a public one. Pass `-4` or
`-6` to the client to pick the address family.

## Configure

| Name | Type | Default | Effect |
| --- | --- | --- | --- |
| `MAXMIND_ACCOUNT_ID` | environment | none | MaxMind account ID. Required when a database is configured and `-u` is not `0`, otherwise the server exits on start. |
| `GEOIP_LICENSE_KEY` | environment | none | MaxMind license key. Required alongside the account ID. |
| `-a` | string | none | Path to the GeoIP ASN database |
| `-c` | string | none | Path to the GeoIP city database |
| `-u` | int | `24` | Hours between checks for new databases. `0` disables checking, and then no credentials are needed. |
| `-l` | string | `:8080` | Listening address. An empty host listens on all interfaces, IPv4 and IPv6. `0.0.0.0:8080` is IPv4 only, `127.0.0.1:8080` is loopback only. |
| `-H` | string, repeatable | none | Header to trust for the remote IP, e.g. `X-Real-IP` |
| `-T` | string, repeatable | any peer | Networks whose requests may set the headers from `-H`, e.g. `10.0.0.0/8` or a single address |
| `-r` | bool | `false` | Perform reverse hostname lookups |
| `-C` | int | `0` | Number of responses to cache. The entry read longest ago is dropped when it is full. `0` disables caching. |
| `-P` | bool | `false` | Register the pprof and cache handlers below `/debug` |

`-V` prints the version and exits. The image sets `-c`, `-a`, `-r`, `-C 1000`
and `-H X-Real-IP` in its `ENTRYPOINT`, so the table describes the binary.

`-c` and `-a` also tell the updater where to write. The credentials are read
from the environment rather than from flags, because flags are visible in the
process list. They are sent as HTTP basic auth, so no URL carries them.

`-u` counts whole hours. MaxMind rebuilds GeoLite2 twice a week and limits
downloads per day, so a finer interval buys nothing and a coarser one such as
`48` costs little.

`-u 0` leaves the database files to whoever put them there, which is what
`make geoip-download` or a volume filled from outside does. The browser page is built into the binary, so there is nothing to
point at a template directory.

`-T` takes CIDR notation or a single address. A network with host bits set is
refused rather than widened, so `-T 10.1.2.3/8` exits and names `10.0.0.0/8`.

## Security

The service answers unauthenticated requests from anyone.

| Measure | Effect |
| --- | --- |
| The service opens no outbound connections | It answers from the GeoIP databases only, so a caller cannot make it reach an address of their choosing. |
| Only public addresses are looked up | An address from `?ip=` or from a trusted header is refused with `400` unless it is globally reachable. The address the connection came from is always answered for. |
| Security headers on every response | `Content-Security-Policy`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`. |
| Content Security Policy by hash | Inline script and style are allowed by their SHA-256 hash rather than by `unsafe-inline`. |
| Database downloads are verified | Each archive is checked against the SHA-256 checksum MaxMind publishes for it. |
| Requests are bounded | Headers are capped at 8 KiB, the body of `/debug/cache/resize` at 32 bytes, and an error quotes at most 128 characters of what the caller sent. |
| The container runs as an unprivileged user | UID 65532, with a read-only root filesystem, no capabilities and `no-new-privileges`. |
| The credentials never reach a log | They are read from the environment and sent in an `Authorization` header, and errors are stripped of the request URL. |
| Refused requests are logged | Every 4xx and 5xx is written with the peer address, the method, the target and the reason, quoted and cut at 128 characters. |

Set `-H` only for headers the proxy overwrites, otherwise a caller picks the
address they are shown data for. `-T` narrows that to the networks the proxy
connects from.

`-P` registers handlers below `/debug` that are unauthenticated.
`/debug/pprof/heap` hands out memory contents, which include the credentials.
Keep the listening address unreachable from the internet while they are on.

## Limitations

- The refresh runs in process. A container that is restarted more often than
  the refresh interval downloads the databases again whenever the volume is
  empty.
- A failed check is logged and retried after 15 minutes. The previously
  downloaded databases stay in use.
- A database that is damaged after it was written goes unnoticed, because the
  conditional request still answers `304`. Delete the file to force a download.
- `SIGTERM` and `SIGINT` stop the listener and give running requests up to 10
  seconds to finish.
- The image carries the CA bundle of the build stage. A root that expires or a
  certificate chain that changes after the build breaks the MaxMind download
  until the image is rebuilt.

## Development

```bash
make lint test
make vulncheck
make run              # needs GEOIP_LICENSE_KEY
```

## Release

```bash
git tag -a v2.0.2 -m "v2.0.2"
git push origin v2.0.2
```

CI builds the image for the tag and publishes it as `2.0.2`, `2.0` and
`sha-<commit>`. A push to `master` publishes `latest`. Tag the commit on
`master`, because that is what the image is built from.

## License

BSD 3-Clause, see [LICENSE](LICENSE). Copyright is held by Martin Polden for the
original work and by Christian Charon for the changes in this fork.

This product includes GeoLite2 data created by MaxMind, available from
[maxmind.com](https://www.maxmind.com). The databases are subject to the
[GeoLite2 End User License Agreement](https://www.maxmind.com/en/geolite2/eula)
and are not distributed with this repository or its image.
