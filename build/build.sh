#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
go_root="$root_dir/build_cache/go"
go_bin="$go_root/bin/go"

get_go_tool() {
    if command -v go >/dev/null 2>&1; then
        command -v go
        return
    fi
    if [ -x "$go_bin" ]; then
        printf '%s\n' "$go_bin"
        return
    fi

    mkdir -p "$root_dir/build_cache"
    version="1.25.0"
    os_name="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch_name="$(uname -m)"

    case "$os_name" in
        linux*) os_name="linux" ;;
        darwin*) os_name="darwin" ;;
        *) printf 'unsupported os: %s\n' "$os_name" >&2; exit 1 ;;
    esac

    case "$arch_name" in
        x86_64|amd64) arch_name="amd64" ;;
        aarch64|arm64) arch_name="arm64" ;;
        *) printf 'unsupported arch: %s\n' "$arch_name" >&2; exit 1 ;;
    esac

    archive_path="$root_dir/build_cache/go-$version.tar.gz"
    curl -L "https://go.dev/dl/go$version.$os_name-$arch_name.tar.gz" -o "$archive_path"
    rm -rf "$go_root"
    mkdir -p "$go_root"
    tar -xzf "$archive_path" -C "$go_root" --strip-components=1
    printf '%s\n' "$go_bin"
}

go_tool="$(get_go_tool)"
mkdir -p "$root_dir/bin" "$root_dir/build_cache/gocache"
GOCACHE="$root_dir/build_cache/gocache" "$go_tool" build -o "$root_dir/bin/hx" ./src

