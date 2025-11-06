#!/usr/bin/env bash
# Build relay-tunnel. With no arguments it builds for the host into
# ./relay-tunnel; pass --all to cross-compile every release target into ./dist,
# which is what the GitHub Actions workflow does.
set -euo pipefail

cd "$(dirname "$0")"

pkg="./cmd/relay-tunnel"
version="$(git describe --tags --always 2>/dev/null || echo dev)"
ldflags="-s -w -X main.version=${version}"

build_one() {
  local goos="$1" goarch="$2" ext="${3:-}"
  local out="dist/relay-tunnel-${goos}-${goarch}${ext}"
  echo "  ${out}"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$ldflags" -o "$out" "$pkg"
}

case "${1:-}" in
  --all)
    echo "Building relay-tunnel ${version} for all targets"
    mkdir -p dist
    build_one windows amd64 .exe
    build_one windows arm64 .exe
    build_one linux   amd64
    build_one linux   arm64
    build_one darwin  amd64
    build_one darwin  arm64
    if command -v sha256sum >/dev/null 2>&1; then
      (cd dist && sha256sum relay-tunnel-* > SHA256SUMS)
      echo "Wrote dist/SHA256SUMS"
    fi
    ;;
  --test)
    gofmt -l .
    go vet ./...
    go test ./...
    ;;
  -h|--help)
    echo "Usage: ./build.sh [--all | --test | --help]"
    echo "  (no args)  build ./relay-tunnel for this machine"
    echo "  --all      cross-compile every release target into ./dist"
    echo "  --test     gofmt, go vet and go test"
    exit 0
    ;;
  "")
    echo "Building relay-tunnel ${version}"
    CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o relay-tunnel "$pkg"
    echo "Wrote ./relay-tunnel"
    ;;
  *)
    echo "unknown argument: $1 (try --help)" >&2
    exit 1
    ;;
esac
