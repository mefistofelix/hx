# hx - universal artifact fetcher and extractor

Fetch and extract **archives, packages, container images, Git repos, and plain files** from HTTP(S) URLs, package registries, container registries, Git repository URLs, or local files. `hx` also handles single-file compression formats like `.gz`, supports download-only mode, and stays dependency-light and CI-friendly.

## Install

Download the binary for your platform from [Releases](../../releases) and put it on your `PATH`.

Or build from source (see [Building](#building)).

## Usage

```sh
hx [flags] <source> [dest]
```

| Argument | Required | Description |
|----------|----------|-------------|
| `source` | yes | HTTP/HTTPS URL, `docker://` image reference, `nuget://` package reference, `winget://` package reference, `pypi://` package reference, `npm://` package reference, `apt://` package reference, `rpm://` package reference, `apk://` package reference, Git repository URL, or local file path |
| `dest` | no | Destination folder; defaults to current directory; created if absent |

| Flag | Default | Description |
|------|---------|-------------|
| `-strip N`, `-skip N` | `0` | Strip N leading path components from every archive entry |
| `-symlinks 0|1` | `0` | Extract symbolic links when set to `1` (skipped by default for safety) |
| `-quiet 0|1`, `-q 0|1` | `0` | Plain text output instead of rich ANSI progress |
| `-download-only 0|1`, `-do 0|1` | `0` | Download/copy the original source file without extracting or decompressing it |
| `-notmp 0|1`, `-no-tempfile 0|1` | `0` | Buffer non-Range ZIP in memory instead of a temp file |
| `-platform OS/ARCH[/VARIANT]`, `-plat ...` | `linux/<host-arch>` | Platform selector for Docker registry images and WinGet installer architecture selection, and the base OS/arch hint for source types that care about platform, for example `linux/amd64` |
| `-registry VALUE`, `-reg VALUE` | auto | Override the registry/repository base for Docker, NuGet, WinGet, PyPI, npm, APT, RPM, or APK sources |
| `-target VALUE`, `-t VALUE` | auto | Repository-specific target selector such as `bionic`, `v3.22`, `42`, or `net8.0` for source types that support it |

Flags must be placed before `source`.

## Examples

```sh
# Extract into current directory, strip the top-level wrapper folder
hx -strip 1 https://example.com/repo.tar.gz

# Extract a remote ZIP into ./out/
hx https://example.com/repo.zip ./out

# Extract a local ZIP into ./out/
hx .\downloads\repo.zip ./out

# Strip the wrapper folder from a local tarball
hx -strip 1 ./downloads/repo.tar.gz ./out

# Decompress a single gzip file into ./out/file.txt
hx https://example.com/file.txt.gz ./out

# Download a plain file without extracting it
hx https://example.com/tool.exe ./out

# Download an archive without extracting it
hx -do 1 https://example.com/repo.tar.gz ./out

# Download the default branch of a Git repo
hx https://github.com/go-git/go-billy ./out

# Download a specific branch/tag/commit from a Git repo
hx https://github.com/go-git/go-billy?branch=master ./out
hx https://github.com/go-git/go-billy#tag=v5.6.2 ./out
hx https://github.com/go-git/go-billy#commit=9d2901ab42b4 ./out

# Extract a container image root filesystem from a registry
hx docker://busybox:latest ./out

# Select a specific image platform from a multi-arch image
hx -plat linux/amd64 docker://registry.k8s.io/pause:3.9 ./out

# Download a container image without applying its layers
hx -do 1 docker://busybox:latest ./out

# Extract the latest NuGet package plus its dependencies
hx nuget://Newtonsoft.Json ./out

# Extract a specific NuGet version
hx nuget://Newtonsoft.Json@13.0.3 ./out

# Force a specific NuGet target framework group
hx -t netstandard2.0 nuget://Newtonsoft.Json ./out

# Download the resolved NuGet packages without extracting them
hx -do 1 nuget://Newtonsoft.Json ./out

# Download the latest WinGet installer for the selected architecture
hx winget://Microsoft.VisualStudioCode ./out

# Pin a WinGet version and architecture
hx -plat linux/amd64 winget://Microsoft.VisualStudioCode@1.105.1 ./out

# Download a WinGet installer without extracting it
hx -do 1 winget://Microsoft.VisualStudioCode ./out

# Extract the latest npm package tarball
hx npm://lodash ./out

# Extract the latest PyPI package plus its dependencies
hx pypi://requests ./out

# Extract a specific PyPI version
hx pypi://httpx@0.28.1 ./out

# Download the resolved PyPI artifacts without extracting them
hx -do 1 pypi://httpx ./out

# Extract a specific npm version or dist-tag
hx npm://typescript@5.8.3 ./out
hx npm://react@next ./out

# Download an npm tarball without extracting it
hx -do 1 npm://@types/node@24.0.0 ./out

# Extract an APT package plus all its dependencies
hx apt://curl ./out

# Pin the APT repository target with -target
hx -reg "https://archive.ubuntu.com/ubuntu" -t bionic apt://curl ./out

# Download the resolved .deb files without extracting them
hx -do 1 apt://curl ./out

# Extract an RPM package plus its dependencies
hx rpm://bash ./out

# Pin the RPM repository target with -target
hx -reg "https://mirrors.kernel.org/fedora/releases" -t 42 rpm://bash ./out

# Extract an Alpine APK package plus its dependencies
hx apk://curl ./out

# Pin the Alpine repository target with -target
hx -reg "https://dl-cdn.alpinelinux.org/alpine" -t v3.22 apk://curl ./out

# Download the resolved .apk files without extracting them
hx -do 1 apk://curl ./out

# Strip prefix and extract symlinks
hx -strip 1 -symlinks 1 https://example.com/repo.tar.gz ./out

# CI / plain text output (no ANSI)
hx -q 1 -strip 1 https://example.com/repo.tar.gz ./out

# Force in-memory ZIP buffer for a non-Range HTTP server
hx -notmp 1 https://example.com/repo.zip ./out
```

## Output

### Plain mode (CI-friendly)

```text
source: https://example.com/repo.tar.gz
format: tar.gz  32.5 MB
done  14970 files  138.2 MB  (4.1s)
```

### ANSI progress mode (default)

```text
Downloading  [▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱]   43%  35.6 / 83.0 MB  4.2 MB/s  ETA 11s
Extracting  go/src/compress/gzip/gunzip.go  [4.2 kB]  file 1,234  22.3 MB extracted  [▰▰▱▱ 52% @ 3.1 MB/s]
done  14970 files  138.2 MB  (4.1s)
```

## Idempotency

After a successful extraction/download `hx` writes a sentinel file in the destination. On subsequent runs with the same source, destination, `-strip`, `-symlinks`, `-download-only`, and Docker `-platform` values it prints `already extracted, skipping` and exits 0 immediately.

- Remote sources are keyed by URL.
- Git sources are keyed by the normalized clone URL plus selected branch/tag/commit.
- Docker sources are keyed by normalized image reference plus selected platform.
- NuGet sources are keyed by normalized registry, package name, selected version when pinned, and explicit `-target` when set.
- WinGet sources are keyed by normalized registry plus package identifier and selected version when pinned.
- PyPI sources are keyed by normalized registry plus package name and selected version when pinned.
- npm sources are keyed by normalized registry plus package name and selected version or dist-tag.
- APT sources are keyed by normalized repository base plus package name/version and selected repository target.
- RPM sources are keyed by normalized repository base plus package name/version and selected repository target when set.
- APK sources are keyed by normalized repository base plus package name/version and selected repository target.
- Local sources are keyed by absolute file path.
- Changing the source, destination, `-strip`, `-symlinks`, `-download-only`, or `-platform` triggers a fresh extraction/download.
- Changing `-registry` or `-target` for a source type that uses them also triggers a fresh extraction/download.

## Supported formats

- **tar**: plain, gzip, bzip2, xz, zstd, lz4, brotli, snappy, and more
- **zip**
- **7-Zip**, **RAR** (read-only), and others via [mholt/archives](https://github.com/mholt/archives)
- **Git repositories** via [go-git](https://github.com/go-git/go-git)
- **Docker/OCI registry images** fetched directly from the registry HTTP API
- **NuGet packages** resolved from the NuGet V3 service index and flat container
- **WinGet packages** resolved from WinGet YAML manifests parsed with [go.yaml.in/yaml/v4](https://pkg.go.dev/go.yaml.in/yaml/v4) and downloaded from the referenced installer URLs
- **PyPI packages** resolved from the PyPI JSON API, including transitive dependencies from `requires_dist`
- **npm packages** fetched from the npm registry and resolved to their published tarballs
- **APT packages** resolved from a repository `Packages` index, including transitive dependencies
- **RPM packages** resolved from repository metadata, including transitive dependencies, with payload extraction via [github.com/sassoftware/go-rpmutils](https://github.com/sassoftware/go-rpmutils)
- **Alpine APK packages** resolved from `APKINDEX.tar.gz`, including transitive dependencies

Format is auto-detected from magic bytes. If no archive/compression format matches, `hx` falls back to copying the source file into `dest`.

For Git sources, `hx` accepts explicit Git clone URLs such as `https://host/org/repo.git`, plus direct GitHub repository URLs like `https://github.com/org/repo`. GitHub archive/release asset URLs such as `/archive/...zip` still stay on the normal HTTP archive path and are not reinterpreted as Git repositories.

For Docker registry sources, use an explicit `docker://` image reference such as `docker://busybox:latest`. Use `-registry` to override the registry host, for example `-registry ghcr.io docker://org/image:tag`. `hx` talks to the registry API directly and streams layers into `dest` without requiring Docker, Podman, or any other local container runtime.

With `-download-only`, Docker registry sources are stored as a simple on-disk layout: `manifest.json` plus the original config/layer blobs under `blobs/<algorithm>/<digest>`, without applying the image filesystem.

For NuGet sources, use `nuget://package` or `nuget://package@version`. By default `hx` uses the NuGet V3 service index at `https://api.nuget.org/v3/index.json`. Use `-registry` to point at a different NuGet V3 service index, and use `-target` to force a target framework selector such as `net8.0`, `netstandard2.0`, or `dotnetcore`. `-platform` remains an OS/arch selector and is not used to encode the .NET framework version. `hx` resolves the latest version from the flat container when needed, selects the most appropriate dependency group from the package `.nuspec`, then downloads the `.nupkg` files and extracts them like ZIP archives. `-registry` fragments are ignored; use `-target` explicitly instead.

For WinGet sources, use `winget://Package.Identifier` or `winget://Package.Identifier@version`. By default `hx` uses the GitHub API view of `microsoft/winget-pkgs`. Use `-registry` to point at a different GitHub manifests API root, or a GitHub repository URL that can be normalized to one. `hx` resolves the selected manifest version, chooses an installer matching `-platform` architecture, follows package dependencies declared in the manifest when present, then downloads the referenced installer artifacts and handles them like any other source.

For PyPI sources, use `pypi://package` or `pypi://package@version`. By default `hx` uses `https://pypi.org/pypi`. Use `-registry` to point at a different JSON-compatible PyPI registry base. `hx` resolves the selected release, follows `requires_dist` recursively in a conservative way, then prefers a wheel artifact and falls back to an sdist when needed.

For npm sources, use `npm://package`, `npm://package@version`, or `npm://package@dist-tag`. Use `-registry` to point at a different npm registry base URL. `hx` resolves package metadata from the npm registry, selects the requested version, then downloads the published tarball and handles it like any other remote archive.

For APT sources, use `apt://package` or `apt://package@version`. By default `hx` uses the Ubuntu archive at `https://archive.ubuntu.com/ubuntu` and, if no target is specified, picks the newest repository target that actually contains the requested package. Use `-registry` to point at a different APT base URL and `-target` to choose a repository-specific target such as `bionic`. `-platform` supplies the target architecture for APT package resolution. `-registry` fragments are ignored; use `-target` explicitly instead.

For RPM sources, use `rpm://package` or `rpm://package@version`. By default `hx` uses Fedora release repositories and picks the newest repository target exposed by the metadata. Use `-registry` to point at a different RPM repository base and `-target` to choose a repository-specific target such as `42`. `-platform` supplies the target architecture for RPM package resolution. `-registry` fragments are ignored; use `-target` explicitly instead.

For Alpine APK sources, use `apk://package` or `apk://package@version`. By default `hx` uses `https://dl-cdn.alpinelinux.org/alpine` and, if no target is specified, probes the repository and picks the newest `vX.Y` target that actually contains the requested package. Use `-registry` to point at a different Alpine base URL and `-target` to choose a repository-specific target such as `v3.22` or `edge`. Add `?component=community` to switch repository component. `-platform` supplies the target architecture for APK package resolution. `-registry` fragments are ignored; use `-target` explicitly instead.

For HTTPS sources, if certificate verification fails, `hx` emits a warning and retries insecurely instead of aborting the download.

## Streaming design

| Format | Strategy |
|--------|----------|
| tar-based over HTTP | True streaming: bytes flow TCP -> decompressor -> disk. Memory is O(1). |
| ZIP with `Accept-Ranges` | HTTP 206 range requests: only the central directory and active file are fetched. Peak memory stays near the Go runtime baseline (~15 MB). |
| ZIP without `Accept-Ranges` | Downloaded to a temp file on disk, then extracted. A `[warn]` line is printed. Use `-notmp 1` to buffer in memory instead. |
| Single-file compression (`.gz`, `.xz`, ...) | The stream is decompressed and written as a single output file inside `dest`, usually with the compression suffix removed. |
| Plain files | If no registered format matches, the source is copied into `dest` without extraction. |
| Local files | Read directly from disk. Local ZIP archives are extracted from the source file itself, so no HTTP buffering/temp-file fallback is involved. |
| `-download-only` | Bypasses extraction/decompression and writes the original source file into `dest`. For Docker registry images it downloads `manifest.json` plus the referenced blobs instead of applying the layers. For NuGet packages it downloads the resolved `.nupkg` files without extracting them. For WinGet packages it downloads the resolved installer artifacts without extracting them. For PyPI packages it downloads the resolved wheel/sdist artifacts without extracting them. For npm packages it downloads the published `.tgz` tarball without extracting it. For APT sources it downloads the resolved `.deb` files without unpacking them. For RPM and APK sources it downloads the resolved package files without unpacking them. |
| NuGet packages | The NuGet V3 service index is fetched, the flat container is used to resolve versions, the best matching dependency group is selected from the `.nuspec`, then each `.nupkg` is downloaded and extracted or copied with the normal archive/file pipeline. |
| WinGet packages | The WinGet manifests repository is queried through the GitHub contents API, the selected version manifest is parsed, an installer matching the selected architecture is chosen, then the referenced installer artifact is downloaded and extracted or copied with the normal archive/file pipeline. |
| PyPI packages | The PyPI JSON API is fetched for the selected project/release, `requires_dist` is traversed recursively, then each selected wheel or sdist is downloaded and extracted or copied with the normal archive/file pipeline. |
| npm packages | Package metadata is fetched from the npm registry, a version or dist-tag is resolved, then the published tarball is downloaded and extracted or copied with the normal archive/file pipeline. |
| APT repositories | The repository `Packages` index is fetched, a package plus its dependencies are resolved, then each `.deb` is downloaded and extracted or copied with the normal archive/file pipeline. |
| RPM repositories | Repository metadata is fetched from `repomd.xml`, dependencies are resolved from the primary metadata, then each `.rpm` is downloaded and either extracted or copied into `dest`. |
| Alpine APK repositories | `APKINDEX.tar.gz` is fetched from the selected release/component/architecture, dependencies are resolved from `P`/`D`/`p` fields, then each `.apk` is downloaded and either extracted or copied into `dest`. |
| Git repositories | Cloned into a temp directory with `go-git`, then only the checked-out worktree contents are copied into `dest` without leaving a usable `.git` directory behind. Default branch, branch, and tag downloads use a shallow clone; exact commit downloads may fall back to a broader fetch so the requested commit can be checked out. |
| Docker registry images | The image manifest is fetched from the registry API, the requested platform is selected, and each layer is streamed and applied directly into `dest` without temp files or a local container runtime. With `-download-only`, the selected manifest and original blobs are downloaded instead. |

## Building

Requires no pre-installed Go: the build scripts download and cache the toolchain automatically.

```sh
# Windows
build.bat

# Linux / macOS
chmod +x build.sh && ./build.sh
```

Output binaries land in `bin/`:

```text
bin/hx.exe   Windows AMD64, statically linked
bin/hx       Linux AMD64, statically linked
```

## Testing

Run the platform-native test script:

```sh
# Windows
test.ps1

# Linux / macOS
chmod +x test.sh && ./test.sh
```

Both test scripts build `hx` first through the repo build scripts (`build.ps1` or `build.sh`) so local testing and release automation use the same toolchain/bootstrap path.

## License

MIT
