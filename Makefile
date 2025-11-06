BINARY := relay-tunnel
PKG := ./cmd/relay-tunnel
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test vet fmt lint tidy clean run-server

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

test:
	go test -race -covermode=atomic ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

lint:
	golangci-lint run

tidy:
	go mod tidy

clean:
	rm -f $(BINARY) $(BINARY).exe rt.exe
	rm -rf dist

run-server:
	go run $(PKG) server -v
