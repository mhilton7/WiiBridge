SHELL := /bin/bash
VERSION := 0.1.0-rc.1
BUILD_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_DIRTY := $(shell [ -z "$$(git status --porcelain --untracked-files=normal 2>/dev/null)" ] && echo false || echo true)
HOST_LDFLAGS := -s -w -buildid= -X main.gitCommit=$(BUILD_COMMIT) -X main.buildTime=$(BUILD_TIME) -X main.buildDirty=$(BUILD_DIRTY)
export GOCACHE ?= /tmp/wiibridge-go-cache
export GOPATH ?= /tmp/wiibridge-gopath

.PHONY: all test static server oci compose firmware-zero-w firmware-pi4 firmware-pi5 firmware-all validate-firmware release

all: test server

test:
	go test ./server/... ./pi/... ./shared/... ./tests/... \
	  ./scripts/synthetic-wbfs ./scripts/gamecube-volume ./scripts/gamecube-saves
	./tests/truenas/runtime-identity-test.sh
	./tests/security/public-tree-privacy-test.sh

static: server
	go vet ./server/... ./pi/... ./shared/... ./tests/... \
	  ./scripts/synthetic-wbfs ./scripts/gamecube-volume ./scripts/gamecube-saves
	shellcheck scripts/*.sh deploy/truenas/*.sh tests/truenas/*.sh tests/firmware/offline/*.sh tests/security/*.sh
	./scripts/validate-pi-static.sh

server:
	mkdir -p build/bin
	CGO_ENABLED=0 go build -trimpath -ldflags="$(HOST_LDFLAGS)" -o build/bin/wiibridge-host ./server/host-daemon

oci:
	./scripts/build-oci.sh
	./scripts/package-server.sh

compose:
	./deploy/truenas/validate-compose.sh
	./deploy/truenas/validate-compose.sh deploy/truenas/compose.separate-libraries.yaml
	./deploy/truenas/validate-compose.sh deploy/truenas/compose.ghcr.yaml

firmware-zero-w:
	./scripts/build-firmware.sh zero-w-armhf
	./scripts/package-firmware.sh zero-w-armhf

firmware-pi4:
	./scripts/build-firmware.sh pi4-arm64
	./scripts/package-firmware.sh pi4-arm64

firmware-pi5:
	./scripts/build-firmware.sh pi5-arm64
	./scripts/package-firmware.sh pi5-arm64

firmware-all: firmware-zero-w firmware-pi4 firmware-pi5

validate-firmware:
	./tests/firmware/offline/validate.sh zero-w-armhf
	./tests/firmware/offline/validate.sh pi4-arm64
	./tests/firmware/offline/validate.sh pi5-arm64

release: test static server oci compose firmware-all validate-firmware
