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

## Configure

| Name | Type | Default | Effect |
| --- | --- | --- | --- |
| `GEOIP_LICENSE_KEY` | environment | none | MaxMind license key. Required when `-c` or `-a` is set, otherwise the server exits on start. |
| `-a` | string | none | Path to the GeoIP ASN database |
| `-c` | string | none | Path to the GeoIP city database |
| `-u` | duration | `24h` | Interval for checking MaxMind for new databases. `0` disables checking. |
| `-l` | string | `:8080` | Listening address. An empty host listens on all interfaces, IPv4 and IPv6. `0.0.0.0:8080` is IPv4 only, `127.0.0.1:8080` is loopback only. |
| `-H` | string, repeatable | none | Header to trust for the remote IP, e.g. `X-Real-IP` |
| `-T` | string, repeatable | any peer | Networks whose requests may set the headers from `-H`, e.g. `10.0.0.0/8` or a single address |
| `-r` | bool | `false` | Perform reverse hostname lookups |
| `-C` | int | `0` | Size of the response cache. `0` disables caching. |
| `-P` | bool | `false` | Register the pprof and cache handlers below `/debug` |

`-V` prints the version and exits. It configures nothing, so it is not in the
table.

The browser page is built into the binary. There is no flag to replace it.

The image sets `-c`, `-a`, `-r`, `-C 1000` and `-H X-Real-IP` in its
`ENTRYPOINT`, so the table describes the binary.

`-T` takes CIDR notation or a single address. A dotted netmask is not accepted,
and a network with host bits set is refused rather than widened, so
`-T 10.1.2.3/8` exits with a message naming `10.0.0.0/8`.

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
at most a day behind the source.

Checking daily costs nothing while the edition is unchanged, because the
request is conditional. The server sends `If-Modified-Since` with the
modification time of the file it holds, and MaxMind answers `304` with an empty
body until it has rebuilt that edition. Data is transferred about twice a week,
roughly 80 MB for both editions.

The modification time on disk therefore means "last confirmed current", since a
`304` updates it as well, and that is what moves the next check a full interval
away. An unchanged database triggers no reload, so the response cache survives
a check that brought nothing new.

## Security

The service answers unauthenticated requests from anyone.

| Measure | Effect |
| --- | --- |
| The service opens no outbound connections | It answers from the GeoIP databases only, so a caller cannot make it reach an address of their choosing. |
| Only public addresses are looked up | An address from `?ip=` or from a trusted header is refused with 400 unless it is globally reachable, so a caller cannot have the resolver queried for the network the service runs in. The peer address is always answered for. |
| Security headers on every response | `Content-Security-Policy`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`. |
| Content Security Policy by hash | Inline script and style are allowed by their SHA-256 hash rather than by `unsafe-inline`, so an injected script is refused. The hashes are taken from the rendered page at startup. |
| Database downloads are verified | Each archive is checked against the SHA-256 checksum MaxMind publishes for it, and only moved into place when it matches. |
| The container runs as an unprivileged user | UID 65532, with a read-only root filesystem, no capabilities and `no-new-privileges`. |
| The license key never reaches a log | It is read from the environment, and errors are stripped of the request URL that carries it. |
| Refused requests are logged | Every 4xx and 5xx is written with the peer address, the method, the target and the reason, so an attempt to probe the service leaves a trace. |

`-P` registers pprof and cache handlers below `/debug`. They are
unauthenticated, `/debug/pprof/heap` hands out memory contents, which include
the license key, and `POST /debug/cache/resize` changes the cache without
asking. The listening address must not be reachable from the internet while
they are on.

Successful requests are not logged, because the proxy in front already records
them. Refused and failed ones are, in one line each:

```
echoip: 203.0.113.9:54321 GET "/ip?ip=10.0.0.5" -> 400: not a public IP: 10.0.0.5
```

The address is the one the connection came from, not the one a header claims.

The trusted headers from `-H` decide which address the service reports. Set
them only for headers the proxy in front of the service overwrites, otherwise a
caller can choose the address they are shown data for. That address is only
looked up, never contacted, and it has to be public.

`?ip=` and the trusted headers accept globally reachable addresses only.
Loopback, RFC 1918, IPv6 unique local, carrier grade NAT, link local,
multicast, the documentation and benchmarking ranges and the reserved space are
answered with 400. This applies to what the caller supplies. A request whose own address is
private, from a LAN or from localhost, is answered for that address as before.

`?ip=` is read before the headers, so a local server behind a proxy is tested
by naming a public address:

```bash
curl 'localhost:8080/json?ip=1.2.3.4'
```

A plain request through that proxy carries the loopback address the proxy sees,
which is answered with 400.

`-T` narrows that to the networks the proxy connects from. A request that
arrives from anywhere else is answered for its own address, whatever headers it
carries. With the service bound to localhost, `-T 127.0.0.1` covers a proxy on
the same host.

`make vulncheck` runs govulncheck, which CI runs before it builds the image. It
fails the build when a known vulnerability is reachable from this code. One
that sits in a dependency nothing here calls is reported without failing.

Rate limiting is left to the proxy. The nginx configuration below sets it.

## Nginx configuration

The service speaks plain HTTP and binds to localhost, so it needs a proxy in
front of it for TLS. The proxy also decides which address the service reports,
because it sets the trusted header.

```nginx
# Rate limits. A page view and a CLI answer are cheap, a reverse lookup for an
# address the service has not seen before is not.
limit_req_zone $binary_remote_addr zone=echoip_req:10m rate=10r/s;
limit_conn_zone $binary_remote_addr zone=echoip_conn:10m;

upstream echoip {
    # The address the container publishes. Written out, because localhost may
    # resolve to ::1, where nothing is listening.
    server 127.0.0.1:8082;
    keepalive 16;
}

# Requests for a host this server does not serve never reach the service, so
# its pages cannot be shown under someone else's name.
server {
    listen 80 default_server;
    listen [::]:80 default_server;
    listen 443 ssl default_server;
    listen [::]:443 ssl default_server;
    server_name _;

    ssl_certificate /etc/letsencrypt/live/yoursite.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/yoursite.com/privkey.pem;

    return 444;
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name echoip.yoursite.com;

    ssl_certificate /etc/letsencrypt/live/yoursite.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/yoursite.com/privkey.pem;
    include /etc/letsencrypt/options-ssl-nginx.conf;
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;

    server_tokens off;
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;

    proxy_http_version 1.1;
    proxy_set_header Connection "";
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;

    # echoip trusts this header for the address it reports, so it must carry
    # the peer and never what the caller sent.
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $remote_addr;

    proxy_connect_timeout 5s;
    proxy_send_timeout 30s;
    proxy_read_timeout 30s;

    location / {
        limit_req zone=echoip_req burst=20 nodelay;
        limit_req_status 429;
        limit_conn echoip_conn 10;
        limit_conn_status 429;

        proxy_pass http://echoip;
    }

    # Monitoring should not be throttled or fill the log.
    location = /health {
        access_log off;
        proxy_pass http://echoip;
    }
}

server {
    listen 80;
    listen [::]:80;
    server_name echoip.yoursite.com;

    return 301 https://$host$request_uri;
}
```

The rate limit covers the one expensive path. A page view is served from
memory, while a reverse lookup for an address outside the cache waits on DNS,
up to two seconds when there is no PTR record. Ten requests per second with a
burst of twenty is far above what a browser or a CLI client does. `/health` is
exempt so that monitoring is never throttled, and a client over the limit gets
`429`.

`proxy_set_header X-Real-IP $remote_addr` replaces whatever the caller sent,
which is what makes the reported address trustworthy. Keep the service bound to
localhost, otherwise a caller can reach it directly and pick their own
address.

## Usage

```
$ curl -L echoip.yoursite.com
127.0.0.1
```

Pass the appropriate flag (usually `-4` and `-6`) to your client to switch
between IPv4 and IPv6 lookup.

`?ip=` looks up another address, which has to be a public one:

```
$ curl -L 'echoip.yoursite.com/country?ip=1.2.3.4'
Elbonia

$ curl -L 'echoip.yoursite.com/country?ip=10.0.0.5'
{
  "status": 400,
  "error": "not a public IP: 10.0.0.5"
}
```

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

## Release

```bash
git tag -a v1.0.0 -m "v1.0.0"
git push origin v1.0.0
```

CI builds the image again for the tag and publishes it as `1.0.0`, `1.0` and
`sha-<commit>`. A push to `master` publishes `latest`.

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

## License

BSD 3-Clause, see [LICENSE](LICENSE). Copyright is held by Martin Polden for the
original work and by Christian Charon for the changes in this fork. The commit
history of both upstream repositories is kept in this repository.

This product includes GeoLite2 data created by MaxMind, available from
[maxmind.com](https://www.maxmind.com). The databases are subject to the
[GeoLite2 End User License Agreement](https://www.maxmind.com/en/geolite2/eula)
and are not distributed with this repository or its image.
