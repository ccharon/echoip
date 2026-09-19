# Changelog

## Unreleased

Changes since the fork of [leafcloudhq/echoip](https://github.com/leafcloudhq/echoip).

### Breaking

- The port check is gone, with it the `/port` endpoints, the `-p` flag and the port field on the page. It was the only place where the service opened a connection to an address a caller supplied.
- `GEOIP_LICENSE_KEY` is required when `-c` or `-a` is set. The server exits on start without it.
- The module path is `github.com/ccharon/echoip`.
- The GeoLite2 databases are no longer part of the image. They are downloaded into a volume under `data/` on first start.
- The default refresh interval (`-u`) is 24 hours instead of two weeks.
- `-t` is gone. The browser page is embedded in the binary, so the image no longer carries an `html/` directory and the templates moved to `http/html`.

### Added

- `-T` limits the headers from `-H` to peers inside the given networks. Without it, the headers are believed from any caller, as before.
- An address supplied through `?ip=` or a trusted header has to be globally reachable. Anything else is answered with 400. The peer address is unaffected, so a private caller is still answered for its own address.
- `-V` prints the version, which CI and the Makefile stamp in through `ldflags`.
- Reverse lookup (`-r`) and the response cache are enabled by default.
- Security headers and a CSP that allows inline script and style by SHA-256 hash, taken from the page rendered at startup.
- Downloads are verified against the SHA-256 checksum MaxMind publishes beside the archive.
- Tests for `cmd/echoip`, `maxmind`, `iputil/geo`, `http/request`, `http/security` and `http/server`.

### Changed

- Database updates use `If-Modified-Since`, so a check costs nothing while the edition is unchanged. Only a real download triggers a reload and clears the cache.
- The refresh is timed from the age of the databases, not from process start.
- A missing or damaged database no longer stops the server. `/country`, `/country-iso`, `/city` and `/coordinates` answer 404 until the file is in place, `/asn` likewise, and both start answering without a restart.
- `Reload` swaps the city and ASN databases separately, so one broken file keeps the other in use.
- `-T` refuses a network with host bits set instead of masking it off, and refuses an IPv4-mapped prefix, which never matched an address the server had unmapped. `-T 10.1.2.3/8` now exits and names `10.0.0.0/8`.
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
- The index template is executed by name, so a file added to `http/html/` cannot take over the page.
- The request line on the page keeps `?ip=` when the page was opened for another address. It was built from the input field alone, which starts empty.
- `country_eu` is reported only when a country was found. A `*bool` pointing at false is not dropped by `omitempty`, so it was the one geo field that showed up for an address the database does not know.

### Documentation

- The fork's copyright line sits beside the original in `LICENSE`, and the README names both upstream repositories and the GeoLite2 attribution.
