# hx

`hx` is a CLI tool that downloads, extracts, or copies a source into a local folder in a single pass.

It supports local files, HTTP(S) URLs, Git repositories, container images, package registries, archives, compressed single files, and plain files.

## Install

Download the binary for your platform from [Releases](../../releases) and put it on your `PATH`.

Or build from source with the included scripts.

## Usage

```sh
hx [flags] <source> [dest]
```

`source` can be:

- a local path such as `./file.zip`, `../dir/archive.tgz`, `/opt/archive.tgz`, or `file:///opt/archive.tgz`
- an HTTP or HTTPS URL
- a Git repository URL
- a `docker://` image reference
- a `nuget://`, `winget://`, `pypi://`, `npm://`, `apt://`, `rpm://`, or `apk://` source

`dest` is optional. If omitted, the current directory is used.

Flags must be placed before `source`.

## Common flags

| Flag | Default | Description |
| --- | --- | --- |
| `-strip N`, `-skip N` | `0` | Strip `N` leading path components from extracted entries |
| `-symlinks 0|1` | `1` | Enable or disable symlink extraction when supported |
| `-download-only 0|1`, `-do 0|1` | `0` | Download the source without extracting it |
| `-notmp 0|1`, `-no-tempfile 0|1` | `0` | Avoid temp-file fallback for ZIP downloads from non-range HTTP servers |
| `-platform OS/ARCH[/VARIANT]`, `-plat ...` | host-specific | Select the target platform for sources that use it |
| `-registry VALUE`, `-reg VALUE` | auto | Override the registry or repository base for supported source types |
| `-target VALUE`, `-t VALUE` | auto | Select a repository-specific target such as distro release or framework |
| `-incexc RULES` | `:+` | Include or exclude extracted paths with ordered `+` and `-` rules |
| `-quiet 0|1`, `-q 0|1` | `0` | Prefer plain CI-friendly output |

## Examples

```sh
# Extract a remote archive into ./out
hx https://example.com/repo.tar.gz ./out

# Extract a local archive and strip the top-level folder
hx -strip 1 ./downloads/repo.zip ./out

# Decompress a single gzip file
hx https://example.com/file.txt.gz ./out

# Download without extracting
hx -do 1 https://example.com/repo.tar.gz ./out

# Download a GitHub repository
hx https://github.com/go-git/go-billy ./out

# Extract a container image filesystem
hx docker://busybox:latest ./out

# Extract a NuGet package
hx nuget://Newtonsoft.Json ./out

# Extract a PyPI package
hx pypi://requests ./out

# Extract an npm package
hx npm://lodash ./out

# Extract an APT package with dependencies
hx apt://curl ./out
```

## Idempotency

After a successful extraction or download, `hx` writes a sentinel file in the destination.

If the same source is requested again with the same material options, `hx` skips the operation and exits successfully.

The cache key depends on the source identity and relevant options such as destination, `-strip`, `-symlinks`, `-download-only`, `-platform`, `-registry`, `-target`, and `-incexc` when they affect the output.

## Supported formats

- archives: `.tar`, `.tar.gz`, `.zip`, `.7z`, `.rar`, `.deb`, `.rpm`, `.cpio`, and other formats supported through [mholt/archives](https://github.com/mholt/archives)
- compressed single files: `.br`, `.bz2`, `.gz`, `.lz`, `.lz4`, `.mz`, `.s2`, `.sz`, `.xz`, `.zz`, `.zst`
- Git repositories via [go-git](https://github.com/go-git/go-git)
- Docker and OCI images fetched directly from the registry API
- NuGet, WinGet, PyPI, npm, APT, RPM, and Alpine APK package sources

Format detection is automatic. If the source is not recognized as an archive or compressed payload, `hx` copies it into `dest` as a plain file.

## Notes

- local files are read directly from disk
- GitHub repository URLs are accepted directly as Git sources
- `docker://` sources do not require Docker or Podman
- with `-download-only`, package and image sources are downloaded without unpacking or applying layers
- when `-incexc` is used, rules are evaluated relative to the destination root after path stripping
- for HTTPS sources, if certificate verification fails, `hx` warns and retries insecurely

## Building

The included scripts bootstrap the Go toolchain automatically. No preinstalled Go is required.

```sh
# Windows
build.bat

# Linux / macOS
chmod +x build.sh && ./build.sh
```

Build outputs are written to `bin/`.

## Testing

Use the platform-native test script:

```sh
# Windows
test.ps1

# Linux / macOS
chmod +x test.sh && ./test.sh
```

## License

MIT
