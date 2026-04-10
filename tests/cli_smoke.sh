#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
hx_bin="$root_dir/bin/hx"
tests_cache_dir="$root_dir/tests_cache"

fail() {
    printf 'test failed: %s\n' "$1" >&2
    exit 1
}

assert_file() {
    [ -f "$1" ] || fail "missing file: $1"
}

assert_glob() {
    compgen -G "$1" >/dev/null || fail "missing match: $1"
}

assert_contains() {
    case "$1" in
        *"$2"*) ;;
        *) fail "missing text: $2" ;;
    esac
}

run_hx() {
    "$hx_bin" -quiet "$@"
}

rm -rf "$tests_cache_dir/cli"
mkdir -p "$tests_cache_dir/cli"

local_root="$tests_cache_dir/cli/local"
mkdir -p "$local_root/out"
printf 'plain\n' >"$local_root/payload.txt"
run_hx -q 1 -symlinks 0 "$local_root/payload.txt" "$local_root/out"
assert_file "$local_root/out/payload.txt"

link_root="$tests_cache_dir/cli/link"
mkdir -p "$link_root/out"
printf 'linked\n' >"$link_root/payload.txt"
ln -s payload.txt "$link_root/src_link.txt"
run_hx "$link_root/src_link.txt" "$link_root/out"
[ -L "$link_root/out/src_link.txt" ] || fail "missing symlink: $link_root/out/src_link.txt"
[ "$(readlink "$link_root/out/src_link.txt")" = "payload.txt" ] || fail "unexpected symlink target"

http_root="$tests_cache_dir/cli/http"
mkdir -p "$http_root/out"
run_hx -strip 1 "https://github.com/go-git/go-billy/archive/refs/heads/master.tar.gz" "$http_root/out"
assert_file "$http_root/out/fs.go"
http_repeat_output="$(run_hx -strip 1 "https://github.com/go-git/go-billy/archive/refs/heads/master.tar.gz" "$http_root/out" 2>&1 || true)"
assert_contains "$http_repeat_output" "already matches"

tar_xz_root="$tests_cache_dir/cli/tar_xz"
mkdir -p "$tar_xz_root/out"
run_hx -strip 1 "https://raw.githubusercontent.com/glennrp/libpng-releases/master/libpng-1.6.34.tar.xz" "$tar_xz_root/out"
assert_file "$tar_xz_root/out/README"

zst_root="$tests_cache_dir/cli/zst"
mkdir -p "$zst_root/out"
run_hx "https://london.mirror.pkgbuild.com/core/os/x86_64/bash-5.3.9-1-x86_64.pkg.tar.zst" "$zst_root/out"
assert_file "$zst_root/out/usr/bin/bash"
assert_file "$zst_root/out/usr/share/doc/bash/README"

github_root="$tests_cache_dir/cli/github"
mkdir -p "$github_root/out"
run_hx "https://github.com/go-git/go-billy" "$github_root/out"
assert_file "$github_root/out/go.mod"

github_tree_root="$tests_cache_dir/cli/github_tree"
mkdir -p "$github_tree_root/out"
run_hx "https://github.com/go-git/go-billy/tree/master" "$github_tree_root/out"
assert_file "$github_tree_root/out/go.mod"

github_release_zip_root="$tests_cache_dir/cli/github_release_zip"
mkdir -p "$github_release_zip_root/out"
run_hx "https://github.com/osquery/osquery/releases/download/5.22.1/osquery-5.22.1.windows_x86_64.zip" "$github_release_zip_root/out"
assert_file "$github_release_zip_root/out/osquery-5.22.1.windows_x86_64/Program Files/osquery/osqueryd/osqueryd.exe"

pypi_root="$tests_cache_dir/cli/pypi"
mkdir -p "$pypi_root/out"
run_hx -strip 1 "pypi://requests@2.32.3" "$pypi_root/out"
assert_file "$pypi_root/out/pyproject.toml"

nuget_root="$tests_cache_dir/cli/nuget"
mkdir -p "$nuget_root/out"
run_hx "nuget://Newtonsoft.Json@13.0.3" "$nuget_root/out"
assert_file "$nuget_root/out/lib/net45/Newtonsoft.Json.dll"

winget_root="$tests_cache_dir/cli/winget"
mkdir -p "$winget_root/out"
run_hx -platform "windows/amd64" "winget://Git.Git@2.46.0" "$winget_root/out"
assert_glob "$winget_root/out/Git-*-64-bit.exe"

npm_root="$tests_cache_dir/cli/npm"
mkdir -p "$npm_root/out"
run_hx "npm://lodash@4.17.21" "$npm_root/out"
assert_file "$npm_root/out/package/package.json"

docker_root="$tests_cache_dir/cli/docker"
mkdir -p "$docker_root/out"
run_hx -platform "linux/amd64" "docker://busybox:1.36.1" "$docker_root/out"
assert_file "$docker_root/out/bin/busybox"

docker_do_root="$tests_cache_dir/cli/docker_do"
mkdir -p "$docker_do_root/out"
run_hx -download-only 1 -platform "linux/amd64" "docker://busybox:1.36.1" "$docker_do_root/out"
assert_file "$docker_do_root/out/manifest.json"
assert_glob "$docker_do_root/out/sha256-*.tar*"

apk_root="$tests_cache_dir/cli/apk"
mkdir -p "$apk_root/out"
run_hx -target "v3.22/main" -platform "linux/amd64" "apk://curl" "$apk_root/out"
assert_file "$apk_root/out/usr/bin/curl"
assert_glob "$apk_root/out/usr/lib/libcurl.so.4*"

apt_root="$tests_cache_dir/cli/apt"
mkdir -p "$apt_root/out"
run_hx -registry "https://deb.debian.org/debian" -target "bookworm/main" -platform "linux/amd64" "apt://curl" "$apt_root/out"
assert_file "$apt_root/out/usr/bin/curl"
assert_glob "$apt_root/out/usr/lib/x86_64-linux-gnu/libcurl.so.4*"

rpm_root="$tests_cache_dir/cli/rpm"
mkdir -p "$rpm_root/out"
run_hx -registry "https://archives.fedoraproject.org/pub/archive/fedora/linux/releases" -target "41/Everything" -platform "linux/amd64" "rpm://jq" "$rpm_root/out"
assert_file "$rpm_root/out/usr/bin/jq"
assert_glob "$rpm_root/out/usr/lib64/libonig.so.5*"

printf 'cli smoke tests passed\n'
