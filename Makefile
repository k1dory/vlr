.PHONY: build test vet lint install clean build-linux dist

BIN     := vlr
PKG     := ./cmd/vlr
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
ARCHES  := amd64 arm64

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed, skipping"

install:
	./install.sh --from-source

# cross-compile for a Linux RU/EU node from a dev box
build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN)-linux-amd64 $(PKG)

# Release bundles (what install.sh downloads): dist/vlr_<ver>_linux_<arch>.tar.gz
# each holding vlr + vlr.service + env.example, plus dist/SHA256SUMS and install.sh.
dist:
	rm -rf dist && mkdir -p dist
	set -e; for arch in $(ARCHES); do \
	  d=dist/vlr_$(VERSION)_linux_$$arch; mkdir -p $$d; \
	  GOOS=linux GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $$d/vlr $(PKG); \
	  cp deploy/systemd/vlr.service env.example $$d/; \
	  tar --no-xattrs -C $$d -czf $$d.tar.gz vlr vlr.service env.example; rm -rf $$d; \
	done
	cp install.sh dist/install.sh
	cd dist && sha256sum vlr_*.tar.gz install.sh > SHA256SUMS

clean:
	rm -rf $(BIN) $(BIN)-linux-amd64 dist
