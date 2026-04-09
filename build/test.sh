#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bash "$root_dir/build/build.sh"

go_tool="$root_dir/build_cache/go/bin/go"
mkdir -p "$root_dir/tests_cache/gocache"
GOCACHE="$root_dir/tests_cache/gocache" HX_GO_EXE="$go_tool" "$go_tool" test ./tests/...
