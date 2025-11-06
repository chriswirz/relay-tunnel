#!/usr/bin/env bash
# Build relay-tunnel. With no arguments it builds for the host into
# ./relay-tunnel; pass --all to cross-compile every release target into ./dist,
# which is what the GitHub Actions workflow does.
#
# The admin web interface is a separate build: ./build.sh --web exports the
# Next.js application in web/ into internal/webui/out, where the Go build embeds
# it. A binary built without that step runs fine and serves the API alone, so
# the frontend is only rebuilt when asked for, or as part of --all.
#
# Everything under examples/ is a program of its own, built alongside the CLI by
# --all (into dist/examples, which is what the release attaches) and on its own
# by --examples.
set -euo pipefail

cd "$(dirname "$0")"

pkg="./cmd/relay-tunnel"
version="$(git describe --tags --always 2>/dev/null || echo dev)"
ldflags="-s -w -X main.version=${version}"

# build_web exports the frontend into the package that embeds it. npm ci is used
# when there is a lockfile to honour, which is the reproducible path CI wants.
build_web() {
  echo "Building the web interface"
  if [ -f web/package-lock.json ]; then
    (cd web && npm ci --no-audit --no-fund)
  else
    (cd web && npm install --no-audit --no-fund)
  fi
  (cd web && npm run build)
}

# warn_no_web says why a binary has no web interface, rather than leaving it to
# be discovered by a browser getting a wall of plain text.
warn_no_web() {
  if [ ! -f internal/webui/out/index.html ]; then
    echo "  note: no web interface embedded (run ./build.sh --web first)"
  fi
}

# examples lists every program under examples/, so a new one is picked up here
# without this script having to name it.
examples() {
  for dir in examples/*/; do
    [ -f "${dir}main.go" ] || continue
    basename "$dir"
  done
}

build_one() {
  local goos="$1" goarch="$2" ext="${3:-}"
  local out="dist/relay-tunnel-${goos}-${goarch}${ext}"
  echo "  ${out}"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$ldflags" -o "$out" "$pkg"
  # The examples ship next to the CLI so they can be run without a toolchain.
  # They carry no version stamp of their own, so only -s -w here.
  local name
  for name in $(examples); do
    local exout="dist/examples/${name}-${goos}-${goarch}${ext}"
    echo "  ${exout}"
    GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
      go build -trimpath -ldflags "-s -w" -o "$exout" "./examples/${name}"
  done
}

# build_examples_host builds each example for this machine, next to the CLI.
build_examples_host() {
  local name
  for name in $(examples); do
    echo "  ./${name}"
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$name" "./examples/${name}"
  done
}

case "${1:-}" in
  --all)
    build_web
    echo "Building relay-tunnel ${version} and the examples for all targets"
    mkdir -p dist dist/examples
    build_one windows amd64 .exe
    build_one windows arm64 .exe
    build_one linux   amd64
    build_one linux   arm64
    build_one darwin  amd64
    build_one darwin  arm64
    if command -v sha256sum >/dev/null 2>&1; then
      # One checksums file over every artifact, examples included, which is the
      # same shape the release publishes.
      (cd dist && find . -type f ! -name SHA256SUMS | sed 's|^\./||' | sort | xargs sha256sum > SHA256SUMS)
      echo "Wrote dist/SHA256SUMS"
    fi
    ;;
  --web)
    build_web
    ;;
  --examples)
    echo "Building the examples for this machine"
    build_examples_host
    ;;
  --test)
    gofmt -l .
    go vet ./...
    go test ./...
    ;;
  -h|--help)
    echo "Usage: ./build.sh [--all | --web | --examples | --test | --help]"
    echo "  (no args)   build ./relay-tunnel for this machine"
    echo "  --web       export the admin web interface into internal/webui/out"
    echo "  --examples  build everything under examples/ for this machine"
    echo "  --all       build the web interface, then cross-compile the CLI and the examples into ./dist"
    echo "  --test      gofmt, go vet and go test"
    exit 0
    ;;
  "")
    echo "Building relay-tunnel ${version}"
    CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o relay-tunnel "$pkg"
    echo "Wrote ./relay-tunnel"
    warn_no_web
    ;;
  *)
    echo "unknown argument: $1 (try --help)" >&2
    exit 1
    ;;
esac
