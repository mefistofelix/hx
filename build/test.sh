#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"$root_dir/build/build.sh"

go_tool="$(command -v go || printf '%s\n' "$root_dir/build_cache/go/bin/go")"
mkdir -p "$root_dir/tests_cache/gocache"
GOCACHE="$root_dir/tests_cache/gocache" "$go_tool" test ./tests/...

