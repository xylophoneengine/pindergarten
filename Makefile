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

screenshots:
	rm -rf .screenshots-ans
	PINDERGARTEN_SCREENSHOT_DIR=$$PWD/.screenshots-ans go test ./internal/tui/ -run TestWriteScreenshots -count=1
	python3 tools/screenshots/render.py .screenshots-ans docs/screenshots
	rm -rf .screenshots-ans

release:
	podman build -f Containerfile.builder -t pindergarten-builder .
	podman run --rm -v $$PWD:/src:Z -e VERSION=$(VERSION) pindergarten-builder

.PHONY: build test lint init screenshots release
