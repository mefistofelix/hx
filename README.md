# hx

`hx` is a CLI tool that copies, downloads, or extracts a source into a local folder.

This first implementation round supports local paths, `file://` paths, plain HTTP(S) downloads, and archive extraction for `.zip`, `.tar`, `.tar.gz`, `.tgz`, and single-file `.gz`.

## Usage

```sh
hx [flags] <source> [dest]
```

`source` currently supports:

- local files and directories
- `file://` local paths
- `http://` and `https://` file/archive URLs

`dest` defaults to the current directory.

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `-strip N`, `-skip N` | `0` | Strip `N` leading path components from extracted entries |
| `-symlinks` | `false` | Preserve symlinks when the source provides them and the platform supports them |
| `-download-only`, `-do` | `false` | Download or copy the source as a single file without extraction |
| `-notmp`, `-no-tempfile` | `false` | Refuse the temp-file fallback used for HTTP ZIP extraction |
| `-quiet`, `-q` | `false` | Use plain output instead of the ANSI status line |
| `-incexc RULES` | `:+` | Apply ordered include/exclude rules to extracted paths |

## Behavior

- local directories are copied recursively
- archives are extracted into `dest`
- plain files are copied into `dest`
- successful runs write a sentinel file in `dest`; the same source/options combination is skipped on the next run

## Examples

```sh
hx ./sample.zip ./out
hx ./folder ./out
hx https://example.com/project.tar.gz ./out
hx -download-only https://example.com/file.zip ./downloads
hx -strip 1 ./sample.tar.gz ./out
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

## Test

```sh
# Windows
test.bat

# Linux / macOS
chmod +x test.sh && ./test.sh
```
