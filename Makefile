BIN     ?= $(HOME)/.local/bin/conductor
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build install test vet

build:
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o conductor ./cmd/conductor

install:
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/conductor
	@echo "installed $(BIN)"

test:
	go test ./...

vet:
	go vet ./...
