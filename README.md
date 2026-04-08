# hx

`hx` is a CLI tool that copies, downloads, or extracts a source into a local folder.

This implementation currently supports local paths, `file://` paths, plain HTTP(S) downloads, Git repositories, GitHub repository URLs, `pypi://` package sources, `nuget://` package sources, and `npm://` package sources, plus extraction for `.zip`, `.tar`, `.tar.gz`, `.tgz`, and single-file `.gz`.

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
- GitHub repository URLs such as `https://github.com/owner/repo`, `/tree/<ref>`, and `/commit/<sha>`
- `pypi://package` and `pypi://package@version` sources
- `nuget://package` and `nuget://package@version` sources
- `npm://package` and `npm://package@version` sources for non-scoped packages

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
- Git sources are cloned to a temporary worktree and copied without the `.git` directory
- `pypi://` downloads the package metadata, prefers the source distribution when available, then extracts or downloads the selected artifact
- `nuget://` resolves the package from the flat-container API and extracts or downloads the `.nupkg` artifact
- `npm://` resolves the package metadata and extracts or downloads the published tarball for the selected version
- if HTTPS certificate verification fails, `hx` warns and retries insecurely
- successful runs write a sentinel file in `dest`; the same source/options combination is skipped on the next run

## Examples

```sh
hx ./sample.zip ./out
hx ./folder ./out
hx https://example.com/project.tar.gz ./out
hx https://example.com/project.git ./out
hx https://github.com/go-git/go-billy ./out
hx https://github.com/go-git/go-billy/tree/master ./out
hx -registry https://pypi.org pypi://requests@2.32.3 ./out
hx -registry https://api.nuget.org nuget://Newtonsoft.Json@13.0.3 ./out
hx -registry https://registry.npmjs.org npm://lodash@4.17.21 ./out
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
