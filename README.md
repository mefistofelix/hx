# hx

`hx` is a CLI tool that copies, downloads, or extracts a source into a local folder.

This implementation currently supports local paths, `file://` paths, plain HTTP(S) downloads, Git repositories, GitHub repository URLs, `docker://` image sources, `pypi://` package sources, `nuget://` package sources, `winget://` package sources, `npm://` package sources, `apt://` package sources, `rpm://` package sources, and `apk://` package sources, plus extraction for `.zip`, `.tar`, `.tar.gz`, `.tgz`, `.7z`, `.rar`, `.deb`, `.rpm`, `.apk`, and decompression for `.br`, `.bz2`, `.gz`, `.lz`, `.lz4`, `.mz`, `.s2`, `.sz`, `.xz`, `.zz`, and `.zst`.

## Usage

```sh
hx [flags] <source> [dest]
```

`source` currently supports:

- local files and directories
- `file://` local paths
- `http://` and `https://` file/archive URLs
- `http://` and `https://` Git repository URLs ending in `.git`
- `git://` repository URLs
- GitHub repository URLs only when the path is exactly `https://github.com/owner/repo` or `.../(tree|commit)/...`, plus `github://owner/repository`; other GitHub URLs such as release assets remain plain HTTP(S) archive/file URLs
- `docker://image[:tag]` image sources
- `pypi://package` and `pypi://package@version` sources
- `nuget://package` and `nuget://package@version` sources
- `winget://Package.Identifier` and `winget://Package.Identifier@version` sources
- `npm://package` and `npm://package@version` sources, plus `npm://package/submodule` aliases for scoped platform submodules such as `npm://prebuilt-tdlib/win32-x64`
- `apt://package` and `apt://package@version` sources
- `rpm://package` and `rpm://package@version` sources
- `apk://package` and `apk://package@version` sources

`dest` defaults to the current directory.

Running `hx` without a source prints a short example for every supported source scheme. Invalid flags, source URLs, and source-specific arguments exit with a readable `error:` message instead of a stack trace.

### npm submodules

Use `npm://package/submodule` for a scoped npm platform submodule. `hx` resolves it as `@package/submodule`; for example, `npm://prebuilt-tdlib/win32-x64` resolves and extracts `@prebuilt-tdlib/win32-x64`.

## Embedding

`hx` now also exposes an importable Go package at `hx/src/hx`.

The package keeps the CLI behavior available through a single high-level entrypoint:

```go
exit_code := hx.Main(args, stdout, stderr)
```

This is intended for projects that want to reuse `hx` in-process instead of spawning the `hx` executable as a subprocess.

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-delpathseg N` | `0` | Drop `N` leading path segments from extracted entries |
| `-symlinks 0|1` | `1` | Preserve symlinks when the source provides them and the platform supports them |
| `-download-only 0|1`, `-do 0|1` | `0` | Download or copy the source artifact without extraction; package and image sources may emit multiple files |
| `-notmp 0|1`, `-no-tempfile 0|1` | `0` | Refuse the temp-file fallback used for HTTP ZIP extraction |
| `-platform OS/ARCH[/VARIANT]`, `-plat ...` | host-specific | Select the target platform for sources that use it |
| `-registry VALUE`, `-reg VALUE` | auto | Override the registry or repository base for supported source types |
| `-target VALUE`, `-t VALUE` | auto | Select a repository-specific target such as distro release or framework |
| `-quiet 0|1`, `-q 0|1` | `0` | Retained for compatibility; normal output is always one destination file path per line |
| `--recursive 0|1` | `0` | Include Git submodules recursively for GitHub sources |
| `-incexc RULES` | `:+` | Apply ordered include/exclude rules to extracted paths |
| `-repath GLOBS` | empty | After `-incexc`, keep only items whose destination path suffix matches one of the glob or doublestar patterns, and rewrite the destination path to that matching suffix |
| `-f 0|1` | `1` | Replace existing destination entries instead of leaving them untouched |

## Behavior

- local directories are copied recursively
- archives are extracted into `dest`
- additional archive/compression formats such as `.7z`, `.rar`, `.xz`, `.bz2`, `.zst`, `.lz4`, `.br`, and related single-file compressed variants are handled through `mholt/archives`
- plain files are copied into `dest`
- generic Git sources fetch the selected shallow revision into an in-memory bare object store and write its tree directly to `dest`, without a `.git` directory or temporary worktree
- `github://owner/repository` downloads the requested branch or commit as a streaming tarball and removes GitHub's wrapper directory; use `?ref=branch-or-commit` to select a revision
- if the GitHub archive cannot be downloaded or extracted, `hx` warns on stderr and automatically falls back to a shallow Git checkout of the same revision
- `--recursive` is opt-in and uses the existing `go-git` implementation to read `.gitmodules` and each gitlink commit recursively, without GitHub APIs or temporary worktrees
- `-repath` filters and rewrites the final destination path after `-incexc`; for example `-repath '**/osqueryi*'` keeps `osquery-5.22.1.windows_x86_64/Program Files/osquery/osqueryi.exe` as `osqueryi.exe` and drops non-matching items
- `docker://` fetches the image manifest from the registry API, downloads the selected layers, and applies them to a temporary rootfs before copying to `dest`; with `-download-only`, it writes the manifest plus config/layer blobs instead
- `pypi://` downloads the package metadata, prefers the source distribution when available, then extracts or downloads the selected artifact
- `nuget://` resolves the package from the flat-container API and extracts or downloads the `.nupkg` artifact
- `winget://` resolves package manifests from the public `winget-pkgs` repository, selects a matching installer for the requested architecture, then downloads or extracts that installer artifact
- `npm://` resolves the package metadata and extracts or downloads the published tarball for the selected version
- npm submodule aliases resolve to their scoped package before the same metadata and tarball flow
- `apt://` fetches `Packages.gz`, resolves the selected package plus `Depends` and `Pre-Depends` for the requested distribution target and arch, then extracts or downloads the `.deb` artifacts
- `rpm://` fetches `repomd.xml` plus `primary.xml.gz`, resolves the selected package plus matching providers for `Requires`, then extracts or downloads the `.rpm` artifacts
- `apk://` fetches `APKINDEX.tar.gz`, resolves the selected package plus dependency providers for the requested repo and arch, then extracts or downloads the `.apk` artifacts
- if HTTPS certificate verification fails, `hx` warns and retries insecurely
- successful runs write a sentinel file in `dest`; the same source/options combination is skipped on the next run
- successful files and links are printed as destination paths, one per line; directories, byte counters, item counters, and Git progress are not printed

## Examples

```sh
hx ./sample.zip ./out
hx ./folder ./out
hx https://example.com/project.tar.gz ./out
hx https://example.com/project.git ./out
hx https://github.com/go-git/go-billy ./out
hx https://github.com/go-git/go-billy/tree/master ./out
hx github://go-git/go-billy ./out
hx 'github://true-async/php-src?ref=master' ./php-src
hx --recursive github://ngtcp2/ngtcp2 ./ngtcp2
hx docker://busybox:latest ./out
hx -registry https://pypi.org pypi://requests@2.32.3 ./out
hx -registry https://api.nuget.org nuget://Newtonsoft.Json@13.0.3 ./out
hx winget://Git.Git@2.46.0 ./out
hx -registry https://registry.npmjs.org npm://lodash@4.17.21 ./out
hx npm://prebuilt-tdlib/win32-x64 ./out
hx -registry https://deb.debian.org/debian -target bookworm/main -platform linux/amd64 apt://curl ./out
hx -registry https://download.fedoraproject.org/pub/fedora/linux/releases -target 41/Everything -platform linux/amd64 rpm://jq ./out
hx -registry https://dl-cdn.alpinelinux.org/alpine -target v3.22/main -platform linux/amd64 apk://curl ./out
hx -download-only https://example.com/file.zip ./downloads
hx -repath '**/osqueryi*' https://github.com/osquery/osquery/releases/download/5.22.1/osquery-5.22.1.windows_x86_64.zip ./out
hx -delpathseg 1 ./sample.tar.gz ./out
```

## Build

The build scripts bootstrap Go automatically when it is not already available.

```sh
# Windows
build.bat

# Linux / macOS
chmod +x build.sh && ./build.sh
```

Build outputs are written to `bin/`.

Tagged GitHub Releases include the direct executables `hx-windows-amd64.exe` and `hx-linux-amd64`.

## Test

```sh
# Windows
test.bat

# Linux / macOS
chmod +x test.sh && ./test.sh
```

## Platform Notes

- symlinks are preserved on Windows and Unix-like platforms when the current user can create symbolic links
- on Windows, local or extracted symlink creation may require Developer Mode or equivalent symlink privilege
- `docker://` sources work without Docker or Podman on every platform
