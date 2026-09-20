# Nginx configuration

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
tens of milliseconds in the usual case and up to the two second deadline when
the resolver does not answer. Ten requests per second with a
burst of twenty is far above what a browser or a CLI client does. `/health` is
exempt so that monitoring is never throttled, and a client over the limit gets
`429`.

`proxy_set_header X-Real-IP $remote_addr` replaces whatever the caller sent,
which is what makes the reported address trustworthy. Keep the service bound to
localhost, otherwise a caller can reach it directly and pick their own
address.
