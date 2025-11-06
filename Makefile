BINARY := relay-tunnel
PKG := ./cmd/relay-tunnel
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build web examples test vet fmt lint tidy clean run-server

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

# Exports the admin web interface into internal/webui/out, where the Go build
# embeds it. A binary built without this serves the API alone.
web:
	./build.sh --web

# Builds every program under examples/ for this machine.
examples:
	./build.sh --examples

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
	rm -f relay-sync relay-sync.exe
	rm -rf dist web/out web/.next
	rm -rf internal/webui/out
	mkdir -p internal/webui/out && touch internal/webui/out/.gitkeep

run-server:
	go run $(PKG) server -v
