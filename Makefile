DOCKER ?= docker
DOCKER_IMAGE ?= ccharon/echoip
OS := $(shell uname)
ifeq ($(OS),Linux)
	TAR_OPTS := --wildcards
endif
all: lint test install

test:
	go test ./...

vet:
	go vet ./...

check-fmt:
	bash -c "diff --line-format='%L' <(echo -n) <(gofmt -d -s .)"

lint: check-fmt vet

vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

install:
	go install ./...

databases := GeoLite2-City GeoLite2-ASN

$(databases):
ifndef GEOIP_LICENSE_KEY
	$(error GEOIP_LICENSE_KEY must be set. Please see https://blog.maxmind.com/2019/12/18/significant-changes-to-accessing-and-using-geolite2-databases/)
endif
	mkdir -p data
	@curl -fsSL -m 30 "https://download.maxmind.com/app/geoip_download?edition_id=$@&license_key=$(GEOIP_LICENSE_KEY)&suffix=tar.gz" | tar $(TAR_OPTS) --strip-components=1 -C $(CURDIR)/data -xzf - '*.mmdb'

geoip-download: $(databases)

# The classic builder does not fill BUILDPLATFORM in, and an empty value is
# rejected.
BUILDPLATFORM ?= $(shell $(DOCKER) version -f '{{.Server.Os}}/{{.Server.Arch}}')

docker-build:
	$(DOCKER) build --build-arg BUILDPLATFORM=$(BUILDPLATFORM) -t $(DOCKER_IMAGE) .

docker-login:
	$(DOCKER) login --username "$(DOCKER_USERNAME)" --password "$(DOCKER_PASSWORD)"

docker-test:
	$(eval CONTAINER=$(shell $(DOCKER) run --rm --detach --env GEOIP_LICENSE_KEY --volume ./data:/opt/echoip/data --publish-all $(DOCKER_IMAGE)))
	$(eval DOCKER_PORT=$(shell $(DOCKER) port $(CONTAINER) | cut -d ":" -f 2))
	curl -fsS -m 5 localhost:$(DOCKER_PORT) > /dev/null; $(DOCKER) stop $(CONTAINER)

docker-push: docker-test docker-login
	$(DOCKER) push $(DOCKER_IMAGE)

docker-run:
	$(DOCKER) run --env GEOIP_LICENSE_KEY --volume ./data:/opt/echoip/data --publish 127.0.0.1:8080:8080 $(DOCKER_IMAGE)

run:
	go run ./cmd/echoip -a data/GeoLite2-ASN.mmdb -c data/GeoLite2-City.mmdb -r -C 1000 -H X-Real-IP
