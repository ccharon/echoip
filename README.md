# echoip
Fork of https://github.com/leafcloudhq/echoip

## Build the container

before building this container you have to download your own geoip databases from maxmind.com. A free registration ist required.
You will receive a license key which needs to be set as an environment variable, then execute the make target.

```bash
export GEOIP_LICENSE_KEY="your_license_key"
make geoip-download
```

after that you can use docker build

```bash
make docker-build
```

## Run

```bash
docker run -d --name echoip -p 8080:8080 echoip
```

## Nginx configuration

You can run this server with your own domain.

```
server {
    listen 80;
    listen [::]:80;
    ...
    location / {
        if ($http_user_agent !~* (curl|wget)) {
            return 301 https://$server_name$request_uri;
        }
        proxy_set_header  Host $host;
		proxy_set_header  X-Real-IP $remote_addr;
		proxy_set_header  X-Forwarded-For $proxy_add_x_forwarded_for;
		proxy_set_header  X-Forwarded-Proto $scheme;
		proxy_pass  http://localhost:8080;
    }
}
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    ssl_certificate /path/to/ssl/chain.pem; 
    ssl_certificate_key /path/to/ssl/private.key;
    ...
    location / {
        proxy_set_header  Host $host;
		proxy_set_header  X-Real-IP $remote_addr;
		proxy_set_header  X-Forwarded-For $proxy_add_x_forwarded_for;
		proxy_set_header  X-Forwarded-Proto $scheme;
		proxy_pass  http://localhost:8080;
    }
}
```

## Usage

```
$ curl ifconfig
127.0.0.1

$ http ifconfig.co
127.0.0.1

$ wget -qO- ifconfig.co
127.0.0.1

$ fetch -qo- https://ifconfig.co
127.0.0.1

$ bat -print=b ifconfig.co/ip
127.0.0.1
```

Country and city lookup:

```
$ curl ifconfig.co/country
Elbonia

$ curl ifconfig.co/country-iso
EB

$ curl ifconfig.co/city
Bornyasherk

$ curl ifconfig.co/asn
AS59795
```

As JSON:

```
$ curl -H 'Accept: application/json' ifconfig.co  # or curl ifconfig.co/json
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
$ curl ifconfig.co/port/80
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
  -f string
    	Path to GeoIP country database
  -l string
    	Listening address (default ":8080")
  -p	Enable port lookup
  -r	Perform reverse hostname lookups
  -t string
    	Path to template directory (default "html")
```
