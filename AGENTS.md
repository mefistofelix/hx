# hx - archive extractor

## Agent rules

- Whenever a code or behavior change is made, always update `README.md` and `AGENTS.md` in the same commit to keep docs in sync.

## Purpose

`hx` extracts archives from HTTP(S), Docker registry images, NuGet packages, WinGet manifests, PyPI packages, npm packages, APT repositories, RPM repositories, Alpine APK repositories, Git repository URLs, or local files, supports single-file compression formats like `.gz`, and falls back to copying plain files when the source is not an archive. It can strip leading path segments, skips symlinks by default for safety, and ships as a statically linked binary with no runtime dependencies beyond the standard library plus `mholt/archives`, `go-git`, `go-rpmutils`, and `go.yaml.in/yaml/v4`.

## Usage

```sh
hx [flags] <source> [dest]
```

| Argument | Required | Description |
|----------|----------|-------------|
| `source` | yes | HTTP/HTTPS URL, `docker://` image reference, `nuget://` package reference, `winget://` package reference, `pypi://` package reference, `npm://` package reference, `apt://` package reference, `rpm://` package reference, `apk://` package reference, Git repository URL, or local file path |
| `dest` | no | Destination folder; defaults to the current directory; created if absent |

| Flag | Default | Description |
|------|---------|-------------|
| `-skip N` | `0` | Strip N leading path components from every archive entry |
| `-symlinks` | off | Extract symbolic links (skipped by default for safety) |
| `-quiet` | off | Plain text output instead of rich ANSI progress |
| `-download-only` | off | Download/copy the original source file without extracting or decompressing it |
| `-no-tempfile` | off | Buffer non-Range ZIP in memory instead of a temp file |
| `-platform OS/ARCH[/VARIANT]` | `linux/<host-arch>` | Platform selector for Docker registry images and WinGet installer architecture selection, and the base OS/arch hint for source types that care about platform, for example `linux/amd64` |
| `-registry VALUE` | auto | Override the registry/repository base for Docker, NuGet, WinGet, PyPI, npm, APT, RPM, or APK sources |
| `-target VALUE` | auto | Repository-specific target selector such as `bionic`, `v3.22`, `42`, or `net8.0` for source types that support it |

Flags must be placed before `source`.

### Examples

```sh
# Remote tarball, strip wrapper directory
hx -skip 1 https://example.com/repo.tar.gz

# Remote zip into ./out
hx https://example.com/repo.zip ./out

# Local zip into ./out
hx .\downloads\repo.zip ./out

# Local tarball into ./out with wrapper stripped
hx -skip 1 ./downloads/repo.tar.gz ./out

# Decompress a single gzip file into ./out/file.txt
hx https://example.com/file.txt.gz ./out

# Download a plain file without extracting it
hx https://example.com/tool.exe ./out

# Download an archive without extracting it
hx -download-only https://example.com/repo.tar.gz ./out

# Download the default branch of a Git repo
hx https://github.com/go-git/go-billy ./out

# Download a specific branch/tag/commit from a Git repo
hx https://github.com/go-git/go-billy?branch=master ./out
hx https://github.com/go-git/go-billy#tag=v5.6.2 ./out
hx https://github.com/go-git/go-billy#commit=9d2901ab42b4 ./out

# Extract a container image root filesystem from a registry
hx docker://busybox:latest ./out

# Select a specific image platform from a multi-arch image
hx -platform linux/amd64 docker://registry.k8s.io/pause:3.9 ./out

# Download a container image without applying its layers
hx -download-only docker://busybox:latest ./out

# Extract the latest npm package tarball
hx npm://lodash ./out

# Extract a specific npm version or dist-tag
hx npm://typescript@5.8.3 ./out
hx npm://react@next ./out

# Download an npm tarball without extracting it
hx -download-only npm://@types/node@24.0.0 ./out

# Extract an APT package plus all its dependencies
hx apt://curl ./out

# Pin the APT repository target with -target
hx -registry "https://archive.ubuntu.com/ubuntu" -target bionic apt://curl ./out

# Download the resolved .deb files without extracting them
hx -download-only apt://curl ./out

# Extract an RPM package plus its dependencies
hx rpm://bash ./out

# Extract an Alpine APK package plus its dependencies
hx apk://curl ./out

# Extract a PyPI package plus its dependencies
hx pypi://requests ./out

# Extract a NuGet package plus its dependencies
hx nuget://Newtonsoft.Json ./out

# Force a specific NuGet target framework group
hx -target netstandard2.0 nuget://Newtonsoft.Json ./out

# Download a WinGet installer
hx winget://Microsoft.VisualStudioCode ./out

# Download the resolved .apk files without extracting them
hx -download-only apk://curl ./out

# Enable symlink extraction
hx -skip 1 -symlinks https://example.com/repo.tar.gz ./out
```

## Done-file / idempotency

After a successful extraction `hx` writes:

```text
<dest>/hx-<sanitized-source-id>-skip<N>-sym<0|1>-dl<0|1>args.done
```

- Remote sources use the URL as the source ID.
- Docker sources use the normalized image reference plus the selected platform as the source ID.
- NuGet sources use the normalized registry, package name, selected version when pinned, and explicit `-target` when set as the source ID.
- WinGet sources use the normalized registry plus package identifier and selected version when pinned as the source ID.
- PyPI sources use the normalized registry plus package name and selected version when pinned as the source ID.
- npm sources use the normalized registry plus package name and selected version or dist-tag as the source ID.
- APT sources use the normalized repository base plus package name/version and selected repository target as the source ID.
- RPM sources use the normalized repository base plus package name/version and selected repository target when set as the source ID.
- APK sources use the normalized repository base plus package name/version and selected repository target as the source ID.
- Git sources use the normalized clone URL plus the selected branch/tag/commit as the source ID.
- Local sources use the absolute file path as the source ID.
- `-quiet` and `-no-tempfile` are excluded because they do not affect extracted content.
- `-download-only` is included because it changes the produced output.
- Changing `-registry` or `-target` for a source type that uses them produces a different sentinel.

## Output

### Plain mode

```text
source: https://example.com/repo.tar.gz
format: tar.gz  32.5 MB
done  14970 files  138.2 MB  (4.1s)
```

### ANSI progress mode

- HTTP ZIP downloads may show a `Downloading` progress line before extraction.
- Extraction always shows an in-place `Extracting` line when ANSI output is enabled.
- Single-file compression formats are decompressed into a single output file inside `dest`.
- Plain non-archive sources are copied into `dest` unchanged.
- `-download-only` copies the original source file and skips extraction/decompression.
- Local archives skip the HTTP download phase and go straight to extraction.

## Supported archive formats

All formats recognized by [github.com/mholt/archives](https://github.com/mholt/archives), including:

- Tar and compressed tar variants
- ZIP
- 7-Zip and RAR (read-only)
- Docker/OCI registry images fetched through the registry HTTP API
- NuGet packages resolved from the NuGet V3 service index and flat container
- WinGet packages resolved from WinGet YAML manifests parsed with [go.yaml.in/yaml/v4](https://pkg.go.dev/go.yaml.in/yaml/v4) and installer URLs
- PyPI packages resolved from the PyPI JSON API, including transitive dependencies from `requires_dist`
- npm packages fetched from the npm registry and resolved to their published tarballs
- APT packages resolved from a repository `Packages` index, including transitive dependencies
- RPM packages resolved from repository metadata, including transitive dependencies, with payload extraction via [github.com/sassoftware/go-rpmutils](https://github.com/sassoftware/go-rpmutils)
- Alpine APK packages resolved from `APKINDEX.tar.gz`, including transitive dependencies
- Git repositories via [github.com/go-git/go-git](https://github.com/go-git/go-git)

Format is auto-detected from magic bytes first, with the source basename used as a hint when needed.

For Docker registry sources, use an explicit `docker://` image reference such as `docker://busybox:latest` or `docker://ghcr.io/org/image:tag`. `hx` talks to the registry API directly and does not require Docker, Podman, or any other local container runtime.

With `-download-only`, Docker registry sources are saved as a simple on-disk layout: `manifest.json` plus the original config/layer blobs under `blobs/<algorithm>/<digest>`, without applying the image filesystem.

For NuGet sources, use `nuget://package` or `nuget://package@version`. By default `hx` uses the NuGet V3 service index at `https://api.nuget.org/v3/index.json`. Use `-registry` to point at a different NuGet V3 service index, and use `-target` to force a target framework selector such as `net8.0`, `netstandard2.0`, or `dotnetcore`. `-platform` remains an OS/arch selector and is not used to encode the .NET framework version. `hx` resolves the latest version from the flat container when needed, selects the most appropriate dependency group from the package `.nuspec`, then downloads the `.nupkg` files and extracts them like ZIP archives. `-registry` fragments are ignored; use `-target` explicitly instead.

For WinGet sources, use `winget://Package.Identifier` or `winget://Package.Identifier@version`. By default `hx` uses the GitHub API view of `microsoft/winget-pkgs`. Use `-registry` to point at a different GitHub manifests API root, or a GitHub repository URL that can be normalized to one. `hx` resolves the selected manifest version, chooses an installer matching `-platform` architecture, follows package dependencies declared in the manifest when present, then downloads the referenced installer artifacts and handles them like any other source.

For PyPI sources, use `pypi://package` or `pypi://package@version`. By default `hx` uses `https://pypi.org/pypi`. Use `-registry` to point at a different JSON-compatible PyPI registry base. `hx` resolves the selected release, follows `requires_dist` recursively in a conservative way, then prefers a wheel artifact and falls back to an sdist when needed.

For npm sources, use `npm://package`, `npm://package@version`, or `npm://package@dist-tag`. `hx` resolves package metadata from the npm registry, selects the requested version, then downloads the published tarball and handles it like any other remote archive.

For APT sources, use `apt://package` or `apt://package@version`. By default `hx` uses `https://archive.ubuntu.com/ubuntu` and, if no target is specified, picks the newest repository target that actually contains the requested package. Use `-registry` to point at a different APT base URL and `-target` to choose a repository-specific target such as `bionic`. `-platform` supplies the target architecture for APT package resolution. `-registry` fragments are ignored; use `-target` explicitly instead.

For RPM sources, use `rpm://package` or `rpm://package@version`. By default `hx` uses Fedora release repositories and picks the newest repository target exposed by the metadata. Use `-registry` to point at a different RPM repository base and `-target` to choose a repository-specific target such as `42`. `-platform` supplies the target architecture for RPM package resolution. `-registry` fragments are ignored; use `-target` explicitly instead.

For Alpine APK sources, use `apk://package` or `apk://package@version`. By default `hx` uses `https://dl-cdn.alpinelinux.org/alpine` and, if no target is specified, probes the repository and picks the newest `vX.Y` target that actually contains the requested package. Use `-registry` to point at a different Alpine base URL and `-target` to choose a repository-specific target such as `v3.22` or `edge`. Add `?component=community` to switch repository component. `-platform` supplies the target architecture for APK package resolution. `-registry` fragments are ignored; use `-target` explicitly instead.

For HTTPS sources, if certificate verification fails, `hx` emits a warning and retries insecurely instead of aborting the download.

For Git sources, `hx` accepts explicit clone URLs such as `https://host/org/repo.git`, plus direct GitHub repository URLs like `https://github.com/org/repo`. GitHub archive and release asset URLs continue through the normal HTTP archive/file path and are not treated as Git repositories.

## Extraction design

| Source / format | Strategy |
|-----------------|----------|
| Remote tar-based archives | True streaming from HTTP response through decompressor into file writes |
| Remote ZIP with `Accept-Ranges` and `Content-Length` | `httpRangeReader` provides `io.ReaderAt`/`io.Seeker` over HTTP 206 requests |
| Remote ZIP without Range support | Full archive is downloaded to a temp file, or buffered in memory with `-no-tempfile` |
| Single-file compression formats | Decompress the payload and write it as one file in `dest`, usually dropping the compression suffix |
| Plain files | Copy the source file into `dest` unchanged |
| Local archives | Source file is opened directly; local ZIP extraction reads from the file itself |
| `-download-only` | Copy the original source bytes into `dest` without extraction. For Docker registry images it downloads `manifest.json` plus the referenced blobs instead of applying the layers. For NuGet packages it downloads the resolved `.nupkg` files without extracting them. For WinGet packages it downloads the resolved installer artifacts without extracting them. For PyPI packages it downloads the resolved wheel/sdist artifacts without extracting them. For npm packages it downloads the published `.tgz` tarball without extracting it. For APT sources it downloads the resolved `.deb` files without unpacking them. For RPM and APK sources it downloads the resolved package files without unpacking them |
| Docker registry images | Fetch the manifest from the registry API, select the requested platform, then stream and apply each layer directly into `dest` without temp files |
| NuGet packages | Fetch the NuGet V3 service index, resolve versions via the flat container, select the best matching dependency group from `.nuspec`, then download and extract or copy each `.nupkg` |
| WinGet packages | Query the WinGet manifests repository through the GitHub contents API, parse the selected version manifest, choose an installer for the selected architecture, then download and extract or copy the installer artifact |
| PyPI packages | Fetch the PyPI JSON API for the selected project/release, traverse `requires_dist`, then download and extract or copy each selected wheel or sdist artifact |
| npm packages | Fetch package metadata from the npm registry, resolve a version or dist-tag, then download and extract or copy the published tarball |
| APT repositories | Fetch the repository `Packages` index, resolve a package plus its dependencies, then download and extract or copy each `.deb` |
| RPM repositories | Fetch `repomd.xml` plus the primary metadata, resolve a package plus its dependencies, then download and extract or copy each `.rpm` |
| Alpine APK repositories | Fetch `APKINDEX.tar.gz`, resolve a package plus its dependencies from `P`/`D`/`p` fields, then download and extract or copy each `.apk` |
| Git repositories | Clone into a temp directory with `go-git`, then copy only the checked-out worktree into `dest` without leaving a usable `.git` directory behind |

## Project layout

```text
hx/
|-- src/
|   |-- main.go
|   |-- go.mod
|   `-- go.sum
|-- bin/
|-- build/
|-- build.bat
|-- build.ps1
|-- build.sh
|-- test.ps1
|-- test.sh
`-- AGENTS.md
```

## Testing

Use the platform-native test entrypoint:

```sh
# Windows
test.ps1

# Linux / macOS
chmod +x test.sh && ./test.sh
```

The test scripts always build `hx` first through `build.ps1` or `build.sh`, so tests and release automation exercise the same bootstrap/build path.

## Implementation notes

- `src/main.go` is the single-file implementation.
- `resolveInputSource` classifies the first argument as remote (`http/https`), Docker image, NuGet package, WinGet package, PyPI package, npm package, APT package, RPM package, APK package, Git, or local.
- Docker image references are accepted only with an explicit `docker://` or `oci://` source prefix to keep source detection conservative and avoid ambiguity.
- NuGet package references are accepted only with an explicit `nuget://` source prefix so they do not collide with ordinary URLs or local paths.
- WinGet package references are accepted only with an explicit `winget://` source prefix so they do not collide with ordinary URLs or local paths.
- PyPI package references are accepted only with an explicit `pypi://` source prefix so they do not collide with ordinary URLs or local paths.
- npm package references are accepted only with an explicit `npm://` source prefix so they do not collide with ordinary URLs or local paths.
- APT package references are accepted only with an explicit `apt://` prefix so they do not collide with ordinary URLs or local paths.
- RPM package references are accepted only with an explicit `rpm://` prefix so they do not collide with ordinary URLs or local paths.
- APK package references are accepted only with an explicit `apk://` prefix so they do not collide with ordinary URLs or local paths.
- Docker registry pulls use the HTTP API directly with bearer-token auth when challenged, select manifests by `-platform`, and stream layers into `dest` without temp files.
- `-download-only` for Docker stores the selected `manifest.json` and original blobs instead of applying the layer filesystem.
- NuGet sources resolve the package base from the V3 service index, choose a version from the flat container, then traverse only the best matching dependency group from the `.nuspec`.
- WinGet sources resolve the selected manifest version from the GitHub contents API, parse YAML manifests with `go.yaml.in/yaml/v4`, and choose an installer matching `-platform`.
- PyPI sources resolve project metadata from the JSON API, traverse `requires_dist` conservatively, then prefer wheel artifacts and fall back to sdists.
- npm sources resolve the packument from the registry, choose an exact version or dist-tag, then reuse the normal tarball extraction/download path.
- APT sources resolve package metadata from `Packages` indexes, traverse `Depends` and `Pre-Depends`, then extract the `data.tar.*` payload from each downloaded `.deb`. If `-target` is omitted, `hx` probes the repository and chooses the newest target that actually contains the requested package.
- RPM sources resolve package metadata from `repomd.xml` and the primary XML, traverse dependency/provide metadata, then extract each package payload with `go-rpmutils`. Paths that are invalid on Windows are warned about and skipped instead of aborting the extraction.
- APK sources resolve package metadata from `APKINDEX.tar.gz`, traverse dependencies/provides from `D:` and `p:`, then stream the gzip/tar payload directly. Alpine control entries such as `.PKGINFO` are skipped during extraction.
- Direct GitHub repository URLs are recognized conservatively; GitHub archive/release asset URLs stay on the normal HTTP path.
- Remote ZIP handling still uses the HTTP-specific fallback and range-reader logic.
- Local ZIP handling rewinds the opened file and extracts directly from disk.
- Git branch and tag downloads use shallow clone options where possible; exact commit downloads may need a broader fetch before detached checkout.
- Formats that implement `archives.Decompressor` but not `archives.Extractor` are written as a single output file.
- If `archives.Identify` returns `NoMatch`, the source is copied into `dest` as a plain file.
- `-download-only` short-circuits format handling and writes the original source file as-is.
- HTTPS downloads retry insecurely with a warning if TLS certificate verification fails.
- The path traversal guard remains based on an absolute destination path.
- Symlinks remain opt-in via `-symlinks`.
