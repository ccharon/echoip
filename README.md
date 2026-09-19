# echoip
![Build Status](https://github.com/ccharon/echoip/workflows/ci/badge.svg)
[![Docker pulls](https://img.shields.io/docker/pulls/ccharon/echoip.svg?label=docker+pulls)](https://hub.docker.com/r/ccharon/echoip)
[![Docker stars](https://img.shields.io/docker/stars/ccharon/echoip.svg?label=docker+stars)](https://hub.docker.com/r/ccharon/echoip)

HTTP service that returns the caller's IP address, enriched with location and
ASN data from the MaxMind GeoLite2 databases. The response format follows the
`Accept` header and the user agent: plain text for CLI clients, JSON for
`application/json`, an HTML page for browsers.

Fork of https://github.com/leafcloudhq/echoip

![Screenshot](https://raw.githubusercontent.com/ccharon/echoip/master/doc/screenshot.jpg)

## Run

The GeoLite2 databases are not part of the image. The container downloads them
on first start and checks for new ones once a day. This needs a MaxMind license
key, available after free registration at
[maxmind.com](https://www.maxmind.com), and a writable volume to keep the
databases across restarts.

Put the key in a `.env` file next to `docker-compose.yml`:

```
GEOIP_LICENSE_KEY=your-key
```

```bash
docker compose up -d
```

The first start downloads about 80 MB before the server accepts requests.

```yaml
services:
  echoip:
    image: ccharon/echoip
    ports:
      - "127.0.0.1:8082:8080"
    environment:
      GEOIP_LICENSE_KEY: ${GEOIP_LICENSE_KEY:?set GEOIP_LICENSE_KEY in .env}
    container_name: echoip
    restart: unless-stopped
    volumes:
      - geodata:/opt/echoip/data
    networks:
      - internal

volumes:
  geodata: {}

networks:
  internal: {}
```

## Configure

| Name | Type | Default | Effect |
| --- | --- | --- | --- |
| `GEOIP_LICENSE_KEY` | environment | unset | MaxMind license key. Required when `-c` or `-a` is set, otherwise the server exits on start. |
| `-a` | string | unset | Path to the GeoIP ASN database |
| `-c` | string | unset | Path to the GeoIP city database |
| `-u` | duration | `24h` | Interval for checking MaxMind for new databases. `0` disables checking. |
| `-l` | string | `:8080` | Listening address |
| `-t` | string | `html` | Path to the template directory |
| `-H` | string | unset | Header to trust for the remote IP, e.g. `X-Real-IP`. May be repeated. |
| `-r` | bool | `false` | Perform reverse hostname lookups |
| `-p` | bool | `false` | Enable port lookup |
| `-C` | int | `0` | Size of the response cache. `0` disables caching. |
| `-P` | bool | `false` | Enable profiling handlers |

The license key is read from the environment rather than a flag, because flags
are visible in the process list.

`-c` and `-a` also tell the updater where to write. The file named by `-c`
receives the GeoLite2-City edition, the file named by `-a` GeoLite2-ASN.

A missing database does not stop the server. It starts, answers `/`, `/ip` and
`/json` without geo data, and downloads the databases in the background.
`/country`, `/country-iso`, `/city` and `/coordinates` answer `404` until the
city database is in place, `/asn` until the ASN database is. They start working
without a restart.

## Refresh interval

MaxMind rebuilds GeoLite2 twice a week. `-u` defaults to `24h`, so the data is
at most a day behind the source. The two week interval this project used before
left it up to two weeks behind.

Checking that often costs nothing while the edition is unchanged, because the
request is conditional. The server sends `If-Modified-Since` with the
modification time of the file it holds, and MaxMind answers `304` with an empty
body until it has rebuilt that edition. Data is transferred about twice a week,
roughly 80 MB for both editions, which is what the long interval used to save.

Two consequences follow from that. The modification time on disk means "last
confirmed current" rather than "last downloaded", because a `304` updates it,
and that is what moves the next check a full interval away. An unchanged
database also triggers no reload, so the response cache survives a check that
brought nothing new.

## Security

The service answers unauthenticated requests from anyone, so the surface it
offers is what matters.

| Measure | Effect |
| --- | --- |
| `/port/` only accepts routable targets | Loopback, private, link local, multicast and carrier grade NAT addresses are refused, so a caller who controls the trusted header cannot aim the check at the network around the service. |
| Security headers on every response | `Content-Security-Policy`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`. |
| Content Security Policy by hash | Inline script and style are allowed by their SHA-256 hash rather than by `unsafe-inline`, so an injected script is refused. The hashes are taken from the rendered page at startup. |
| Database downloads are verified | Each archive is checked against the SHA-256 checksum MaxMind publishes for it, and only moved into place when it matches. |
| The container runs as an unprivileged user | UID 65532, with a read-only root filesystem, no capabilities and `no-new-privileges`. |
| The license key never reaches a log | It is read from the environment, and errors are stripped of the request URL that carries it. |

`-P` registers pprof and cache handlers below `/debug`. They are
unauthenticated and expose memory contents, so the listening address must not
be reachable from the internet while they are on.

The trusted headers from `-H` decide which address the service reports and
checks. Set them only for headers the proxy in front of the service
overwrites, otherwise a caller can choose the address they are treated as.

`make vulncheck` runs govulncheck against the module.

## Nginx configuration

You can run this server with your own domain.

```
  map $http_upgrade $connection_upgrade {
    default upgrade;
    '' close;
  }
    
    upstream echoip {
    server localhost:8082;
  }
    
    server {
    listen 443 ssl;
    server_name echoip.yoursite.com;

    location / {
    proxy_set_header  Host $host;
    proxy_set_header  X-Real-IP $remote_addr;
    proxy_set_header  X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header  X-Forwarded-Proto $scheme;
    proxy_pass  http://echoip;
    }

    ssl_certificate /etc/letsencrypt/live/yoursite.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/yoursite.com/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;
  }
    
    server {
    listen 80;
    server_name echoip.yoursite.com;

    if ($host = echoip.yoursite.com) {
    return 301 https://$host$request_uri;
    }

    return 404;
  }
```

## Usage

```
$ curl -L echoip.yoursite.com
127.0.0.1
```

Pass the appropriate flag (usually `-4` and `-6`) to your client to switch
between IPv4 and IPv6 lookup.

### Country and city lookup:

```
$ curl -L echoip.yoursite.com/country
Elbonia

$ curl -L echoip.yoursite.com/country-iso
EB

$ curl -L echoip.yoursite.com/city
Bornyasherk

$ curl -L echoip.yoursite.com/asn
AS59795
```

### As JSON:

```
$ curl -L -H 'Accept: application/json' echoip.yoursite.com  # or curl -L echoip.yoursite.com/json
{
  "city": "Bornyasherk",
  "country": "Elbonia",
  "country_iso": "EB",
  "ip": "127.0.0.1",
  "ip_decimal": 2130706433,
  "asn": "AS59795",
  "asn_org": "Hosting4Real"
}
```

### Port testing:

```
$ curl -L echoip.yoursite.com/port/80
{
  "ip": "127.0.0.1",
  "port": 80,
  "reachable": false
}
```

## Development

```bash
make lint test
make geoip-download   # needs GEOIP_LICENSE_KEY
make run              # needs GEOIP_LICENSE_KEY
```

## Limitations

- The refresh runs in process. A container that is restarted more often than
  the refresh interval downloads the databases again whenever the volume is
  empty.
- The next check is due when the files reach the age set by `-u`, counted from
  when they were last confirmed current, so a restart does not delay it.
- A failed check is logged and retried after 15 minutes. The previously
  downloaded databases stay in use.
- A database that is damaged after it was written goes unnoticed, because the
  conditional request still answers `304`. Delete the file to force a download.
- `SIGTERM` and `SIGINT` stop the listener and give running requests up to 10
  seconds to finish.
