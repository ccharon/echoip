DOCKER ?= docker
DOCKER_IMAGE ?= ccharon/echoip
OS := $(shell uname)
ifeq ($(OS),Linux)
	TAR_OPTS := --wildcards
endif
all: lint test install

test:
	go test ./...

# Needs cgo and is slower, so CI runs it and make test stays quick.
test-race:
	go test -race ./...

vet:
	go vet ./...

check-fmt:
	bash -c "diff --line-format='%L' <(echo -n) <(gofmt -d -s .)"

lint: check-fmt vet

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

install:
	go install -ldflags "-X main.version=$(VERSION)" ./...

databases := GeoLite2-City GeoLite2-ASN

$(databases):
ifndef MAXMIND_ACCOUNT_ID
	$(error MAXMIND_ACCOUNT_ID must be set. Please see https://dev.maxmind.com/geoip/updating-databases/)
endif
ifndef GEOIP_LICENSE_KEY
	$(error GEOIP_LICENSE_KEY must be set. Please see https://dev.maxmind.com/geoip/updating-databases/)
endif
	mkdir -p data
	@curl -fsSL -m 30 -u "$(MAXMIND_ACCOUNT_ID):$(GEOIP_LICENSE_KEY)" "https://download.maxmind.com/geoip/databases/$@/download?suffix=tar.gz" | tar $(TAR_OPTS) --strip-components=1 -C $(CURDIR)/data -xzf - '*.mmdb'

geoip-download: $(databases)

# The classic builder does not fill BUILDPLATFORM in, and an empty value is
# rejected.
BUILDPLATFORM ?= $(shell $(DOCKER) version -f '{{.Server.Os}}/{{.Server.Arch}}')

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

docker-build:
	$(DOCKER) build --build-arg BUILDPLATFORM=$(BUILDPLATFORM) --build-arg VERSION=$(VERSION) -t $(DOCKER_IMAGE) .

# The password comes from the environment and goes in through stdin, so it is
# not part of any command line.
docker-login:
	@printf '%s' "$$DOCKER_PASSWORD" | $(DOCKER) login --username "$(DOCKER_USERNAME)" --password-stdin

# One shell block, so the container is stopped whatever the probe says and the
# target fails when the service does not answer. Without credentials the binary
# exits at startup, which the loop notices instead of waiting out the limit.
docker-test:
	@set -u; \
	id=$$($(DOCKER) run --rm --detach --env MAXMIND_ACCOUNT_ID --env GEOIP_LICENSE_KEY --volume ./data:/opt/echoip/data --publish-all $(DOCKER_IMAGE)) || exit 1; \
	ok=1; \
	for i in $$(seq 1 30); do \
		$(DOCKER) inspect -f '{{.State.Running}}' "$$id" 2>/dev/null | grep -q true || { echo "container exited"; $(DOCKER) logs "$$id" 2>&1 | tail -n 5; break; }; \
		port=$$($(DOCKER) port "$$id" 8080/tcp 2>/dev/null | head -n 1 | cut -d: -f2); \
		if [ -n "$$port" ] && curl -fsS -m 2 "localhost:$$port/health" > /dev/null 2>&1; then ok=0; break; fi; \
		sleep 1; \
	done; \
	$(DOCKER) stop "$$id" > /dev/null 2>&1 || true; \
	[ $$ok -eq 0 ] || echo "service did not answer on /health"; \
	exit $$ok

docker-push: docker-test docker-login
	$(DOCKER) push $(DOCKER_IMAGE)

docker-run:
	$(DOCKER) run --env MAXMIND_ACCOUNT_ID --env GEOIP_LICENSE_KEY --volume ./data:/opt/echoip/data --publish 127.0.0.1:8080:8080 $(DOCKER_IMAGE)

run:
	go run ./cmd/echoip -a data/GeoLite2-ASN.mmdb -c data/GeoLite2-City.mmdb -r -C 1000 -H X-Real-IP
