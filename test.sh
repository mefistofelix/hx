#!/usr/bin/env bash
set -euo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bash "$root_dir/build/build.sh"
bash "$root_dir/tests/cli_smoke.sh"
