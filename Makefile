BINARY  := concord
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PKG     := ./cmd/concord

.PHONY: build test fmt vet verify run clean tidy

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) $(PKG)

test:
	go test -timeout 300s ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

verify: vet test build

run:
	CGO_ENABLED=0 go run $(PKG)

tidy:
	go mod tidy

clean:
	rm -rf bin
