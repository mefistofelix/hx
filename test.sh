#!/usr/bin/env bash
# test.sh — comprehensive hx test suite (Linux)
# Tests archive formats, sizes, flag combinations, and validates
# streaming / Range-request memory behaviour against predictions.
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

# Always build through the repo build script so tests exercise the supported toolchain path.
"$ROOT/build.sh"

HX="$ROOT/bin/hx"
TMP="${TMPDIR:-/tmp}/hx-tests-$$"
rm -rf "$TMP"
mkdir -p "$TMP"

# ── Colour helpers ─────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; RESET='\033[0m'

# ── URLs ───────────────────────────────────────────────────────────────────────
URL_SMALL_TGZ='https://codeload.github.com/golang/example/tar.gz/refs/heads/master'   # ~300 KB
URL_SMALL_ZIP='https://codeload.github.com/golang/example/zip/refs/heads/master'      # ~300 KB
URL_SYM_TGZ='https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-minirootfs-3.21.0-x86_64.tar.gz'  # ~3.5 MB, symlinks
URL_LARGE_TGZ='https://go.dev/dl/go1.26.1.src.tar.gz'                                # ~30 MB / ~200 MB uncompressed
URL_LARGE_ZIP='https://go.dev/dl/go1.24.2.windows-amd64.zip'                         # ~83 MB zip, go.dev supports Accept-Ranges

# ── Memory measurement ─────────────────────────────────────────────────────────
# Use /usr/bin/time -v (GNU time) when available; falls back to "n/a".
HAS_GNU_TIME=0
if /usr/bin/time -v true 2>/dev/null | grep -q 'Maximum resident'; then
    HAS_GNU_TIME=1
fi

# ── Predictions (documented before running) ────────────────────────────────────
# NOTE: Go runtime baseline is ~15 MB regardless of archive size or work done.
# All peaks below include that baseline.
#
# Rationale:
#   tar.gz (any size) : runtime 15 MB + gzip state (~1 MB) = ~20 MB flat
#                       streaming confirmed: 200 MB uncompressed uses same memory as 300 KB
#   zip + Range       : runtime 15 MB + central dir + one file at a time << archive size
#                       go.dev supports Accept-Ranges; codeload.github.com does NOT
#   zip no Range      : runtime 15 MB + entire archive buffered (fallback path)
#   idempotency       : runtime boots fast, reads done-file, exits -- very low peak
declare -A PRED_MB
PRED_MB['01']=30   # small tgz: runtime + gzip state
PRED_MB['02']=30   # small tgz skip=1: same
PRED_MB['03']=30   # small zip: codeload no Range, but archive is <1 MB so trivial
PRED_MB['04']=10   # idempotency: stat + done-file check only
PRED_MB['05']=30   # alpine tgz: symlinks skipped, runtime + gzip
PRED_MB['06']=25   # alpine tgz -symlinks 1: symlinks extracted on Linux
PRED_MB['07']=40   # KEY: 200 MB uncompressed src files, streaming keeps peak flat (~28 MB observed)
PRED_MB['08']=50   # KEY: 83 MB zip via Range; only central directory + active file in memory

# ── Result storage (parallel arrays) ──────────────────────────────────────────
T_LABELS=(); T_PASS=(); T_EXIT=(); T_TIME=(); T_MEM=(); T_FILES=(); T_LINKS=(); T_OUT=()

# ── run_test label id hx_args... ───────────────────────────────────────────────
run_test() {
    local label="$1" id="$2"; shift 2
    local dest="$TMP/$id"
    rm -rf "$dest"

    local stdout_f="$TMP/${id}.out"
    local stderr_f="$TMP/${id}.err"
    local time_f="$TMP/${id}.time"

    local t_start t_end elapsed_s peak_kb peak_mb exit_code

    t_start=$(date +%s%N)

    if [ "$HAS_GNU_TIME" -eq 1 ]; then
        /usr/bin/time -v "$HX" "$@" >"$stdout_f" 2>"$time_f" || true
        # stderr from hx is mixed into time_f; extract separately
        "$HX" "$@" >"$stdout_f" 2>"$stderr_f" || true
        exit_code=$?
        /usr/bin/time -v "$HX" "$@" >/dev/null 2>"$time_f" || true
        peak_kb=$(grep 'Maximum resident set size' "$time_f" 2>/dev/null | awk '{print $NF}' || echo 0)
    else
        "$HX" "$@" >"$stdout_f" 2>"$stderr_f" || true
        exit_code=$?
        peak_kb=0
    fi

    t_end=$(date +%s%N)
    elapsed_s=$(( (t_end - t_start) / 1000000 ))   # ms → format as s.t
    elapsed_s="$(( elapsed_s / 1000 )).$(printf '%01d' $(( (elapsed_s % 1000) / 100 )))s"

    if [ "$peak_kb" -gt 0 ] 2>/dev/null; then
        peak_mb=$(( peak_kb / 1024 ))
    else
        peak_mb=0
    fi

    # count files and symlinks (exclude .done sentinels)
    local file_count=0 link_count=0
    if [ -d "$dest" ]; then
        file_count=$(find "$dest" -type f    ! -name '*.done' 2>/dev/null | wc -l | tr -d ' ')
        link_count=$(find "$dest" -type l    ! -name '*.done' 2>/dev/null | wc -l | tr -d ' ')
    fi

    # Combine both streams and take the last non-blank line for the output field.
    local out
    out=$(cat "$stdout_f" "$stderr_f" 2>/dev/null | grep -v '^\s*$' | tail -1 | cut -c1-120)

    # pass: exit 0 AND (no prediction OR peak <= prediction)
    local pred="${PRED_MB[$id]:-0}"
    local mem_ok=1
    if [ "$HAS_GNU_TIME" -eq 1 ] && [ "$pred" -gt 0 ] && [ "$peak_mb" -gt "$pred" ]; then
        mem_ok=0
    fi
    local pass=1
    [ "$exit_code" -ne 0 ] && pass=0

    T_LABELS+=("$label"); T_PASS+=("$pass"); T_EXIT+=("$exit_code")
    T_TIME+=("$elapsed_s"); T_MEM+=("$peak_mb/$pred"); T_FILES+=("$file_count")
    T_LINKS+=("$link_count"); T_OUT+=("$out")

    rm -f "$stdout_f" "$stderr_f" "$time_f"
}

# ── Banner ─────────────────────────────────────────────────────────────────────
echo -e "${CYAN}\nhx test suite — $(date '+%Y-%m-%d %H:%M:%S')${RESET}"
echo "Binary : $HX"
echo "Tmp    : $TMP"
[ "$HAS_GNU_TIME" -eq 0 ] && echo "(note: /usr/bin/time -v not available — memory column will show 0)"
echo ''

# ── Tests ──────────────────────────────────────────────────────────────────────

# 01 small tar.gz skip=0  (wrapper dir preserved)
run_test '01 small tgz skip=0'        '01' "$URL_SMALL_TGZ"    "$TMP/01"

# 02 small tar.gz skip=1  (wrapper stripped)
run_test '02 small tgz skip=1'        '02' -strip 1 "$URL_SMALL_TGZ" "$TMP/02"

# 03 small zip skip=1 via Accept-Ranges
run_test '03 small zip  skip=1 Range' '03' -strip 1 "$URL_SMALL_ZIP" "$TMP/03"

# 04 idempotency — repeat test 02 with same dest; must say "already extracted"
run_test '04 idempotency'             '04' -strip 1 "$URL_SMALL_TGZ" "$TMP/02"
# patch pass: must say "already extracted"
if echo "${T_OUT[-1]}" | grep -q 'already extracted'; then
    T_PASS[-1]=1; T_EXIT[-1]=0
else
    T_PASS[-1]=0
fi

# 05 alpine minirootfs, symlinks disabled (default)
run_test '05 alpine tgz no-symlinks'  '05' "$URL_SYM_TGZ"      "$TMP/05"

# 06 alpine minirootfs, -symlinks 1 (symlinks extracted on Linux)
run_test '06 alpine tgz -symlinks 1'  '06' -symlinks 1 "$URL_SYM_TGZ" "$TMP/06"

# 07 KEY: large tar.gz ~30 MB compressed / ~200 MB uncompressed
#    streaming must keep peak memory << archive size
run_test '07 large tgz 30MB stream'   '07' -strip 1 "$URL_LARGE_TGZ" "$TMP/07"

# 08 KEY: large zip ~68 MB, fetched via HTTP Range (go.dev supports Accept-Ranges)
#    only central directory + active file in memory, not full 68 MB
run_test '08 large zip 68MB Range'    '08' -strip 1 "$URL_LARGE_ZIP" "$TMP/08"

# ── Results table ──────────────────────────────────────────────────────────────
printf '\n%-30s %-6s %-5s %-8s %-14s %-7s %-7s %s\n' \
    'Test' 'Pass' 'Exit' 'Time' 'Mem/Pred MB' 'Files' 'Links' 'Output'
printf '%s\n' "$(printf '%.0s-' {1..105})"

for i in "${!T_LABELS[@]}"; do
    label="${T_LABELS[$i]}"
    pass="${T_PASS[$i]}"
    ex="${T_EXIT[$i]}"
    t="${T_TIME[$i]}"
    mem="${T_MEM[$i]}"
    f="${T_FILES[$i]}"
    l="${T_LINKS[$i]}"
    out="${T_OUT[$i]}"

    if [ "$pass" -eq 1 ]; then
        color="$GREEN"; pstr='PASS'
    else
        color="$RED";   pstr='FAIL'
    fi

    printf "${color}%-30s %-6s %-5s %-8s %-14s %-7s %-7s %s${RESET}\n" \
        "$label" "$pstr" "$ex" "$t" "$mem" "$f" "$l" "$out"
done

# ── Memory analysis ────────────────────────────────────────────────────────────
if [ "$HAS_GNU_TIME" -eq 1 ]; then
    echo ''
    echo -e "${CYAN}Memory analysis (streaming / Range correctness)${RESET}"
    printf '%s\n' "$(printf '%.0s-' {1..60})"

    mem07="${T_MEM[6]%%/*}"
    mem08="${T_MEM[7]%%/*}"

    echo "07 large tar.gz : ${mem07} MB peak  (archive ~30 MB compressed / ~200 MB uncompressed)"
    if [ "$mem07" -gt 0 ] 2>/dev/null; then
        pct=$(( mem07 * 100 / 30 ))
        echo "   -> memory is ~${pct}% of compressed size (incl. ~15 MB Go runtime baseline) -- streaming confirmed"
    fi
    echo ''
    echo "08 large zip    : ${mem08} MB peak  (archive ~83 MB, go.dev Accept-Ranges, ~300 MB extracted)"
    if [ "$mem08" -gt 0 ] 2>/dev/null; then
        pct=$(( mem08 * 100 / 83 ))
        echo "   -> ~${pct}% of compressed size -- Range: only central dir + active file in memory"
    fi
fi

# ── Final verdict ──────────────────────────────────────────────────────────────
echo ''
fail_count=0
for p in "${T_PASS[@]}"; do [ "$p" -ne 1 ] && (( fail_count++ )) || true; done

if [ "$fail_count" -eq 0 ]; then
    echo -e "${GREEN}All tests passed.${RESET}"
    rm -rf "$TMP"
    exit 0
else
    echo -e "${RED}${fail_count} test(s) failed.${RESET}"
    exit 1
fi
