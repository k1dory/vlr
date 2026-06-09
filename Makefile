.PHONY: build test vet lint install clean run-main run-node

BIN := vlr
PKG := ./cmd/vlr

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed, skipping"

install:
	./install.sh

# cross-compile for a Linux RU/EU node from a dev box
build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN)-linux-amd64 $(PKG)

clean:
	rm -f $(BIN) $(BIN)-linux-amd64
