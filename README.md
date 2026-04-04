# hx

Stream-extract **tar.gz, zip, 7z, rar** and more over HTTP on the fly — optionally strip leading path segments, zero dependencies, statically compiled. Drop it into any CI pipeline or bootstrap script on any platform.

## Install

Download the binary for your platform from [Releases](../../releases) and put it on your `PATH`.

Or build from source (see [Building](#building)).

## Usage

```
hx [flags] <url> [dest]
```

| Argument | Required | Description |
|----------|----------|-------------|
| `url` | yes | HTTP/HTTPS URL of the archive |
| `dest` | no | Destination folder; defaults to current directory; created if absent |

| Flag | Default | Description |
|------|---------|-------------|
| `-skip N` | `0` | Strip N leading path components from every archive entry |
| `-symlinks` | off | Extract symbolic links (skipped by default for safety) |
| `-quiet` | off | Plain text output instead of rich ANSI progress |
| `-no-tempfile` | off | Buffer non-Range ZIP in memory instead of a temp file |

Flags must be placed before `url`.

## Examples

```sh
# Extract into current directory, strip the top-level wrapper folder
hx -skip 1 https://example.com/repo.tar.gz

# Extract into ./out/
hx https://example.com/repo.zip ./out

# Strip prefix and extract symlinks
hx -skip 1 -symlinks https://example.com/repo.tar.gz ./out

# CI / plain text output (no ANSI)
hx -quiet -skip 1 https://example.com/repo.tar.gz ./out

# Force in-memory ZIP buffer (no temp file on disk)
hx -no-tempfile https://example.com/repo.zip ./out
```

## Output

### Plain mode (CI-friendly)

```
url:    https://example.com/repo.tar.gz
format: tar.gz  32.5 MB
done  14970 files  138.2 MB  (4.1s)
```

### ANSI progress mode (default)

```
Downloading  [▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱]   43%  35.6 / 83.0 MB  4.2 MB/s  ETA 11s
Extracting  go/src/compress/gzip/gunzip.go  [4.2 kB]  file 1,234  22.3 MB extracted  [▰▰▱▱ 52% @ 3.1 MB/s]
done  14970 files  138.2 MB  (4.1s)
```

## Idempotency

After a successful extraction `hx` writes a sentinel file in the destination. On subsequent runs with the same URL, destination, `-skip`, and `-symlinks` values it prints `already extracted, skipping` and exits 0 immediately. Changing any of those flags triggers a fresh extraction.

## Supported formats

- **tar** — plain, gzip, bzip2, xz, zstd, lz4, brotli, snappy, and more
- **zip**
- **7-Zip**, **RAR** (read-only), and others via [mholt/archives](https://github.com/mholt/archives)

Format is auto-detected from magic bytes.

## Streaming design

| Format | Strategy |
|--------|----------|
| tar-based | True streaming — bytes flow TCP → decompressor → disk. Memory is O(1). |
| ZIP with `Accept-Ranges` | HTTP 206 Range requests — only the central directory and active file are fetched. Peak memory stays near the Go runtime baseline (~15 MB). |
| ZIP without `Accept-Ranges` | Downloaded to a temp file on disk, then extracted. A `[warn]` line is printed. Use `-no-tempfile` to buffer in memory instead. |

## Building

Requires no pre-installed Go — the build scripts download and cache the toolchain automatically.

```sh
# Windows
build.bat

# Linux / macOS
chmod +x build.sh && ./build.sh
```

Output binaries land in `bin/`:

```
bin/hx.exe   Windows AMD64, statically linked
bin/hx       Linux AMD64, statically linked
```

## License

MIT
