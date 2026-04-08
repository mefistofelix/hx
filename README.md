# hx

`hx` is a CLI tool that copies or downloads a source into a local folder with idempotent re-runs.

This repository is currently in the first implementation stage driven by `CODE_DESIGN.md`.

## Current scope

Implemented today:

- local file copy
- local directory copy
- `file://` sources
- `http://` and `https://` single-file downloads
- destination sentinel for skip-on-repeat behavior
- basic include/exclude filtering
- path prefix stripping
- plain or ANSI progress output

Not implemented yet:

- archive extraction
- Git and GitHub sources
- package registries
- container images
- `download_only` behavior beyond raw file copy/download

## Usage

```sh
hx [flags] <source> [dest]
```

Supported `source` values in the current stage:

- local path like `./file.txt` or `./dir`
- `file:///path/to/file.txt`
- `https://example.com/file.txt`

`dest` defaults to the current directory.

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-strip N`, `-skip N` | `0` | Skip `N` leading path components before writing to destination |
| `-symlinks 0|1` | `1` | Disable symlink materialization from local directory sources with `0` |
| `-incexc RULES` | empty | Ordered `+pattern` and `-pattern` path rules |
| `-quiet 0|1`, `-q 0|1` | `0` | Use plain output instead of ANSI progress updates |
| `-overwrite 0|1` | `1` | Overwrite destination files when present |
| `-platform`, `-plat`, `-registry`, `-reg`, `-target`, `-t`, `-download-only`, `-do`, `-notmp`, `-no-tempfile` | parsed | Reserved and persisted already for future source implementations |

## Build

The build scripts bootstrap a local Go toolchain into `build_cache/` and write binaries to `bin/`.

```sh
# Windows
powershell -ExecutionPolicy Bypass -File build/build.ps1

# Linux / macOS
chmod +x build/build.sh && build/build.sh
```

## Test

Test entrypoints live in `tests/`, while Go package tests stay next to `src/main.go`.

```sh
# Windows
powershell -ExecutionPolicy Bypass -File tests/test.ps1

# Linux / macOS
chmod +x tests/test.sh && tests/test.sh
```
