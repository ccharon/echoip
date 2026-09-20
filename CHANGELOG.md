# Changelog

## 2.0.2 - Unreleased

### Breaking

- `-u` counts whole hours instead of taking a Go duration. `-u 24h` is now `-u 24`. MaxMind rebuilds GeoLite2 twice a week and limits downloads per day, so nothing below an hour is useful.
- `MAXMIND_ACCOUNT_ID` is required beside `GEOIP_LICENSE_KEY`. Downloads use the current MaxMind endpoint, which authenticates with HTTP basic auth instead of a license key in the query string.

### Changed

- The response cache evicts the entry that was read longest ago instead of the one inserted first. A client that keeps asking stays cached however many one-time visitors pass through. A read takes the write lock now, which costs a map lookup and a pointer swap.
- The MaxMind credentials are required only when a database is configured and `-u` is not `0`. A server that is handed its database files and never checks for new ones asks for neither.

### Fixed

- `country_eu` follows the country of the address alone. The registered country says where the block is registered, not where it is used, so a block registered in the EU and used elsewhere reported `true`.

## 2.0.1 - 2026-09-20

### Breaking

- `-t` is gone. The browser page is embedded in the binary, so the image no longer carries an `html/` directory and the templates moved to `http/html`.
- `-version` is `-V`, the spelling curl, ssh and grep use, which leaves `-v` free of a verbose reading.
- An address supplied through `?ip=` or a trusted header has to be globally reachable. Anything else is answered with 400. The peer address is unaffected, so a private caller is still answered for its own address.
- `-T` refuses a network with host bits set instead of masking it off, and refuses an IPv4-mapped prefix, which never matched an address the server had unmapped. `-T 10.1.2.3/8` now exits and names `10.0.0.0/8`.
- `country_eu` is reported only when a country was found. A `*bool` pointing at false is not dropped by `omitempty`, so it was the one geo field that showed up for an address the database does not know.

### Added

- Every refused or failed request is logged with the time, the peer address, the method, the target and the reason. The target and the reason are quoted and cut at 128 characters, so a newline in `?ip=` cannot forge a line. Successful requests stay out of the log, which the proxy records.

### Changed

- `HEAD` reaches the handlers registered for `GET`, so monitoring that uses it on `/health` no longer sees a 404.
- The MaxMind client refuses a redirect that changes the scheme of the first request. The license key is a query parameter, which a redirect carries to whoever answers.
- Building needs Go 1.27.1, the version the image is built with, so a local check evaluates the standard library that ships.

### Fixed

- The request line on the page keeps `?ip=` when the page was opened for another address. It was built from the input field alone, which starts empty.

### Documentation

- The README follows the structure it documents: what it is, how to run it, how to configure it, known limitations. The nginx configuration moved to `doc/nginx.md`, the endpoints are a table, and the explanations of how the refresh and the address check work are gone from it.

## 2.0.0 - 2026-09-19

Changes since the fork of [leafcloudhq/echoip](https://github.com/leafcloudhq/echoip).

### Breaking

- The port check is gone, with it the `/port` endpoints, the `-p` flag and the port field on the page. It was the only place where the service opened a connection to an address a caller supplied.
- `GEOIP_LICENSE_KEY` is required when `-c` or `-a` is set. The server exits on start without it.
- The module path is `github.com/ccharon/echoip`.
- The GeoLite2 databases are no longer part of the image. They are downloaded into a volume under `data/` on first start.
- The default refresh interval (`-u`) is 24 hours instead of two weeks.

### Added

- `-T` limits the headers from `-H` to peers inside the given networks. Without it, the headers are believed from any caller, as before.
- `-version` prints the version, which CI and the Makefile stamp in through `ldflags`.
- Reverse lookup (`-r`) and the response cache are enabled by default.
- Security headers and a CSP that allows inline script and style by SHA-256 hash, taken from the page rendered at startup.
- Downloads are verified against the SHA-256 checksum MaxMind publishes beside the archive.
- Tests for `cmd/echoip`, `maxmind`, `iputil/geo`, `http/request`, `http/security` and `http/server`.

### Changed

- Database updates use `If-Modified-Since`, so a check costs nothing while the edition is unchanged. Only a real download triggers a reload and clears the cache.
- The refresh is timed from the age of the databases, not from process start.
- A missing or damaged database no longer stops the server. `/country`, `/country-iso`, `/city` and `/coordinates` answer 404 until the file is in place, `/asn` likewise, and both start answering without a restart.
- `Reload` swaps the city and ASN databases separately, so one broken file keeps the other in use.
- The `Accept` header is parsed with `mime.ParseMediaType` instead of compared for equality, which fixes clients sending `application/json, text/plain, */*`.
- Addresses are carried as `netip.Addr`, which removes the cache's own hashing and the 4 versus 16 byte mismatch.
- The listener has read, write and idle timeouts and shuts down on `SIGTERM` or `SIGINT`, giving running requests 10 seconds.
- The reverse lookup runs against a two second deadline.
- `http.go` is split into `server.go`, `handler.go`, `response.go`, `request.go` and `security.go`. Options live in `http.Config`.
- The browser page was rebuilt. It carries its own CSS, loads nothing from a CDN except the map, and its script is a strict-mode IIFE bound through `addEventListener`.
- The container runs as UID 65532 with a read-only root filesystem and no capabilities.
- The image cross compiles instead of building the arm64 half under QEMU.
- CI runs `make lint test` and `make vulncheck` before building, and no longer pushes on a pull request.
- The nginx configuration in the README was rewritten: HTTP/2, HSTS, timeouts, a rate limit on the lookup path and a default server answering 444.

### Fixed

- Cache races on `Resize` and `Set`, a key that was evicted although the cache did not grow, and entries that outlived a `Resize`.
- The index template is executed by name, so a file added to `html/` cannot take over the page.

### Documentation

- The fork's copyright line sits beside the original in `LICENSE`, and the README names both upstream repositories and the GeoLite2 attribution.
