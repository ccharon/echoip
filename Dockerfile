# Build
# Empty TARGET* values build for the builder itself.
ARG BUILDPLATFORM
# Pinned by digest so the same source builds the same image. The tag is kept
# beside it for readability and is not what resolves.
FROM --platform=$BUILDPLATFORM golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /go/src/github.com/ccharon/echoip

# Modules first, so this layer is reused until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Cross compiled from the builder platform, so nothing is emulated.
ARG TARGETOS TARGETARCH
ARG VERSION=dev

# Must build without cgo because libc is unavailable in runtime image
ENV CGO_ENABLED=0
# -trimpath keeps the builder paths out of the binary.
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-X main.version=$VERSION" -o /out/echoip ./cmd/echoip

# The runtime image has no shell to create the data directory, and the
# unprivileged user needs to own it.
RUN mkdir /data

# Run
FROM scratch
EXPOSE 8080

# Needed to reach the MaxMind download endpoint over TLS
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/echoip /opt/echoip/
COPY --from=build --chown=65532:65532 /data /opt/echoip/data

# Nothing here needs root. A named volume inherits the ownership of the
# directory it covers, so the databases stay writable.
USER 65532:65532

WORKDIR /opt/echoip
VOLUME /opt/echoip/data
ENTRYPOINT ["/opt/echoip/echoip", "-a", "data/GeoLite2-ASN.mmdb", "-c", "data/GeoLite2-City.mmdb", "-r", "-C", "1000", "-H", "X-Real-IP"]
