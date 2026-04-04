#!/usr/bin/env bash
# build.sh — self-contained build script for hx
# Downloads the Go toolchain into ./build/go/ on first run,
# then compiles hx for Linux + Windows into ./bin/.
set -euo pipefail

# PURE BASH FUNCTION TO DETERMINE THE CURRENT BASH SCRIPT DIRECTORY ABSOLUTE PATH:
get_script_dir() {
  local wdir
  local scriptdir
  wdir="$PWD"; [ "$PWD" = "/" ] && wdir=""
  case "$0" in
    /*) scriptdir="${0}";;
    *) scriptdir="$wdir/${0#./}";;
  esac
  scriptdir="${scriptdir%/*}"
  REPLY=$scriptdir
}

get_script_dir
ROOT="$REPLY"

# ── Configuration ──────────────────────────────────────────────────────────────
GO_VERSION=1.26.1
BUILD_DIR="$ROOT/build"
GOROOT="$BUILD_DIR/go"
SRC="$ROOT/src"
BIN_DIR="$ROOT/bin"

export GOROOT
export GOPATH="$BUILD_DIR/.gopath"
export GOCACHE="$BUILD_DIR/.gocache"
export CGO_ENABLED=0
export GOFLAGS=

mkdir -p "$BIN_DIR"

# ── Download + unpack Go toolchain if not already present ──────────────────────
if [ ! -x "$GOROOT/bin/go" ]; then
    TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"
    echo "[1/3] Downloading Go ${GO_VERSION} ..."
    curl -# -L -o "$TARBALL" "https://go.dev/dl/$TARBALL"
    echo "[2/3] Extracting Go toolchain to ./build/go/ ..."
    mkdir -p "$BUILD_DIR"
    tar -xzf "$TARBALL" -C "$BUILD_DIR"
    rm "$TARBALL"
fi

# ── Fetch module dependencies on first build ───────────────────────────────────
if [ ! -f "$SRC/go.sum" ]; then
    echo "[2/3] Fetching dependencies ..."
    "$GOROOT/bin/go" -C "$SRC" get github.com/mholt/archives@latest
    "$GOROOT/bin/go" -C "$SRC" mod tidy
fi

# ── Build ──────────────────────────────────────────────────────────────────────
echo "[3/3] Building ..."
export GOARCH=amd64

GOOS=linux   "$GOROOT/bin/go" -C "$SRC" build -ldflags="-s -w" -o "$BIN_DIR/hx" .
echo "  OK -> bin/hx      (linux/amd64)"

GOOS=windows "$GOROOT/bin/go" -C "$SRC" build -ldflags="-s -w" -o "$BIN_DIR/hx.exe" .
echo "  OK -> bin/hx.exe  (windows/amd64)"
