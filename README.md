# echoip
![Build Status](https://github.com/ccharon/echoip/workflows/ci/badge.svg)
[![Docker pulls](https://img.shields.io/docker/pulls/ccharon/echoip.svg?label=docker+pulls)](https://hub.docker.com/r/ccharon/echoip)
[![Docker stars](https://img.shields.io/docker/stars/ccharon/echoip.svg?label=docker+stars)](https://hub.docker.com/r/ccharon/echoip)



Fork of https://github.com/leafcloudhq/echoip

- refreshed dependencies
- removed country.mmdb as this information is also in city.mmdb
- added docker-compose.yml
- added nginx config
- modified docker image to run with geoip data
- modified page layout

![Screenshot](https://raw.githubusercontent.com/ccharon/echoip/master/doc/screenshot.jpg)

## Run
Before running this container you have to download your own geoip databases from [maxmind.com](https://www.maxmind.com). A free registration ist required.

The databases required are: GeoLite2-City.mmdb and GeoLite2-ASN.mmdb. Be sure to mount the data directory containing the geoip databases into your docker image.
See the docker-compose.yml

```yaml
version: "3"
services:
  echoip:
    image: ccharon/echoip
    ports:
      - "127.0.0.1:8082:8080"
    deploy:
      resources:
        limits:
          cpus: "0.10"
          memory: 128M
        reservations:
          cpus: "0.05"
          memory: 64M
    container_name: echoip
    restart: unless-stopped
    volumes:
      - ./data:/opt/echoip/data:ro
    networks:
      - internal

networks:
  internal: {}
```
to start run:
```bash
docker compose up -d 
```

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

$ http echoip.yoursite.com
127.0.0.1

$ wget -qO- echoip.yoursite.com
127.0.0.1

$ fetch -qo- https://echoip.yoursite.com
127.0.0.1

$ bat -print=b echoip.yoursite.com/ip
127.0.0.1
```

Country and city lookup:

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

As JSON:

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

Port testing:

```
$ curl -L echoip.yoursite.com/port/80
{
  "ip": "127.0.0.1",
  "port": 80,
  "reachable": false
}
```

Pass the appropriate flag (usually `-4` and `-6`) to your client to switch
between IPv4 and IPv6 lookup.


### Usage

```
$ echoip -h
Usage of echoip:
  -C int
    	Size of response cache. Set to 0 to disable
  -H value
    	Header to trust for remote IP, if present (e.g. X-Real-IP)
  -a string
    	Path to GeoIP ASN database
  -c string
    	Path to GeoIP city database
  -l string
    	Listening address (default ":8080")
  -p	Enable port lookup
  -r	Perform reverse hostname lookups
  -t string
    	Path to template directory (default "html")
```
