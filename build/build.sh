#!/usr/bin/env sh
set -eu

project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
build_cache="$project_root/build_cache"
go_root="$build_cache/go"
go_bin="$go_root/bin/go"
go_version="1.22.3"
go_archive="$build_cache/go.tar.gz"
go_url="https://go.dev/dl/go${go_version}.$(uname | tr '[:upper:]' '[:lower:]')-amd64.tar.gz"

mkdir -p "$build_cache" "$project_root/bin"

if [ ! -x "$go_bin" ]; then
    curl -L "$go_url" -o "$go_archive"
    rm -rf "$go_root"
    tar -C "$build_cache" -xzf "$go_archive"
fi

cd "$project_root"
"$go_bin" test ./src
"$go_bin" build -o "$project_root/bin/hx" ./src
