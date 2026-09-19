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
on first start and refreshes them every 14 days. This needs a MaxMind license
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
| `-u` | duration | `336h` | Interval for refreshing the databases. `0` disables refreshing. |
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
- MaxMind publishes GeoLite2 updates twice a week. A 14 day interval means the
  data can be up to two weeks behind.
- A failed refresh is logged and retried after 15 minutes. The previously
  downloaded databases stay in use.
- `SIGTERM` and `SIGINT` stop the listener and give running requests up to 10
  seconds to finish.
