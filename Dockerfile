# Build
FROM golang:1.27.1-bookworm AS build
WORKDIR /go/src/github.com/ccharon/echoip
COPY . .

# Must build without cgo because libc is unavailable in runtime image
ENV CGO_ENABLED=0
RUN make

# Run
FROM scratch
EXPOSE 8080

# Needed to reach the MaxMind download endpoint over TLS
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /go/bin/echoip /opt/echoip/
COPY html /opt/echoip/html

WORKDIR /opt/echoip
VOLUME /opt/echoip/data
ENTRYPOINT ["/opt/echoip/echoip", "-a", "data/GeoLite2-ASN.mmdb", "-c", "data/GeoLite2-City.mmdb", "-p", "-H", "X-Real-IP"]
