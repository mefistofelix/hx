# hx — HTTP archive extractor

## Purpose

`hx` stream-extracts **tar.gz, zip, 7z, rar** and more over HTTP on the fly —
optionally stripping leading path segments, with zero dependencies and a
statically compiled binary.  Designed to be dropped into any CI pipeline or
bootstrap script on any platform.

## Usage

```
hx [flags] <url> [dest]
```

| Argument | Required | Description |
|----------|----------|-------------|
| `url`    | yes      | HTTP/HTTPS URL of the archive |
| `dest`   | no       | Destination folder; defaults to the current directory; created if absent |

| Flag | Default | Description |
|------|---------|-------------|
| `-skip N` | `0` | Strip N leading path components from every archive entry |
| `-symlinks` | off | Extract symbolic links (skipped by default for safety) |
| `-quiet` | off | Plain text output instead of rich ANSI progress |
| `-no-tempfile` | off | Buffer non-Range ZIP in memory instead of a temp file |

Flags must be placed before `url`.

### Examples

```sh
# Extract into current directory, strip the top-level wrapper folder
hx -skip 1 https://example.com/repo.tar.gz

# Extract into ./out/, keep full paths
hx https://example.com/repo.zip ./out

# Strip prefix and extract symlinks
hx -skip 1 -symlinks https://example.com/repo.tar.gz ./out

# CI / plain text output (no ANSI)
hx -quiet -skip 1 https://example.com/repo.tar.gz ./out

# Force in-memory ZIP buffer (no temp file on disk)
hx -no-tempfile https://example.com/repo.zip ./out
```

### -skip example

Archive contains `rootfolder/sub/file.txt`, `-skip 1`, dest `./out`:

```
rootfolder/sub/file.txt  ->  ./out/sub/file.txt
```

### Done-file / idempotency

After a successful extraction `hx` writes a sentinel file that encodes the
flags that affect the extracted content (`-skip`, `-symlinks`):

```
<dest>/hx-<sanitized-url>-skip<N>-sym<0|1>args.done
```

On subsequent invocations with the same `url`, `dest`, `-skip`, and `-symlinks`
values the tool detects the sentinel, prints `already extracted, skipping`, and
exits 0 immediately.  Changing any of those flags causes a fresh extraction.
`-quiet` and `-no-tempfile` are intentionally excluded from the sentinel name
because they do not affect the extracted content.

## Output

### Plain mode (default — good for CI logs)

```
url:    https://example.com/repo.tar.gz
format: tar.gz  32.5 MB
done  14970 files  138.2 MB  (4.1s)
```

If the server does not support HTTP Range requests a warning is printed before
the download begins:

```
[warn] server does not support HTTP Range (no Accept-Ranges: bytes); downloading to temp file /tmp/hx-1234567890.zip
```

### ANSI progress mode (default)

A single line is redrawn in place using `\r\033[2K`.  Colors require a
VT100-compatible terminal (Windows Terminal, iTerm2, any modern Linux terminal).

**Download phase** (ZIP temp-file download only — streaming formats skip this):
```
Downloading  [████████████░░░░░░░░░░░░░░░░]   43%  35.6 / 83.0 MB  4.2 MB/s  ETA 11s
```

**Extraction phase** (all formats):
```
Extracting  go/src/compress/gzip/gunzip.go  [4.2 kB]  file 1,234  22.3 MB extracted  [██░░░░░░░░░░░░ 52% @ 3.1 MB/s]
```

After extraction:
```
done  14970 files  138.2 MB  (4.1s)
```

Color key:
- **url:** / **format:** labels — dim
- **[warn]** — bold yellow
- Filename in Extracting line — cyan
- File count + extracted size — green
- Download bar (filled) — green, (empty) — dark gray
- **done** — bold green; counts — bold; time — dim

## Supported archive formats

All formats recognised by [github.com/mholt/archives](https://github.com/mholt/archives):

- Tar (plain, gzip, bzip2, xz, zstd, lz4, brotli, snappy, …)
- ZIP
- 7-Zip, RAR (read-only), and others supported by the library

Format is auto-detected from magic bytes first; the URL file extension is used
as a fallback hint when bytes are ambiguous.

## Streaming design

| Format  | Strategy |
|---------|----------|
| tar-based | True streaming — bytes flow from TCP through the decompressor directly into the file handler.  Memory usage is O(1). |
| ZIP (server supports `Accept-Ranges: bytes` + `Content-Length`) | `httpRangeReader` satisfies `io.ReaderAt + io.Seeker` using HTTP 206 Partial Content requests.  Only the bytes that `archive/zip` actually needs are fetched (central directory + individual file data).  Peak memory stays near the Go runtime baseline (~15 MB). |
| ZIP (no Range support) | Archive downloaded to `os.CreateTemp("", "hx-*.zip")` on disk, then extracted from the temp file.  The temp file is deleted on exit.  Use `-no-tempfile` to buffer in memory instead. |

### Why temp file instead of memory for non-Range ZIPs?

`archive/zip` requires `io.ReaderAt`, so the full archive must be available
before extraction can start.  Keeping 100 MB in a `bytes.Reader` costs 100 MB
of process heap.  Writing to a temp file and passing `*os.File` (which also
satisfies `io.ReaderAt`) costs only OS page-cache pages that are immediately
evictable.  A `[warn]` line is always printed when this path is taken so the
operator knows a download phase precedes extraction.

## Build system

The project is self-contained.  On first run each script downloads the Go
toolchain, then compiles both platform targets.

| Script | Run on | Downloads |
|--------|--------|-----------|
| `build.bat` | Windows (calls `build.ps1`) | `go*.windows-amd64.zip` |
| `build.ps1` | Windows PowerShell | `go*.windows-amd64.zip` |
| `build.sh` | Linux / macOS | `go*.linux-amd64.tar.gz` |

```
# Windows
build.bat          (or:  pwsh build.ps1)

# Linux / macOS
chmod +x build.sh && ./build.sh
```

Output binaries land in `bin/`:

```
bin/hx.exe   Windows AMD64, statically linked, stripped
bin/hx       Linux AMD64, statically linked, stripped
```

To change the Go version edit `$GoVersion` / `GO_VERSION` at the top of the
build script.

### Directories created by the build (gitignore these)

```
build/go/        Go toolchain
build/.gopath/   module download cache
build/.gocache/  build cache
bin/             compiled output
src/go.sum       generated — commit this
```

## Project layout

```
hx/
├── src/
│   ├── main.go      single-file implementation
│   ├── go.mod       module declaration
│   └── go.sum       dependency checksums (commit this)
├── bin/             compiled binaries (gitignore)
├── build/           toolchain + caches (gitignore)
├── build.bat        thin launcher -> build.ps1
├── build.ps1        Windows build script
├── build.sh         Linux/macOS build script
├── test.ps1         Windows test suite
├── test.sh          Linux test suite
└── CLAUDE.md        this file
```

Source lives in `src/` so that `go mod tidy` and all go commands only scan
that subdirectory — keeping the bundled Go toolchain in `build/go/` completely
invisible to the module system.

All go commands use `go -C "$Src"` / `go -C "%SRC%"` where the path is the
absolute path to `src/`, resolved at build time.

## Implementation notes

- **No third-party libraries except `mholt/archives`.**  All HTTP work uses
  `net/http`; buffering uses `bufio` from stdlib.
- **`bufio.NewReaderSize` wrapper** gives `archives.Identify` a buffered reader
  so it can peek magic bytes without consuming the stream.
- **`httpRangeReader`** implements `io.Reader`, `io.ReaderAt`, and `io.Seeker`
  using HTTP 206 Partial Content.  Each `ReadAt` call opens its own connection
  so concurrent access by `archive/zip` is safe.  The struct also holds a
  `fetched int64` counter that calls `pr.onDL` after each successful fetch,
  providing real-time download progress in `-progress` mode even for Range-based
  extraction.  Uses the final post-redirect URL to avoid an extra round trip per
  request.
- **ZIP temp file** — when `Accept-Ranges` or `Content-Length` is missing,
  `os.CreateTemp("", "hx-*.zip")` is created, the full archive is streamed into
  it via `io.Copy`, then the `*os.File` (which implements `seekReaderAt`) is
  passed to `archives.Zip{}.Extract`.  A deferred closure calls `Close` then
  `os.Remove` (order matters on Windows, where an open handle blocks deletion).
- **Printer / progress system** — a single `printer` struct owns all output.
  `info()` and `warn()` commit any in-place ANSI line before printing.  `render()`
  is throttled to 100 ms and uses `\033[2K\r` to repaint one line in place.
  The `url:` header is printed before the HTTP body is read so it always appears
  first even when the initial bufio fill triggers an `onDL` callback.
- **`dest` is resolved to absolute path** early in `main` so the
  path-traversal guard works correctly for any relative input including `.`.
- **Symlinks** are skipped by default; opt in with `-symlinks`.  On Windows,
  symlink creation requires Developer Mode or elevated privileges.
- **CGO_ENABLED=0** produces a fully static binary with no libc dependency.
- **Done-file sentinel** encodes only `-skip` and `-symlinks` (flags that affect
  extracted content).  `-quiet` and `-no-tempfile` are excluded so changing
  the output mode does not invalidate a previously completed extraction.
