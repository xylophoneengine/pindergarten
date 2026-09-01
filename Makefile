BIN=pindergarten
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/pindergarten

test:
	go test ./...

lint:
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	golangci-lint run ./...

init:
	git config core.hooksPath .githooks

release:
	podman build -f Containerfile.builder -t pindergarten-builder .
	podman run --rm -v $$PWD:/src:Z pindergarten-builder

.PHONY: build test lint init release
