package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/mholt/archives"
	rpmutils "github.com/sassoftware/go-rpmutils"
	rpmcpio "github.com/sassoftware/go-rpmutils/cpio"
)

var errUnsupportedWindowsPath = errors.New("unsupported Windows path")

type inputSource struct {
	display string
	id      string
	hint    string
	local   bool
	path    string
	git     *gitSource
	docker  *dockerSource
	npm     *npmSource
	apt     *aptSource
	rpm     *rpmSource
}

type gitSource struct {
	cloneURL string
	refKind  gitRefKind
	refValue string
}

type gitRefKind int

const (
	gitRefDefault gitRefKind = iota
	gitRefBranch
	gitRefTag
	gitRefCommit
)

type dockerSource struct {
	registry   string
	repository string
	reference  string
}

type platformSpec struct {
	raw        string
	os         string
	arch       string
	variant    string
	normalized string
}

type npmSource struct {
	registry string
	name     string
	selector string
}

type aptSource struct {
	baseURL   string
	dist      string
	component string
	pkg       string
	version   string
}

type rpmSource struct {
	name     string
	version  string
	registry string
}

const (
	defaultNPMRegistry = "https://registry.npmjs.org"
	defaultAPTRegistry = "https://archive.ubuntu.com/ubuntu"
)

func main() {
	skip := flag.Int("skip", 0, "strip N leading path components from each archive entry")
	symlinks := flag.Bool("symlinks", false, "extract symbolic links (skipped by default for safety)")
	quiet := flag.Bool("quiet", false, "plain text output instead of rich ANSI progress")
	downloadOnly := flag.Bool("download-only", false, "download/copy the original source without extracting it")
	noTempFile := flag.Bool("no-tempfile", false, "buffer non-Range ZIP in memory instead of a temp file")
	platform := flag.String("platform", defaultDockerPlatform(), "platform for registry images (for example linux/amd64)")
	registry := flag.String("registry", "", "override registry/repository base for docker, npm, or apt sources")

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: hx [flags] <source> [dest]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  source  HTTP/HTTPS URL, docker:// image reference, npm:// package reference, apt:// package reference, rpm:// package reference, Git repository URL, or local file path")
		fmt.Fprintln(os.Stderr, "  dest  destination folder (default: current directory); created if absent")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "flags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}

	sourceArg := args[0]
	dest := "."
	if len(args) >= 2 {
		dest = args[1]
	}

	src, err := resolveInputSource(sourceArg, *registry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot resolve source: %v\n", err)
		os.Exit(1)
	}

	absDest, err := filepath.Abs(dest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot resolve dest path: %v\n", err)
		os.Exit(1)
	}
	dest = absDest

	if *skip < 0 {
		fmt.Fprintln(os.Stderr, "-skip must be a non-negative integer")
		os.Exit(1)
	}

	doneFile := filepath.Join(dest, doneFileName(src.id, *skip, *symlinks, *downloadOnly, platformKey(src, *platform)))
	if _, err := os.Stat(doneFile); err == nil {
		fmt.Println("already extracted, skipping")
		return
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create destination: %v\n", err)
		os.Exit(1)
	}

	pr := newPrinter(!*quiet)
	_, err = run(src, dest, *skip, *symlinks, *downloadOnly, !*noTempFile, *platform, pr)
	pr.commit()
	if err != nil {
		fmt.Fprintf(os.Stderr, "extraction failed: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(doneFile, nil, 0o666); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create done file: %v\n", err)
		os.Exit(1)
	}

	pr.done()
}

type printer struct {
	ansi       bool
	start      time.Time
	inplace    bool
	lastRender time.Time

	dlBytes        int64
	dlTotal        int64
	fileCount      int
	totalExtracted int64
	lastFile       string
	lastSize       int64
}

const renderInterval = 100 * time.Millisecond

func newPrinter(ansi bool) *printer {
	return &printer{ansi: ansi, start: time.Now(), dlTotal: -1, lastSize: -1}
}

type tlsFallbackTransport struct {
	secure   *http.Transport
	insecure *http.Transport
	pr       *printer
	mu       sync.Mutex
	warned   map[string]bool
}

func newHTTPClient(pr *printer) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	secure := base.Clone()
	insecure := base.Clone()
	if insecure.TLSClientConfig == nil {
		insecure.TLSClientConfig = &tls.Config{}
	}
	insecure.TLSClientConfig = insecure.TLSClientConfig.Clone()
	insecure.TLSClientConfig.InsecureSkipVerify = true
	return &http.Client{
		Transport: &tlsFallbackTransport{
			secure:   secure,
			insecure: insecure,
			pr:       pr,
			warned:   map[string]bool{},
		},
	}
}

func (t *tlsFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.secure.RoundTrip(req)
	if err == nil || req.URL == nil || req.URL.Scheme != "https" || !isTLSError(err) {
		return resp, err
	}
	t.warn(req.URL.Host)
	retry := req.Clone(req.Context())
	retry.Header = req.Header.Clone()
	return t.insecure.RoundTrip(retry)
}

func (t *tlsFallbackTransport) warn(host string) {
	if t.pr == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.warned[host] {
		return
	}
	t.warned[host] = true
	t.pr.warn(fmt.Sprintf("TLS certificate verification failed for %s; retrying insecurely", host))
}

func isTLSError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "x509:") ||
		strings.Contains(msg, "certificate has expired") ||
		strings.Contains(msg, "certificate signed by unknown authority") ||
		strings.Contains(msg, "tls:")
}

func (p *printer) commit() {
	if p.inplace {
		fmt.Println()
		p.inplace = false
	}
}

func (p *printer) info(msg string) {
	p.commit()
	if p.ansi {
		if i := strings.IndexByte(msg, ':'); i > 0 && i < 12 {
			fmt.Printf("\033[2m%s\033[0m%s\n", msg[:i+1], msg[i+1:])
			return
		}
	}
	fmt.Println(msg)
}

func (p *printer) warn(msg string) {
	p.commit()
	if p.ansi {
		fmt.Printf("\033[1;33m[warn]\033[0;33m %s\033[0m\n", msg)
	} else {
		fmt.Println("[warn] " + msg)
	}
}

func (p *printer) done() {
	sizeInfo := ""
	if p.totalExtracted > 0 {
		sizeInfo = "  " + fmtBytes(p.totalExtracted)
	}
	elapsed := time.Since(p.start).Seconds()
	if p.ansi {
		fmt.Printf("\033[1;32mdone\033[0m  \033[1m%d files%s\033[0m  \033[2m(%.1fs)\033[0m\n",
			p.fileCount, sizeInfo, elapsed)
	} else {
		fmt.Printf("done  %d files%s  (%.1fs)\n", p.fileCount, sizeInfo, elapsed)
	}
}

func (p *printer) onDL(downloaded, total int64) {
	p.dlBytes = downloaded
	p.dlTotal = total
	p.render()
}

func (p *printer) onFile(name string, size int64) {
	p.fileCount++
	if size >= 0 {
		p.totalExtracted += size
	}
	p.lastFile = name
	p.lastSize = size
	if p.ansi {
		p.render()
	}
}

func (p *printer) render() {
	if !p.ansi {
		return
	}
	if time.Since(p.lastRender) < renderInterval {
		return
	}
	p.lastRender = time.Now()

	elapsed := time.Since(p.start).Seconds()
	var rate float64
	if elapsed > 0 && p.dlBytes > 0 {
		rate = float64(p.dlBytes) / elapsed
	}

	var line string
	if p.fileCount > 0 {
		sizeStr := ""
		if p.lastSize >= 0 {
			sizeStr = "  \033[2m" + fmtBytes(p.lastSize) + "\033[0m"
		}
		progress := fmt.Sprintf("  \033[32mfile %d  %s extracted\033[0m",
			p.fileCount, fmtBytes(p.totalExtracted))
		dlInfo := ""
		if p.dlTotal > 0 {
			pct := int(100 * p.dlBytes / p.dlTotal)
			dlInfo = fmt.Sprintf("  \033[2m[%s %d%% @ %s]\033[0m",
				progressBar(pct, 14), pct, fmtRate(rate))
		} else if p.dlBytes > 0 {
			dlInfo = fmt.Sprintf("  \033[2m[%s @ %s]\033[0m", fmtBytes(p.dlBytes), fmtRate(rate))
		}
		line = fmt.Sprintf("\033[1mExtracting\033[0m  \033[36m%-44s\033[0m%s%s%s",
			truncate(p.lastFile, 44), sizeStr, progress, dlInfo)
	} else {
		if p.dlTotal > 0 {
			pct := int(100 * p.dlBytes / p.dlTotal)
			bar := progressBar(pct, 28)
			eta := ""
			if rate > 0 && p.dlBytes < p.dlTotal {
				eta = "  \033[2mETA " + fmtDuration(float64(p.dlTotal-p.dlBytes)/rate) + "\033[0m"
			}
			line = fmt.Sprintf("\033[1;33mDownloading\033[0m  %s  \033[1m%3d%%\033[0m  %s / %s  \033[2m%s\033[0m%s",
				bar, pct, fmtBytes(p.dlBytes), fmtBytes(p.dlTotal), fmtRate(rate), eta)
		} else {
			line = fmt.Sprintf("\033[1;33mDownloading\033[0m  %s  \033[2m%s\033[0m",
				fmtBytes(p.dlBytes), fmtRate(rate))
		}
	}

	fmt.Print("\033[2K\r" + line)
	p.inplace = true
}

func run(src inputSource, dest string, skip int, symlinks, downloadOnly, useTempFile bool, platform string, pr *printer) (int, error) {
	if src.git != nil {
		return runGit(src, dest, symlinks, downloadOnly, pr)
	}
	if src.docker != nil {
		return runDocker(src, dest, skip, symlinks, downloadOnly, platform, pr)
	}
	if src.npm != nil {
		return runNPM(src, dest, skip, symlinks, downloadOnly, pr)
	}
	if src.apt != nil {
		return runAPT(src, dest, skip, symlinks, downloadOnly, platform, pr)
	}
	if src.rpm != nil {
		return runRPM(src, dest, skip, symlinks, downloadOnly, platform, pr)
	}
	if src.local {
		return runLocal(src, dest, skip, symlinks, downloadOnly, pr)
	}
	return runRemote(src, dest, skip, symlinks, downloadOnly, useTempFile, pr)
}

func runRemote(src inputSource, dest string, skip int, symlinks, downloadOnly, useTempFile bool, pr *printer) (int, error) {
	ctx := context.Background()
	client := newHTTPClient(pr)

	resp, err := client.Get(src.display)
	if err != nil {
		return 0, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("server returned %s", resp.Status)
	}

	pr.info(fmt.Sprintf("source: %s", src.display))

	var dlBytes int64
	tracked := &countReader{
		r: resp.Body,
		onRead: func(n int64) {
			dlBytes += n
			pr.onDL(dlBytes, resp.ContentLength)
		},
	}

	br := bufio.NewReaderSize(tracked, 1<<16)
	format, reader, err := archives.Identify(ctx, src.hint, br)
	if err != nil && !errors.Is(err, archives.NoMatch) {
		return 0, fmt.Errorf("identify format: %w", err)
	}

	return materializeSource(ctx, src, dest, skip, symlinks, downloadOnly, useTempFile, pr, format, reader, resp.ContentLength, resp, client, nil)
}

func runLocal(src inputSource, dest string, skip int, symlinks, downloadOnly bool, pr *printer) (int, error) {
	ctx := context.Background()

	f, err := os.Open(src.path)
	if err != nil {
		return 0, fmt.Errorf("open local source: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat local source: %w", err)
	}

	pr.info(fmt.Sprintf("source: %s", src.display))

	br := bufio.NewReaderSize(f, 1<<16)
	format, reader, err := archives.Identify(ctx, src.hint, br)
	if err != nil && !errors.Is(err, archives.NoMatch) {
		return 0, fmt.Errorf("identify format: %w", err)
	}

	return materializeSource(ctx, src, dest, skip, symlinks, downloadOnly, false, pr, format, reader, info.Size(), nil, nil, f)
}

func runGit(src inputSource, dest string, symlinks, downloadOnly bool, pr *printer) (int, error) {
	if downloadOnly {
		return 0, fmt.Errorf("-download-only is not supported for git sources")
	}

	ctx := context.Background()
	pr.info(fmt.Sprintf("source: %s", src.display))
	pr.info(fmt.Sprintf("format: git%s", gitRefInfo(src.git)))

	tmp, err := os.MkdirTemp("", "hx-git-*")
	if err != nil {
		return 0, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	cloneOpts := &git.CloneOptions{
		URL:          src.git.cloneURL,
		SingleBranch: src.git.refKind == gitRefBranch || src.git.refKind == gitRefTag,
		Depth:        1,
		Tags:         git.NoTags,
	}

	switch src.git.refKind {
	case gitRefBranch:
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(src.git.refValue)
	case gitRefTag:
		cloneOpts.ReferenceName = plumbing.NewTagReferenceName(src.git.refValue)
		cloneOpts.Tags = git.AllTags
	case gitRefCommit:
		cloneOpts.Depth = 0
		cloneOpts.SingleBranch = false
		cloneOpts.Tags = git.NoTags
	}

	repo, err := git.PlainCloneContext(ctx, tmp, false, cloneOpts)
	if err != nil {
		return 0, fmt.Errorf("git clone: %w", err)
	}

	if src.git.refKind == gitRefCommit {
		wt, err := repo.Worktree()
		if err != nil {
			return 0, fmt.Errorf("git worktree: %w", err)
		}
		if err := wt.Checkout(&git.CheckoutOptions{
			Hash:  plumbing.NewHash(src.git.refValue),
			Force: true,
		}); err != nil {
			return 0, fmt.Errorf("git checkout %s: %w", src.git.refValue, err)
		}
	}

	return copyGitWorktree(tmp, dest, symlinks, pr)
}

type dockerRegistryClient struct {
	client     *http.Client
	registry   string
	repository string
	token      string
}

type dockerManifest struct {
	SchemaVersion int                `json:"schemaVersion"`
	MediaType     string             `json:"mediaType"`
	Config        dockerDescriptor   `json:"config"`
	Layers        []dockerDescriptor `json:"layers"`
	Manifests     []dockerDescriptor `json:"manifests"`
}

type dockerDescriptor struct {
	MediaType string          `json:"mediaType"`
	Digest    string          `json:"digest"`
	Size      int64           `json:"size"`
	URLs      []string        `json:"urls"`
	Platform  *dockerPlatform `json:"platform"`
}

type dockerPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

type dockerTokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
}

type npmPackument struct {
	DistTags map[string]string          `json:"dist-tags"`
	Versions map[string]npmVersionEntry `json:"versions"`
}

type npmVersionEntry struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	Dist    npmDistInfo `json:"dist"`
}

type npmDistInfo struct {
	Tarball string `json:"tarball"`
}

type aptPackage struct {
	Package    string
	Version    string
	Arch       string
	Filename   string
	Depends    string
	PreDepends string
}

type aptRepoIndex struct {
	best map[string]aptPackage
	all  map[string][]aptPackage
}

type rpmRepoPackage struct {
	Name     string
	Arch     string
	Version  string
	Location string
	Provides []string
	Requires []string
}

type rpmRepoIndex struct {
	best       map[string]rpmRepoPackage
	all        map[string][]rpmRepoPackage
	provides   map[string]string
	allProvide map[string][]string
}

func runDocker(src inputSource, dest string, skip int, symlinks, downloadOnly bool, platformRaw string, pr *printer) (int, error) {
	platform, err := parsePlatform(platformRaw)
	if err != nil {
		return 0, err
	}

	ctx := context.Background()
	pr.info(fmt.Sprintf("source: %s", src.display))
	pr.info(fmt.Sprintf("format: docker  %s", platform.normalized))

	rc := &dockerRegistryClient{
		client:     newHTTPClient(pr),
		registry:   src.docker.registry,
		repository: src.docker.repository,
	}

	manifest, manifestBytes, err := fetchDockerManifest(ctx, rc, src.docker.reference, platform)
	if err != nil {
		return 0, err
	}
	if downloadOnly {
		return downloadDockerImage(ctx, rc, manifest, manifestBytes, dest, pr)
	}

	for _, layer := range manifest.Layers {
		if err := applyDockerLayer(ctx, rc, layer, dest, skip, symlinks, pr); err != nil {
			return 0, err
		}
	}
	return pr.fileCount, nil
}

func runNPM(src inputSource, dest string, skip int, symlinks, downloadOnly bool, pr *printer) (int, error) {
	ctx := context.Background()
	client := newHTTPClient(pr)

	entry, err := resolveNPMVersion(ctx, client, src.npm)
	if err != nil {
		return 0, err
	}

	pr.info(fmt.Sprintf("source: %s", src.display))
	pr.info(fmt.Sprintf("format: npm  %s@%s", entry.Name, entry.Version))

	resp, err := client.Get(entry.Dist.Tarball)
	if err != nil {
		return 0, fmt.Errorf("npm tarball download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("npm tarball download failed: %s", resp.Status)
	}

	var dlBytes int64
	tracked := &countReader{
		r: resp.Body,
		onRead: func(n int64) {
			dlBytes += n
			pr.onDL(dlBytes, resp.ContentLength)
		},
	}

	hint := filepath.Base(strings.SplitN(entry.Dist.Tarball, "?", 2)[0])
	if hint == "." || hint == "" || hint == "/" {
		hint = entry.Name + "-" + entry.Version + ".tgz"
	}

	br := bufio.NewReaderSize(tracked, 1<<16)
	format, reader, err := archives.Identify(ctx, hint, br)
	if err != nil && !errors.Is(err, archives.NoMatch) {
		return 0, fmt.Errorf("identify npm tarball: %w", err)
	}

	tarballSrc := inputSource{
		display: src.display,
		id:      src.id,
		hint:    hint,
	}
	return materializeSource(ctx, tarballSrc, dest, skip, symlinks, downloadOnly, true, pr, format, reader, resp.ContentLength, resp, client, nil)
}

func runAPT(src inputSource, dest string, skip int, symlinks, downloadOnly bool, platformRaw string, pr *printer) (int, error) {
	ctx := context.Background()
	client := newHTTPClient(pr)
	arch, err := aptArchFromPlatform(platformRaw)
	if err != nil {
		return 0, err
	}

	index, err := fetchAPTIndex(ctx, client, src.apt, arch)
	if err != nil {
		return 0, err
	}
	root, order, err := resolveAPTDependencies(src.apt, index)
	if err != nil {
		return 0, err
	}

	pr.info(fmt.Sprintf("source: %s", src.display))
	pr.info(fmt.Sprintf("format: apt  %s@%s  %s/%s", root.Package, root.Version, src.apt.dist, arch))

	for _, name := range order {
		pkg := index.best[name]
		if name == root.Package {
			pkg = root
		}
		if downloadOnly {
			if err := downloadAPTPackage(ctx, client, src.apt, pkg, dest, pr); err != nil {
				return 0, err
			}
			continue
		}
		if err := extractAPTPackage(ctx, client, src.apt, pkg, dest, skip, symlinks, pr); err != nil {
			return 0, err
		}
	}
	return pr.fileCount, nil
}

func runRPM(src inputSource, dest string, skip int, symlinks, downloadOnly bool, platformRaw string, pr *printer) (int, error) {
	ctx := context.Background()
	client := newHTTPClient(pr)
	arch, err := rpmArchFromPlatform(platformRaw)
	if err != nil {
		return 0, err
	}

	baseURL, err := resolveRPMRegistry(client, src.rpm.registry, arch)
	if err != nil {
		return 0, err
	}
	index, err := fetchRPMIndex(ctx, client, baseURL, arch)
	if err != nil {
		return 0, err
	}
	root, order, err := resolveRPMDependencies(src.rpm, index)
	if err != nil {
		return 0, err
	}

	pr.info(fmt.Sprintf("source: %s", src.display))
	pr.info(fmt.Sprintf("format: rpm  %s@%s  %s", root.Name, root.Version, arch))

	for _, name := range order {
		pkg := index.best[name]
		if name == root.Name {
			pkg = root
		}
		if downloadOnly {
			if err := downloadRPMPackage(ctx, client, baseURL, pkg, dest, pr); err != nil {
				return 0, err
			}
			continue
		}
		if err := extractRPMPackage(ctx, client, baseURL, pkg, dest, skip, symlinks, pr); err != nil {
			return 0, err
		}
	}
	return pr.fileCount, nil
}

func resolveNPMVersion(ctx context.Context, client *http.Client, src *npmSource) (npmVersionEntry, error) {
	u := strings.TrimRight(src.registry, "/") + "/" + url.PathEscape(src.name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return npmVersionEntry{}, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return npmVersionEntry{}, fmt.Errorf("npm metadata request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return npmVersionEntry{}, fmt.Errorf("npm metadata request failed: %s", resp.Status)
	}

	var pack npmPackument
	if err := json.NewDecoder(resp.Body).Decode(&pack); err != nil {
		return npmVersionEntry{}, fmt.Errorf("parse npm metadata: %w", err)
	}
	if len(pack.Versions) == 0 {
		return npmVersionEntry{}, fmt.Errorf("npm package %s has no versions", src.name)
	}

	selector := src.selector
	if selector == "" {
		selector = pack.DistTags["latest"]
		if selector == "" {
			return npmVersionEntry{}, fmt.Errorf("npm package %s does not expose a latest dist-tag", src.name)
		}
	}
	if v, ok := pack.Versions[selector]; ok {
		if v.Dist.Tarball == "" {
			return npmVersionEntry{}, fmt.Errorf("npm package %s@%s does not include a tarball URL", src.name, selector)
		}
		return v, nil
	}
	if version, ok := pack.DistTags[selector]; ok {
		v, ok := pack.Versions[version]
		if !ok {
			return npmVersionEntry{}, fmt.Errorf("npm dist-tag %s resolved to missing version %s", selector, version)
		}
		if v.Dist.Tarball == "" {
			return npmVersionEntry{}, fmt.Errorf("npm package %s@%s does not include a tarball URL", src.name, version)
		}
		return v, nil
	}
	return npmVersionEntry{}, fmt.Errorf("npm version or dist-tag %q not found for %s", selector, src.name)
}

func fetchAPTIndex(ctx context.Context, client *http.Client, src *aptSource, arch string) (*aptRepoIndex, error) {
	if src.dist == "" {
		dists, err := listAPTDistsByReleaseDate(client, src.baseURL)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, dist := range dists {
			trySrc := *src
			trySrc.dist = dist
			index, err := fetchAPTIndex(ctx, client, &trySrc, arch)
			if err != nil {
				lastErr = err
				continue
			}
			if _, ok := index.best[src.pkg]; ok {
				src.dist = dist
				return index, nil
			}
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("apt package %s not found in any release under %s", src.pkg, src.baseURL)
	}

	index := &aptRepoIndex{
		best: map[string]aptPackage{},
		all:  map[string][]aptPackage{},
	}
	for _, indexArch := range aptIndexArchitectures(arch) {
		body, err := fetchAPTIndexBytes(ctx, client, src, indexArch)
		if err != nil {
			if indexArch == "all" {
				continue
			}
			return nil, err
		}
		entries, err := parseAPTIndex(body, indexArch)
		if err != nil {
			return nil, err
		}
		for _, pkg := range entries {
			if pkg.Package == "" || pkg.Filename == "" {
				continue
			}
			index.all[pkg.Package] = append(index.all[pkg.Package], pkg)
			if _, exists := index.best[pkg.Package]; !exists || pkg.Arch == arch {
				index.best[pkg.Package] = pkg
			}
		}
	}
	return index, nil
}

func selectAPTRoot(src *aptSource, index *aptRepoIndex) (aptPackage, error) {
	entries := index.all[src.pkg]
	if len(entries) == 0 {
		return aptPackage{}, fmt.Errorf("apt package %s not found in repository index", src.pkg)
	}
	if src.version == "" {
		if pkg, ok := index.best[src.pkg]; ok {
			return pkg, nil
		}
		return entries[0], nil
	}
	for _, pkg := range entries {
		if pkg.Version == src.version {
			return pkg, nil
		}
	}
	return aptPackage{}, fmt.Errorf("apt package %s version %s not found in repository index", src.pkg, src.version)
}

func fetchAPTIndexBytes(ctx context.Context, client *http.Client, src *aptSource, arch string) ([]byte, error) {
	candidates := []string{
		fmt.Sprintf("%s/dists/%s/%s/binary-%s/Packages.xz", strings.TrimRight(src.baseURL, "/"), src.dist, src.component, arch),
		fmt.Sprintf("%s/dists/%s/%s/binary-%s/Packages.gz", strings.TrimRight(src.baseURL, "/"), src.dist, src.component, arch),
		fmt.Sprintf("%s/dists/%s/%s/binary-%s/Packages", strings.TrimRight(src.baseURL, "/"), src.dist, src.component, arch),
	}
	var lastErr error
	for _, u := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", u, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("apt index request failed for %s: %s", u, resp.Status)
			resp.Body.Close()
			continue
		}
		defer resp.Body.Close()

		br := bufio.NewReaderSize(resp.Body, 1<<16)
		format, reader, err := archives.Identify(ctx, filepath.Base(u), br)
		if err != nil && !errors.Is(err, archives.NoMatch) {
			return nil, fmt.Errorf("identify apt index: %w", err)
		}
		if dec, ok := format.(archives.Decompressor); ok {
			rc, err := dec.OpenReader(reader)
			if err != nil {
				return nil, fmt.Errorf("open apt index decompressor: %w", err)
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
		return io.ReadAll(reader)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no apt Packages index found")
	}
	return nil, lastErr
}

func parseAPTIndex(body []byte, arch string) ([]aptPackage, error) {
	chunks := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n\n")
	var out []aptPackage
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		fields := parseDeb822Fields(chunk)
		pkg := aptPackage{
			Package:    fields["Package"],
			Version:    fields["Version"],
			Arch:       fields["Architecture"],
			Filename:   fields["Filename"],
			Depends:    fields["Depends"],
			PreDepends: fields["Pre-Depends"],
		}
		if pkg.Arch == "" {
			pkg.Arch = arch
		}
		out = append(out, pkg)
	}
	return out, nil
}

func resolveAPTDependencies(src *aptSource, index *aptRepoIndex) (aptPackage, []string, error) {
	root, err := selectAPTRoot(src, index)
	if err != nil {
		return aptPackage{}, nil, err
	}
	var order []string
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return nil
		}
		pkg, ok := index.best[name]
		if !ok {
			return fmt.Errorf("apt dependency %s not found in repository index", name)
		}
		visiting[name] = true
		for _, dep := range parseAPTDepends(pkg.PreDepends, index.best) {
			if err := visit(dep); err != nil {
				return err
			}
		}
		for _, dep := range parseAPTDepends(pkg.Depends, index.best) {
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		order = append(order, name)
		return nil
	}
	if err := visit(root.Package); err != nil {
		return aptPackage{}, nil, err
	}
	return root, order, nil
}

func parseAPTDepends(raw string, index map[string]aptPackage) []string {
	var out []string
	for _, group := range strings.Split(raw, ",") {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		for _, alt := range strings.Split(group, "|") {
			name := cleanAPTDepName(alt)
			if name == "" {
				continue
			}
			if _, ok := index[name]; ok {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

func cleanAPTDepName(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, " "); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "("); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, ":"); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "["); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(raw)
}

func parseDeb822Fields(chunk string) map[string]string {
	fields := map[string]string{}
	var current string
	for _, line := range strings.Split(chunk, "\n") {
		if line == "" {
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && current != "" {
			fields[current] += "\n" + strings.TrimSpace(line)
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		current = strings.TrimSpace(key)
		fields[current] = strings.TrimSpace(value)
	}
	return fields
}

func aptIndexArchitectures(arch string) []string {
	if arch == "all" {
		return []string{"all"}
	}
	return []string{arch, "all"}
}

func detectLatestAPTDist(baseURL string) (string, error) {
	dists, err := listAPTDistsByReleaseDate(&http.Client{}, baseURL)
	if err != nil {
		return "", err
	}
	if len(dists) == 0 {
		return "", fmt.Errorf("could not detect latest apt release from %s; specify it via -registry ...#<release>", baseURL)
	}
	return dists[0], nil
}

func listAPTDistsByReleaseDate(client *http.Client, baseURL string) ([]string, error) {
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/dists/")
	if err != nil {
		return nil, fmt.Errorf("apt dists listing request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apt dists listing request failed: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read apt dists listing: %w", err)
	}
	candidates := parseAPTListingCandidates(string(body))
	type datedDist struct {
		name string
		date time.Time
	}
	var dated []datedDist
	for _, name := range candidates {
		t, err := fetchAPTReleaseDate(client, baseURL, name)
		if err != nil {
			continue
		}
		dated = append(dated, datedDist{name: name, date: t})
	}
	if len(dated) == 0 {
		return nil, fmt.Errorf("could not detect latest apt release from %s; specify it via -registry ...#<release>", baseURL)
	}
	for i := 0; i < len(dated)-1; i++ {
		for j := i + 1; j < len(dated); j++ {
			if dated[j].date.After(dated[i].date) {
				dated[i], dated[j] = dated[j], dated[i]
			}
		}
	}
	out := make([]string, 0, len(dated))
	for _, item := range dated {
		out = append(out, item.name)
	}
	return out, nil
}

func parseAPTListingCandidates(html string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(html, "href=\"") {
		if len(part) == len(html) {
			continue
		}
		target, _, _ := strings.Cut(part, "\"")
		target = strings.Trim(target, "/")
		if target == "" || strings.Contains(target, "/") || strings.HasPrefix(target, ".") {
			continue
		}
		switch target {
		case "by-hash", "partial":
			continue
		}
		if !seen[target] {
			seen[target] = true
			out = append(out, target)
		}
	}
	return out
}

func fetchAPTReleaseDate(client *http.Client, baseURL, dist string) (time.Time, error) {
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/dists/" + dist + "/Release")
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("release request failed: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, err
	}
	fields := parseDeb822Fields(strings.ReplaceAll(string(body), "\r\n", "\n"))
	dateValue := fields["Date"]
	if dateValue == "" {
		return time.Time{}, fmt.Errorf("release file missing Date")
	}
	t, err := time.Parse(time.RFC1123Z, dateValue)
	if err == nil {
		return t, nil
	}
	t, err = time.Parse("Mon, 02 Jan 2006 15:04:05 MST", dateValue)
	if err == nil {
		return t, nil
	}
	return time.Time{}, err
}

func downloadAPTPackage(ctx context.Context, client *http.Client, src *aptSource, pkg aptPackage, dest string, pr *printer) error {
	resp, err := fetchAPTFile(ctx, client, src, pkg.Filename)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	name := filepath.Base(pkg.Filename)
	pr.onFile(name, resp.ContentLength)
	return writeSingleFile(resp.Body, dest, name)
}

func extractAPTPackage(ctx context.Context, client *http.Client, src *aptSource, pkg aptPackage, dest string, skip int, symlinks bool, pr *printer) error {
	resp, err := fetchAPTFile(ctx, client, src, pkg.Filename)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return extractDeb(resp.Body, dest, skip, symlinks, pr)
}

func fetchAPTFile(ctx context.Context, client *http.Client, src *aptSource, filename string) (*http.Response, error) {
	u := strings.TrimRight(src.baseURL, "/") + "/" + strings.TrimLeft(filename, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apt package download: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("apt package download failed: %s", resp.Status)
	}
	return resp, nil
}

func extractDeb(r io.Reader, dest string, skip int, symlinks bool, pr *printer) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read deb: %w", err)
	}
	payload, name, err := debDataTar(data)
	if err != nil {
		return err
	}
	ctx := context.Background()
	br := bufio.NewReaderSize(bytes.NewReader(payload), 1<<16)
	format, reader, err := archives.Identify(ctx, name, br)
	if err != nil && !errors.Is(err, archives.NoMatch) {
		return fmt.Errorf("identify deb payload: %w", err)
	}
	if format == nil {
		return fmt.Errorf("deb payload %s is not a supported archive", name)
	}
	ex, ok := format.(archives.Extractor)
	if !ok {
		return fmt.Errorf("deb payload %s does not support extraction", name)
	}
	return ex.Extract(ctx, reader, func(ctx context.Context, f archives.FileInfo) error {
		return handleEntry(f, dest, skip, symlinks, pr)
	})
}

func fetchRPMIndex(ctx context.Context, client *http.Client, baseURL, arch string) (*rpmRepoIndex, error) {
	repomdURL := strings.TrimRight(baseURL, "/") + "/repodata/repomd.xml"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repomdURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rpm repomd request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rpm repomd request failed: %s", resp.Status)
	}
	var repomd struct {
		Data []struct {
			Type     string `xml:"type,attr"`
			Location struct {
				Href string `xml:"href,attr"`
			} `xml:"location"`
		} `xml:"data"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&repomd); err != nil {
		return nil, fmt.Errorf("parse rpm repomd: %w", err)
	}
	primaryHref := ""
	for _, data := range repomd.Data {
		if data.Type == "primary" {
			primaryHref = data.Location.Href
			break
		}
	}
	if primaryHref == "" {
		return nil, fmt.Errorf("rpm repository metadata does not expose primary data")
	}

	primaryURL := strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(primaryHref, "/")
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, primaryURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err = client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rpm primary request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rpm primary request failed: %s", resp.Status)
	}

	br := bufio.NewReaderSize(resp.Body, 1<<16)
	format, reader, err := archives.Identify(ctx, filepath.Base(primaryURL), br)
	if err != nil && !errors.Is(err, archives.NoMatch) {
		return nil, fmt.Errorf("identify rpm primary metadata: %w", err)
	}
	var raw io.Reader = reader
	if dec, ok := format.(archives.Decompressor); ok {
		rc, err := dec.OpenReader(reader)
		if err != nil {
			return nil, fmt.Errorf("open rpm primary decompressor: %w", err)
		}
		defer rc.Close()
		raw = rc
	}

	type rpmPrimaryEntry struct {
		Name    string `xml:"name"`
		Arch    string `xml:"arch"`
		Version struct {
			Epoch string `xml:"epoch,attr"`
			Ver   string `xml:"ver,attr"`
			Rel   string `xml:"rel,attr"`
		} `xml:"version"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
		Format struct {
			Provides struct {
				Entries []struct {
					Name string `xml:"name,attr"`
				} `xml:"entry"`
			} `xml:"provides"`
			Requires struct {
				Entries []struct {
					Name string `xml:"name,attr"`
				} `xml:"entry"`
			} `xml:"requires"`
		} `xml:"format"`
	}
	type rpmPrimary struct {
		Packages []rpmPrimaryEntry `xml:"package"`
	}
	var metadata rpmPrimary
	if err := xml.NewDecoder(raw).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("parse rpm primary metadata: %w", err)
	}

	index := &rpmRepoIndex{
		best:       map[string]rpmRepoPackage{},
		all:        map[string][]rpmRepoPackage{},
		provides:   map[string]string{},
		allProvide: map[string][]string{},
	}
	for _, entry := range metadata.Packages {
		if entry.Location.Href == "" || entry.Name == "" {
			continue
		}
		if entry.Arch != arch && entry.Arch != "noarch" {
			continue
		}
		pkg := rpmRepoPackage{
			Name:     entry.Name,
			Arch:     entry.Arch,
			Version:  rpmVersionString(entry.Version.Epoch, entry.Version.Ver, entry.Version.Rel),
			Location: entry.Location.Href,
		}
		for _, p := range entry.Format.Provides.Entries {
			if p.Name != "" {
				pkg.Provides = append(pkg.Provides, p.Name)
			}
		}
		for _, r := range entry.Format.Requires.Entries {
			if r.Name != "" && !strings.HasPrefix(r.Name, "rpmlib(") {
				pkg.Requires = append(pkg.Requires, r.Name)
			}
		}
		index.all[pkg.Name] = append(index.all[pkg.Name], pkg)
		if prev, ok := index.best[pkg.Name]; !ok || compareRPMVersion(pkg.Version, prev.Version) > 0 {
			index.best[pkg.Name] = pkg
		}
		for _, prov := range pkg.Provides {
			index.allProvide[prov] = append(index.allProvide[prov], pkg.Name)
			if _, ok := index.provides[prov]; !ok {
				index.provides[prov] = pkg.Name
			}
		}
		index.provides[pkg.Name] = pkg.Name
	}
	return index, nil
}

func resolveRPMDependencies(src *rpmSource, index *rpmRepoIndex) (rpmRepoPackage, []string, error) {
	root, err := selectRPMRoot(src, index)
	if err != nil {
		return rpmRepoPackage{}, nil, err
	}
	var order []string
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return nil
		}
		pkg, ok := index.best[name]
		if !ok {
			return fmt.Errorf("rpm dependency %s not found in repository metadata", name)
		}
		visiting[name] = true
		for _, dep := range pkg.Requires {
			target := resolveRPMProvide(dep, index)
			if target == "" {
				continue
			}
			if err := visit(target); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		order = append(order, name)
		return nil
	}
	if err := visit(root.Name); err != nil {
		return rpmRepoPackage{}, nil, err
	}
	return root, order, nil
}

func selectRPMRoot(src *rpmSource, index *rpmRepoIndex) (rpmRepoPackage, error) {
	entries := index.all[src.name]
	if len(entries) == 0 {
		return rpmRepoPackage{}, fmt.Errorf("rpm package %s not found in repository metadata", src.name)
	}
	if src.version == "" {
		return index.best[src.name], nil
	}
	for _, pkg := range entries {
		if pkg.Version == src.version || strings.Contains(pkg.Version, src.version) {
			return pkg, nil
		}
	}
	return rpmRepoPackage{}, fmt.Errorf("rpm package %s version %s not found in repository metadata", src.name, src.version)
}

func resolveRPMProvide(name string, index *rpmRepoIndex) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexAny(name, " ("); i >= 0 {
		name = name[:i]
	}
	if target, ok := index.provides[name]; ok {
		return target
	}
	return ""
}

func downloadRPMPackage(ctx context.Context, client *http.Client, baseURL string, pkg rpmRepoPackage, dest string, pr *printer) error {
	resp, err := fetchRPMFile(ctx, client, baseURL, pkg.Location)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	name := filepath.Base(pkg.Location)
	pr.onFile(name, resp.ContentLength)
	return writeSingleFile(resp.Body, dest, name)
}

func extractRPMPackage(ctx context.Context, client *http.Client, baseURL string, pkg rpmRepoPackage, dest string, skip int, symlinks bool, pr *printer) error {
	resp, err := fetchRPMFile(ctx, client, baseURL, pkg.Location)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return extractRPM(resp.Body, dest, skip, symlinks, pr)
}

func fetchRPMFile(ctx context.Context, client *http.Client, baseURL, location string) (*http.Response, error) {
	u := strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(location, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rpm package download: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("rpm package download failed: %s", resp.Status)
	}
	return resp, nil
}

func extractRPM(r io.Reader, dest string, skip int, symlinks bool, pr *printer) error {
	rpm, err := rpmutils.ReadRpm(r)
	if err != nil {
		return fmt.Errorf("read rpm: %w", err)
	}
	payload, err := rpm.PayloadReaderExtended()
	if err != nil {
		return fmt.Errorf("open rpm payload: %w", err)
	}
	for {
		info, err := payload.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read rpm payload: %w", err)
		}
		switch info.Mode() &^ 0o7777 {
		case rpmcpio.S_ISDIR:
			continue
		case rpmcpio.S_ISLNK:
			if unsupportedWindowsPath(info.Name(), skip) {
				pr.warn(fmt.Sprintf("skipping unsupported Windows path from rpm payload: %s", info.Name()))
				continue
			}
			if !symlinks {
				continue
			}
			pr.onFile(info.Name(), -1)
			if err := writeRPMSymlink(info, dest, skip); err != nil {
				return err
			}
		case rpmcpio.S_ISREG:
			if unsupportedWindowsPath(info.Name(), skip) {
				pr.warn(fmt.Sprintf("skipping unsupported Windows path from rpm payload: %s", info.Name()))
				continue
			}
			if payload.IsLink() {
				continue
			}
			pr.onFile(info.Name(), info.Size())
			if err := writeRPMRegularFile(payload, info, dest, skip); err != nil {
				return err
			}
		}
	}
}

func writeRPMRegularFile(r io.Reader, info rpmutils.FileInfo, dest string, skip int) error {
	if unsupportedWindowsPath(info.Name(), skip) {
		return nil
	}
	path, err := outPath(dest, info.Name(), skip)
	if errors.Is(err, errUnsupportedWindowsPath) {
		return nil
	}
	if err != nil || path == "" {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, r); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeRPMSymlink(info rpmutils.FileInfo, dest string, skip int) error {
	if unsupportedWindowsPath(info.Name(), skip) {
		return nil
	}
	path, err := outPath(dest, info.Name(), skip)
	if errors.Is(err, errUnsupportedWindowsPath) {
		return nil
	}
	if err != nil || path == "" {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	_ = os.Remove(path)
	if err := os.Symlink(info.Linkname(), path); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", path, info.Linkname(), err)
	}
	return nil
}

func unsupportedWindowsPath(name string, skip int) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	parts := entryParts(name, skip)
	for _, part := range parts {
		if strings.ContainsAny(part, `<>:"\|?*`) {
			return true
		}
	}
	return false
}

func rpmVersionString(epoch, ver, rel string) string {
	if rel != "" {
		ver = ver + "-" + rel
	}
	if epoch != "" && epoch != "0" {
		return epoch + ":" + ver
	}
	return ver
}

func compareRPMVersion(a, b string) int {
	return rpmutils.Vercmp(a, b)
}

func fetchDockerManifest(ctx context.Context, rc *dockerRegistryClient, reference string, platform platformSpec) (*dockerManifest, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rc.url("/manifests/"+reference), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))

	resp, err := rc.do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("docker manifest request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("docker manifest request failed: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read docker manifest: %w", err)
	}

	var manifest dockerManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, nil, fmt.Errorf("parse docker manifest: %w", err)
	}

	if isDockerManifestList(manifest) {
		desc, err := pickDockerManifest(manifest.Manifests, platform)
		if err != nil {
			return nil, nil, err
		}
		return fetchDockerManifest(ctx, rc, desc.Digest, platform)
	}
	if len(manifest.Layers) == 0 {
		return nil, nil, fmt.Errorf("unsupported docker manifest type")
	}
	return &manifest, body, nil
}

func downloadDockerImage(ctx context.Context, rc *dockerRegistryClient, manifest *dockerManifest, manifestBytes []byte, dest string, pr *printer) (int, error) {
	pr.onFile("manifest.json", int64(len(manifestBytes)))
	if err := writeSingleFile(bytes.NewReader(manifestBytes), dest, "manifest.json"); err != nil {
		return 0, err
	}

	if manifest.Config.Digest != "" {
		if err := downloadDockerBlob(ctx, rc, manifest.Config, dest, pr); err != nil {
			return 0, err
		}
	}
	for _, layer := range manifest.Layers {
		if err := downloadDockerBlob(ctx, rc, layer, dest, pr); err != nil {
			return 0, err
		}
	}
	return pr.fileCount, nil
}

func downloadDockerBlob(ctx context.Context, rc *dockerRegistryClient, desc dockerDescriptor, dest string, pr *printer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rc.url("/blobs/"+desc.Digest), nil)
	if err != nil {
		return err
	}
	resp, err := rc.do(req)
	if err != nil {
		return fmt.Errorf("docker blob request %s: %w", desc.Digest, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker blob request %s failed: %s", desc.Digest, resp.Status)
	}

	raw, verify, err := newVerifiedReader(resp.Body, desc.Digest)
	if err != nil {
		return err
	}
	blobPath, err := dockerBlobOutPath(dest, desc.Digest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	out, err := os.Create(blobPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", blobPath, err)
	}
	rel, _ := filepath.Rel(dest, blobPath)
	pr.onFile(filepath.ToSlash(rel), desc.Size)
	if _, err := io.Copy(out, raw); err != nil {
		out.Close()
		return fmt.Errorf("write %s: %w", blobPath, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", blobPath, err)
	}
	return verify()
}

func applyDockerLayer(ctx context.Context, rc *dockerRegistryClient, layer dockerDescriptor, dest string, skip int, symlinks bool, pr *printer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rc.url("/blobs/"+layer.Digest), nil)
	if err != nil {
		return err
	}
	resp, err := rc.do(req)
	if err != nil {
		return fmt.Errorf("docker blob request %s: %w", layer.Digest, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker blob request %s failed: %s", layer.Digest, resp.Status)
	}

	raw, verify, err := newVerifiedReader(resp.Body, layer.Digest)
	if err != nil {
		return err
	}

	br := bufio.NewReaderSize(raw, 1<<16)
	format, reader, err := archives.Identify(ctx, layerHint(layer), br)
	if err != nil && !errors.Is(err, archives.NoMatch) {
		return fmt.Errorf("identify docker layer %s: %w", layer.Digest, err)
	}

	if format == nil {
		return fmt.Errorf("unsupported docker layer format for %s", layer.Digest)
	}

	ex, ok := format.(archives.Extractor)
	if !ok {
		return fmt.Errorf("docker layer %s is not an archive", layer.Digest)
	}

	handler := func(ctx context.Context, f archives.FileInfo) error {
		return handleLayerEntry(f, dest, skip, symlinks, pr)
	}
	if err := ex.Extract(ctx, reader, handler); err != nil {
		return fmt.Errorf("extract docker layer %s: %w", layer.Digest, err)
	}
	if err := verify(); err != nil {
		return err
	}
	return nil
}

func (rc *dockerRegistryClient) url(path string) string {
	return "https://" + rc.registry + "/v2/" + rc.repository + path
}

func (rc *dockerRegistryClient) do(req *http.Request) (*http.Response, error) {
	if rc.token != "" {
		req.Header.Set("Authorization", "Bearer "+rc.token)
	}

	resp, err := rc.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	challenge := resp.Header.Get("WWW-Authenticate")
	resp.Body.Close()
	if err := rc.authorize(req.Context(), challenge); err != nil {
		return nil, err
	}

	retry := req.Clone(req.Context())
	retry.Header = req.Header.Clone()
	retry.Header.Set("Authorization", "Bearer "+rc.token)
	return rc.client.Do(retry)
}

func (rc *dockerRegistryClient) authorize(ctx context.Context, challenge string) error {
	realm, service, scope, err := parseBearerChallenge(challenge)
	if err != nil {
		return err
	}
	if scope == "" {
		scope = "repository:" + rc.repository + ":pull"
	}

	u, err := url.Parse(realm)
	if err != nil {
		return fmt.Errorf("parse auth realm: %w", err)
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	q.Set("scope", scope)
	q.Set("client_id", "hx")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := rc.client.Do(req)
	if err != nil {
		return fmt.Errorf("registry auth: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry auth failed: %s", resp.Status)
	}

	var tok dockerTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return fmt.Errorf("decode registry token: %w", err)
	}
	rc.token = tok.Token
	if rc.token == "" {
		rc.token = tok.AccessToken
	}
	if rc.token == "" {
		return fmt.Errorf("registry auth response did not include a bearer token")
	}
	return nil
}

func materializeSource(
	ctx context.Context,
	src inputSource,
	dest string,
	skip int,
	symlinks bool,
	downloadOnly bool,
	useTempFile bool,
	pr *printer,
	format archives.Format,
	reader io.Reader,
	size int64,
	resp *http.Response,
	client *http.Client,
	localFile *os.File,
) (int, error) {
	if downloadOnly {
		pr.info(fmt.Sprintf("format: file%s", formatSizeInfo(size)))
		name := singleFileName(src.hint, "")
		pr.onFile(name, size)
		return 1, writeSingleFile(reader, dest, name)
	}

	pr.info(fmt.Sprintf("format: %s%s", formatLabel(format), formatSizeInfo(size)))

	if format == nil {
		name := singleFileName(src.hint, "")
		pr.onFile(name, size)
		return 1, writeSingleFile(reader, dest, name)
	}

	if ex, ok := format.(archives.Extractor); ok {
		handler := func(ctx context.Context, f archives.FileInfo) error {
			return handleEntry(f, dest, skip, symlinks, pr)
		}
		if _, isZip := format.(archives.Zip); isZip {
			if resp != nil {
				return pr.fileCount, extractRemoteZip(ctx, resp, reader, client, useTempFile, pr, handler)
			}
			if localFile == nil {
				return 0, fmt.Errorf("local zip source not available")
			}
			if _, err := localFile.Seek(0, io.SeekStart); err != nil {
				return 0, fmt.Errorf("rewind local zip: %w", err)
			}
			return pr.fileCount, extractLocalZip(ctx, localFile, handler)
		}
		return pr.fileCount, ex.Extract(ctx, reader, handler)
	}

	if dec, ok := format.(archives.Decompressor); ok {
		rc, err := dec.OpenReader(reader)
		if err != nil {
			return 0, fmt.Errorf("open decompressor: %w", err)
		}
		defer rc.Close()

		name := singleFileName(src.hint, format.Extension())
		pr.onFile(name, -1)
		return 1, writeSingleFile(rc, dest, name)
	}

	name := singleFileName(src.hint, "")
	pr.onFile(name, size)
	return 1, writeSingleFile(reader, dest, name)
}

type countReader struct {
	r      io.Reader
	onRead func(n int64)
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.onRead(int64(n))
	}
	return n, err
}

func extractRemoteZip(
	ctx context.Context,
	resp *http.Response,
	fallback io.Reader,
	client *http.Client,
	useTempFile bool,
	pr *printer,
	handler archives.FileHandler,
) error {
	ex := archives.Zip{}

	if resp.Header.Get("Accept-Ranges") == "bytes" && resp.ContentLength > 0 {
		resp.Body.Close()
		finalURL := resp.Request.URL.String()
		rr := &httpRangeReader{ctx: ctx, url: finalURL, size: resp.ContentLength, client: client, pr: pr}
		return ex.Extract(ctx, rr, handler)
	}

	reason := "no Accept-Ranges: bytes"
	if resp.ContentLength <= 0 {
		reason += ", no Content-Length"
	}

	if useTempFile {
		tmp, err := os.CreateTemp("", "hx-*.zip")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		defer func() { tmp.Close(); os.Remove(tmp.Name()) }()

		pr.warn(fmt.Sprintf(
			"server does not support HTTP Range (%s); downloading to temp file %s",
			reason, tmp.Name()))

		if _, err := io.Copy(tmp, fallback); err != nil {
			return fmt.Errorf("download to temp file: %w", err)
		}
		pr.commit()

		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek temp file: %w", err)
		}
		return ex.Extract(ctx, tmp, handler)
	}

	pr.warn(fmt.Sprintf(
		"server does not support HTTP Range (%s); buffering archive in memory (-no-tempfile set)",
		reason))
	data, err := io.ReadAll(fallback)
	if err != nil {
		return fmt.Errorf("buffer zip: %w", err)
	}
	return ex.Extract(ctx, bytes.NewReader(data), handler)
}

func extractLocalZip(ctx context.Context, f *os.File, handler archives.FileHandler) error {
	return (archives.Zip{}).Extract(ctx, f, handler)
}

func handleEntry(f archives.FileInfo, dest string, skip int, allowSymlinks bool, pr *printer) error {
	if f.IsDir() {
		return nil
	}
	if f.LinkTarget != "" {
		if !allowSymlinks {
			return nil
		}
		pr.onFile(f.NameInArchive, -1)
		return writeSymlink(f, dest, skip)
	}
	pr.onFile(f.NameInArchive, f.Size())
	return writeRegularFile(f, dest, skip)
}

func writeRegularFile(f archives.FileInfo, dest string, skip int) error {
	path, err := outPath(dest, f.NameInArchive, skip)
	if err != nil || path == "" {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", f.NameInArchive, err)
	}
	defer rc.Close()
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeSingleFile(r io.Reader, dest, name string) error {
	path, err := singleFileOutPath(dest, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, r); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeSymlink(f archives.FileInfo, dest string, skip int) error {
	path, err := outPath(dest, f.NameInArchive, skip)
	if err != nil || path == "" {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	_ = os.Remove(path)
	if err := os.Symlink(f.LinkTarget, path); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", path, f.LinkTarget, err)
	}
	return nil
}

func copyGitWorktree(srcRoot, dest string, allowSymlinks bool, pr *printer) (int, error) {
	err := filepath.Walk(srcRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(os.PathSeparator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		target := filepath.Join(dest, rel)
		cleanDest := filepath.Clean(dest) + string(os.PathSeparator)
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), cleanDest) {
			return fmt.Errorf("path traversal blocked: %s", rel)
		}

		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		if info.Mode()&os.ModeSymlink != 0 {
			if !allowSymlinks {
				return nil
			}
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			pr.onFile(filepath.ToSlash(rel), -1)
			return os.Symlink(linkTarget, target)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		pr.onFile(filepath.ToSlash(rel), info.Size())
		return nil
	})
	return pr.fileCount, err
}

type httpRangeReader struct {
	ctx     context.Context
	url     string
	size    int64
	client  *http.Client
	pr      *printer
	fetched int64
	pos     int64
}

func (r *httpRangeReader) Size() int64 { return r.size }

func (r *httpRangeReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, fmt.Errorf("unknown whence: %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("seek to negative offset")
	}
	r.pos = abs
	return abs, nil
}

func (r *httpRangeReader) Read(p []byte) (int, error) {
	n, err := r.ReadAt(p, r.pos)
	r.pos += int64(n)
	return n, err
}

func (r *httpRangeReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}
	end := off + int64(len(p)) - 1
	clampedToEOF := false
	if end >= r.size {
		end = r.size - 1
		clampedToEOF = true
	}

	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return 0, fmt.Errorf("build range request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("range request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("expected 206 Partial Content, got %s", resp.Status)
	}

	want := int(end-off) + 1
	n, err := io.ReadFull(resp.Body, p[:want])
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	if err == nil && clampedToEOF {
		err = io.EOF
	}
	if n > 0 && r.pr != nil {
		r.fetched += int64(n)
		r.pr.onDL(r.fetched, r.size)
	}
	return n, err
}

func doneFileName(sourceID string, skip int, symlinks, downloadOnly bool, platform string) string {
	sl := 0
	if symlinks {
		sl = 1
	}
	dl := 0
	if downloadOnly {
		dl = 1
	}
	pf := ""
	if platform != "" {
		pf = "-plat" + sanitizeForFilename(platform)
	}
	return fmt.Sprintf("hx-%s-skip%d-sym%d-dl%d%sargs.done", sanitizeForFilename(sourceID), skip, sl, dl, pf)
}

func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := b.String()
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func entryParts(nameInArchive string, skip int) []string {
	parts := strings.Split(filepath.ToSlash(nameInArchive), "/")
	kept := parts[:0]
	for _, p := range parts {
		if p != "" && p != "." {
			kept = append(kept, p)
		}
	}
	parts = kept
	if skip >= len(parts) {
		return nil
	}
	return parts[skip:]
}

func outPath(dest, nameInArchive string, skip int) (string, error) {
	parts := entryParts(nameInArchive, skip)
	if len(parts) == 0 {
		return "", nil
	}
	if runtime.GOOS == "windows" {
		for _, part := range parts {
			if strings.ContainsAny(part, `<>:"\|?*`) {
				return "", errUnsupportedWindowsPath
			}
		}
	}
	rel := filepath.Join(parts...)
	if rel == "" || rel == "." {
		return "", nil
	}
	full := filepath.Join(dest, rel)
	cleanDest := filepath.Clean(dest) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(full)+string(os.PathSeparator), cleanDest) {
		return "", fmt.Errorf("path traversal blocked: %s", nameInArchive)
	}
	return full, nil
}

func singleFileOutPath(dest, name string) (string, error) {
	base := filepath.Base(name)
	if base == "." || base == string(os.PathSeparator) || base == "" {
		base = "download"
	}
	full := filepath.Join(dest, base)
	cleanDest := filepath.Clean(dest) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(full)+string(os.PathSeparator), cleanDest) {
		return "", fmt.Errorf("path traversal blocked: %s", name)
	}
	return full, nil
}

func dockerBlobOutPath(dest, digest string) (string, error) {
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok || algo == "" || hex == "" {
		return "", fmt.Errorf("invalid digest %q", digest)
	}
	full := filepath.Join(dest, "blobs", algo, hex)
	cleanDest := filepath.Clean(dest) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(full)+string(os.PathSeparator), cleanDest) {
		return "", fmt.Errorf("path traversal blocked: %s", digest)
	}
	return full, nil
}

func singleFileName(hint, compressionExt string) string {
	name := filepath.Base(hint)
	if name == "." || name == "" {
		name = "download"
	}
	if compressionExt != "" && strings.HasSuffix(strings.ToLower(name), strings.ToLower(compressionExt)) {
		name = name[:len(name)-len(compressionExt)]
	}
	if name == "" || name == "." {
		name = "download"
	}
	return name
}

func formatLabel(format archives.Format) string {
	if format == nil {
		return "file"
	}
	ext := strings.Trim(format.Extension(), ".")
	if ext == "" {
		return "file"
	}
	return ext
}

func formatSizeInfo(size int64) string {
	if size > 0 {
		return "  " + fmtBytes(size)
	}
	return ""
}

func resolveInputSource(arg, registry string) (inputSource, error) {
	if rs, ok, err := parseRPMSource(arg, registry); err != nil {
		return inputSource{}, err
	} else if ok {
		return inputSource{
			display: arg,
			id:      rpmSourceID(rs),
			hint:    rs.name + ".rpm",
			rpm:     rs,
		}, nil
	}

	if as, ok, err := parseAPTSource(arg, registry); err != nil {
		return inputSource{}, err
	} else if ok {
		return inputSource{
			display: arg,
			id:      aptSourceID(as),
			hint:    as.pkg + ".deb",
			apt:     as,
		}, nil
	}

	if ns, ok, err := parseNPMSource(arg, registry); err != nil {
		return inputSource{}, err
	} else if ok {
		return inputSource{
			display: arg,
			id:      npmSourceID(ns),
			hint:    npmHint(ns),
			npm:     ns,
		}, nil
	}

	if ds, ok, err := parseDockerSource(arg, registry); err != nil {
		return inputSource{}, err
	} else if ok {
		return inputSource{
			display: arg,
			id:      dockerSourceID(ds),
			hint:    filepath.Base(ds.repository),
			docker:  ds,
		}, nil
	}

	if gs, ok, err := parseGitSource(arg); err != nil {
		return inputSource{}, err
	} else if ok {
		return inputSource{
			display: arg,
			id:      gitSourceID(gs),
			hint:    filepath.Base(strings.TrimSuffix(gs.cloneURL, ".git")),
			git:     gs,
		}, nil
	}

	if isRemoteSource(arg) {
		return inputSource{
			display: arg,
			id:      arg,
			hint:    filepath.Base(strings.SplitN(arg, "?", 2)[0]),
		}, nil
	}

	absPath, err := filepath.Abs(arg)
	if err != nil {
		return inputSource{}, err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return inputSource{}, err
	}
	if info.IsDir() {
		return inputSource{}, fmt.Errorf("%s is a directory, expected an archive file", arg)
	}

	return inputSource{
		display: absPath,
		id:      absPath,
		hint:    filepath.Base(absPath),
		local:   true,
		path:    absPath,
	}, nil
}

func isRemoteSource(s string) bool {
	s = strings.ToLower(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func parseRPMSource(raw, registry string) (*rpmSource, bool, error) {
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "rpm:\\") {
		raw = "rpm://" + raw[len("rpm:\\"):]
		lower = strings.ToLower(raw)
	}
	if !strings.HasPrefix(lower, "rpm://") {
		return nil, false, nil
	}
	ref := raw[len("rpm://"):]
	if ref == "" {
		return nil, false, fmt.Errorf("empty rpm source")
	}
	name := ref
	version := ""
	if i := strings.LastIndex(ref, "@"); i > 0 {
		name = ref[:i]
		version = ref[i+1:]
	}
	if name == "" {
		return nil, false, fmt.Errorf("invalid rpm source")
	}
	return &rpmSource{name: name, version: version, registry: registry}, true, nil
}

func parseNPMSource(raw, registryOverride string) (*npmSource, bool, error) {
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "npm:\\") {
		raw = "npm://" + raw[len("npm:\\"):]
		lower = strings.ToLower(raw)
	}
	if !strings.HasPrefix(lower, "npm://") {
		return nil, false, nil
	}
	ref := raw[len("npm://"):]
	if ref == "" {
		return nil, false, fmt.Errorf("empty npm source")
	}

	name := ref
	selector := ""
	if i := strings.LastIndex(ref, "@"); i > 0 {
		slash := strings.LastIndex(ref, "/")
		if i > slash {
			name = ref[:i]
			selector = ref[i+1:]
		}
	}
	if name == "" {
		return nil, false, fmt.Errorf("invalid npm source")
	}

	return &npmSource{
		registry: normalizeNPMRegistry(registryOverride),
		name:     name,
		selector: selector,
	}, true, nil
}

func parseAPTSource(raw, registryOverride string) (*aptSource, bool, error) {
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "apt:\\") {
		raw = "apt://" + raw[len("apt:\\"):]
		lower = strings.ToLower(raw)
	}
	if !strings.HasPrefix(lower, "apt://") {
		return nil, false, nil
	}
	ref := raw[len("apt://"):]
	if ref == "" {
		return nil, false, fmt.Errorf("empty apt source")
	}

	pkg := ref
	version := ""
	if i := strings.LastIndex(ref, "@"); i > 0 {
		pkg = ref[:i]
		version = ref[i+1:]
	}
	if pkg == "" {
		return nil, false, fmt.Errorf("invalid apt source")
	}

	baseURL, dist, component, err := resolveAPTRegistry(registryOverride)
	if err != nil {
		return nil, false, err
	}
	return &aptSource{
		baseURL:   baseURL,
		dist:      dist,
		component: component,
		pkg:       pkg,
		version:   version,
	}, true, nil
}

func parseDockerSource(raw, registryOverride string) (*dockerSource, bool, error) {
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "docker://") && !strings.HasPrefix(lower, "oci://") {
		return nil, false, nil
	}

	ref := raw[strings.Index(raw, "://")+3:]
	if ref == "" {
		return nil, false, fmt.Errorf("empty docker source")
	}

	var digest string
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		digest = ref[i+1:]
		ref = ref[:i]
	}

	tag := ""
	lastSlash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > lastSlash {
		tag = ref[i+1:]
		ref = ref[:i]
	}

	parts := strings.Split(ref, "/")
	if len(parts) == 0 || parts[0] == "" {
		return nil, false, fmt.Errorf("invalid docker source")
	}

	registry := ""
	repoParts := parts
	if len(parts) > 1 && looksLikeRegistryHost(parts[0]) {
		registry = parts[0]
		repoParts = parts[1:]
	}
	if override := normalizeDockerRegistry(registryOverride); override != "" {
		registry = override
	}
	if registry == "" {
		registry = "registry-1.docker.io"
		if len(repoParts) == 1 {
			repoParts = []string{"library", repoParts[0]}
		}
	} else if len(repoParts) == 1 && registry == "registry-1.docker.io" {
		repoParts = []string{"library", repoParts[0]}
	}
	repository := strings.Join(repoParts, "/")
	if repository == "" {
		return nil, false, fmt.Errorf("invalid docker repository")
	}

	reference := digest
	if reference == "" {
		reference = tag
	}
	if reference == "" {
		reference = "latest"
	}

	return &dockerSource{
		registry:   registry,
		repository: repository,
		reference:  reference,
	}, true, nil
}

func parseGitSource(raw string) (*gitSource, bool, error) {
	if strings.HasPrefix(strings.ToLower(raw), "git+http://") || strings.HasPrefix(strings.ToLower(raw), "git+https://") {
		raw = raw[4:]
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, false, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false, nil
	}

	if gs := parseGenericGitURL(u); gs != nil {
		return gs, true, nil
	}
	if gs := parseForgeGitURL(u); gs != nil {
		return gs, true, nil
	}
	return nil, false, nil
}

func parseGenericGitURL(u *url.URL) *gitSource {
	path := strings.TrimSuffix(u.Path, "/")
	if !strings.HasSuffix(strings.ToLower(path), ".git") {
		return nil
	}
	cloneURL := *u
	cloneURL.RawQuery = ""
	cloneURL.Fragment = ""

	gs := &gitSource{
		cloneURL: cloneURL.String(),
		refKind:  gitRefDefault,
	}
	applyGitRefSelectors(gs, u)
	return gs
}

func parseForgeGitURL(u *url.URL) *gitSource {
	host := strings.ToLower(u.Host)
	if host != "github.com" {
		return nil
	}

	parts := splitURLPath(u.Path)
	if len(parts) < 2 {
		return nil
	}

	repoPath := parts[:2]
	rest := parts[2:]
	repoName := repoPath[1]
	if strings.HasSuffix(repoName, ".git") {
		repoPath[1] = strings.TrimSuffix(repoName, ".git")
		repoName = repoPath[1]
	}

	cloneBase := *u
	cloneBase.Path = "/" + strings.Join(repoPath, "/") + ".git"
	cloneBase.RawQuery = ""
	cloneBase.Fragment = ""

	gs := &gitSource{
		cloneURL: cloneBase.String(),
		refKind:  gitRefDefault,
	}
	matched := len(rest) == 0

	if len(rest) == 2 && rest[0] == "tree" {
		gs.refKind = gitRefBranch
		gs.refValue = rest[1]
		matched = true
	}
	if len(rest) == 2 && rest[0] == "commit" {
		gs.refKind = gitRefCommit
		gs.refValue = rest[1]
		matched = true
	}
	if len(rest) >= 3 && rest[0] == "releases" && rest[1] == "tag" {
		gs.refKind = gitRefTag
		gs.refValue = strings.Join(rest[2:], "/")
		matched = true
	}

	applyGitRefSelectors(gs, u)
	if matched || gs.refValue != "" {
		return gs
	}
	return nil
}

func applyGitRefSelectors(gs *gitSource, u *url.URL) {
	if value := u.Query().Get("branch"); value != "" {
		gs.refKind, gs.refValue = gitRefBranch, value
		return
	}
	if value := u.Query().Get("tag"); value != "" {
		gs.refKind, gs.refValue = gitRefTag, value
		return
	}
	if value := u.Query().Get("commit"); value != "" {
		gs.refKind, gs.refValue = gitRefCommit, value
		return
	}
	if value := u.Query().Get("ref"); value != "" {
		gs.refKind, gs.refValue = classifyGitRef(value)
		return
	}

	frag := u.Fragment
	if frag == "" {
		return
	}
	switch {
	case strings.HasPrefix(frag, "branch="):
		gs.refKind, gs.refValue = gitRefBranch, strings.TrimPrefix(frag, "branch=")
	case strings.HasPrefix(frag, "tag="):
		gs.refKind, gs.refValue = gitRefTag, strings.TrimPrefix(frag, "tag=")
	case strings.HasPrefix(frag, "commit="):
		gs.refKind, gs.refValue = gitRefCommit, strings.TrimPrefix(frag, "commit=")
	case strings.HasPrefix(frag, "ref="):
		gs.refKind, gs.refValue = classifyGitRef(strings.TrimPrefix(frag, "ref="))
	default:
		gs.refKind, gs.refValue = classifyGitRef(frag)
	}
}

func classifyGitRef(value string) (gitRefKind, string) {
	switch {
	case isLikelyCommitHash(value):
		return gitRefCommit, value
	case strings.HasPrefix(value, "refs/tags/"):
		return gitRefTag, strings.TrimPrefix(value, "refs/tags/")
	case strings.HasPrefix(value, "refs/heads/"):
		return gitRefBranch, strings.TrimPrefix(value, "refs/heads/")
	default:
		return gitRefBranch, value
	}
}

func gitSourceID(gs *gitSource) string {
	switch gs.refKind {
	case gitRefBranch:
		return gs.cloneURL + "#branch=" + gs.refValue
	case gitRefTag:
		return gs.cloneURL + "#tag=" + gs.refValue
	case gitRefCommit:
		return gs.cloneURL + "#commit=" + gs.refValue
	default:
		return gs.cloneURL
	}
}

func gitRefInfo(gs *gitSource) string {
	switch gs.refKind {
	case gitRefBranch:
		return "  branch " + gs.refValue
	case gitRefTag:
		return "  tag " + gs.refValue
	case gitRefCommit:
		return "  commit " + shortHash(gs.refValue)
	default:
		return ""
	}
}

func splitURLPath(p string) []string {
	raw := strings.Split(strings.Trim(p, "/"), "/")
	out := raw[:0]
	for _, part := range raw {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func isLikelyCommitHash(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func dockerSourceID(ds *dockerSource) string {
	return "docker://" + ds.registry + "/" + ds.repository + "@" + ds.reference
}

func npmSourceID(ns *npmSource) string {
	if ns.selector == "" {
		return "npm://" + ns.name
	}
	return "npm://" + ns.name + "@" + ns.selector
}

func rpmSourceID(rs *rpmSource) string {
	if rs.version == "" {
		return "rpm://" + rs.name
	}
	return "rpm://" + rs.name + "@" + rs.version
}

func npmHint(ns *npmSource) string {
	base := filepath.Base(ns.name)
	base = strings.TrimPrefix(base, "@")
	if base == "" || base == "." {
		base = "package"
	}
	if ns.selector != "" {
		return base + "-" + ns.selector + ".tgz"
	}
	return base + ".tgz"
}

func aptSourceID(as *aptSource) string {
	dist := as.dist
	if dist == "" {
		dist = "latest"
	}
	if as.version != "" {
		return fmt.Sprintf("apt://%s@%s#%s?component=%s", as.pkg, as.version, dist, as.component)
	}
	return fmt.Sprintf("apt://%s#%s?component=%s", as.pkg, dist, as.component)
}

func platformKey(src inputSource, raw string) string {
	if src.docker == nil && src.apt == nil {
		return ""
	}
	if src.docker != nil {
		p, err := parsePlatform(raw)
		if err != nil {
			return raw
		}
		return p.normalized
	}
	arch, err := aptArchFromPlatform(raw)
	if err != nil {
		return raw
	}
	return arch
}

func normalizeNPMRegistry(raw string) string {
	if raw == "" {
		return defaultNPMRegistry
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return strings.TrimRight(raw, "/")
}

func normalizeDockerRegistry(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		if u.Host == "" {
			return ""
		}
		return u.Host
	}
	return strings.TrimRight(raw, "/")
}

func resolveAPTRegistry(raw string) (baseURL, dist, component string, err error) {
	reg := raw
	if reg == "" {
		reg = defaultAPTRegistry
	}
	if !strings.Contains(reg, "://") {
		reg = "https://" + reg
	}
	u, err := url.Parse(reg)
	if err != nil {
		return "", "", "", err
	}
	component = u.Query().Get("component")
	if component == "" {
		component = "main"
	}
	dist = strings.Trim(u.Fragment, "/")
	u.RawQuery = ""
	u.Fragment = ""
	baseURL = strings.TrimRight(u.String(), "/")
	return baseURL, dist, component, nil
}

func aptArchFromPlatform(raw string) (string, error) {
	p, err := parsePlatform(raw)
	if err != nil {
		return "", err
	}
	switch p.arch {
	case "amd64":
		return "amd64", nil
	case "386":
		return "i386", nil
	case "arm64":
		return "arm64", nil
	case "arm":
		if p.variant == "v5" {
			return "armel", nil
		}
		return "armhf", nil
	default:
		return p.arch, nil
	}
}

func rpmArchFromPlatform(raw string) (string, error) {
	p, err := parsePlatform(raw)
	if err != nil {
		return "", err
	}
	switch p.arch {
	case "amd64":
		return "x86_64", nil
	case "386":
		return "i686", nil
	case "arm64":
		return "aarch64", nil
	case "arm":
		return "armhfp", nil
	default:
		return p.arch, nil
	}
}

func resolveRPMRegistry(client *http.Client, raw, arch string) (string, error) {
	if raw != "" {
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			return "", err
		}
		if strings.Contains(u.Path, "/repodata") || strings.HasSuffix(u.Path, "/os/") || strings.HasSuffix(u.Path, "/os") {
			return strings.TrimRight(u.String(), "/"), nil
		}
		dist := strings.Trim(u.Fragment, "/")
		u.Fragment = ""
		u.RawQuery = ""
		base := strings.TrimRight(u.String(), "/")
		if dist != "" {
			return fmt.Sprintf("%s/%s/Everything/%s/os", base, dist, arch), nil
		}
	}
	base := "https://mirrors.kernel.org/fedora/releases"
	rel, err := detectLatestFedoraRelease(client, arch)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/Everything/%s/os", base, rel, arch), nil
}

func detectLatestFedoraRelease(client *http.Client, arch string) (string, error) {
	resp, err := client.Get("https://fedoraproject.org/releases.json")
	if err != nil {
		return "", fmt.Errorf("fedora releases metadata request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fedora releases metadata request failed: %s", resp.Status)
	}
	var releases []struct {
		Version string `json:"version"`
		Arch    string `json:"arch"`
		Link    string `json:"link"`
		Variant string `json:"variant"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", fmt.Errorf("parse fedora releases metadata: %w", err)
	}
	best := -1
	for _, item := range releases {
		if item.Arch != arch || item.Variant != "Everything" {
			continue
		}
		if strings.Contains(strings.ToLower(item.Version), "beta") || strings.Contains(item.Link, "/test/") {
			continue
		}
		cand := strings.TrimSpace(item.Version)
		var n int
		if _, err := fmt.Sscanf(cand, "%d", &n); err != nil {
			continue
		}
		if n > best {
			best = n
		}
	}
	if best < 0 {
		return "", fmt.Errorf("could not detect latest Fedora release for arch %s", arch)
	}
	return fmt.Sprintf("%d", best), nil
}

func defaultDockerPlatform() string {
	arch := runtime.GOARCH
	if arch == "" {
		arch = "amd64"
	}
	return "linux/" + arch
}

func parsePlatform(raw string) (platformSpec, error) {
	parts := strings.Split(raw, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return platformSpec{}, fmt.Errorf("invalid platform %q, expected os/arch or os/arch/variant", raw)
	}
	p := platformSpec{
		raw:     raw,
		os:      parts[0],
		arch:    parts[1],
		variant: "",
	}
	if p.os == "" || p.arch == "" {
		return platformSpec{}, fmt.Errorf("invalid platform %q, expected os/arch or os/arch/variant", raw)
	}
	if len(parts) == 3 {
		p.variant = parts[2]
	}
	p.normalized = p.os + "/" + p.arch
	if p.variant != "" {
		p.normalized += "/" + p.variant
	}
	return p, nil
}

func looksLikeRegistryHost(s string) bool {
	return s == "localhost" || strings.Contains(s, ".") || strings.Contains(s, ":")
}

func isDockerManifestList(m dockerManifest) bool {
	if len(m.Manifests) > 0 {
		return true
	}
	switch m.MediaType {
	case "application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json":
		return true
	default:
		return false
	}
}

func pickDockerManifest(descs []dockerDescriptor, platform platformSpec) (dockerDescriptor, error) {
	for _, desc := range descs {
		if desc.Platform == nil {
			continue
		}
		if desc.Platform.OS != platform.os || desc.Platform.Architecture != platform.arch {
			continue
		}
		if platform.variant != "" && desc.Platform.Variant != platform.variant {
			continue
		}
		return desc, nil
	}
	return dockerDescriptor{}, fmt.Errorf("no manifest found for platform %s", platform.normalized)
}

func parseBearerChallenge(header string) (realm, service, scope string, err error) {
	if header == "" {
		return "", "", "", fmt.Errorf("registry requires auth but did not provide WWW-Authenticate")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", "", "", fmt.Errorf("unsupported registry auth challenge: %s", header)
	}
	fields := strings.Split(header[len(prefix):], ",")
	for _, field := range fields {
		field = strings.TrimSpace(field)
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "realm":
			realm = value
		case "service":
			service = value
		case "scope":
			scope = value
		}
	}
	if realm == "" {
		return "", "", "", fmt.Errorf("registry auth challenge missing realm")
	}
	return realm, service, scope, nil
}

func layerHint(layer dockerDescriptor) string {
	switch {
	case strings.Contains(layer.MediaType, "gzip"):
		return "layer.tar.gz"
	case strings.Contains(layer.MediaType, "zstd"):
		return "layer.tar.zst"
	default:
		return "layer.tar"
	}
}

func handleLayerEntry(f archives.FileInfo, dest string, skip int, allowSymlinks bool, pr *printer) error {
	parts := entryParts(f.NameInArchive, skip)
	if len(parts) == 0 {
		return nil
	}

	base := parts[len(parts)-1]
	parent := parts[:len(parts)-1]

	switch {
	case base == ".wh..wh..opq":
		dir, err := outPathFromParts(dest, parent)
		if err != nil || dir == "" {
			return err
		}
		if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.MkdirAll(dir, 0o755)
	case strings.HasPrefix(base, ".wh."):
		target, err := outPathFromParts(dest, append(parent, strings.TrimPrefix(base, ".wh.")))
		if err != nil || target == "" {
			return err
		}
		if err := os.RemoveAll(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}

	if f.IsDir() {
		path, err := outPathFromParts(dest, parts)
		if err != nil || path == "" {
			return err
		}
		return os.MkdirAll(path, 0o755)
	}
	if f.LinkTarget != "" {
		if !allowSymlinks {
			return nil
		}
		pr.onFile(filepath.ToSlash(filepath.Join(parts...)), -1)
		return writeSymlink(f, dest, skip)
	}
	pr.onFile(filepath.ToSlash(filepath.Join(parts...)), f.Size())
	return writeRegularFile(f, dest, skip)
}

func outPathFromParts(dest string, parts []string) (string, error) {
	if len(parts) == 0 {
		return "", nil
	}
	full := filepath.Join(append([]string{dest}, parts...)...)
	cleanDest := filepath.Clean(dest) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(full)+string(os.PathSeparator), cleanDest) {
		return "", fmt.Errorf("path traversal blocked")
	}
	return full, nil
}

func debDataTar(data []byte) ([]byte, string, error) {
	if len(data) < 8 || string(data[:8]) != "!<arch>\n" {
		return nil, "", fmt.Errorf("invalid deb archive")
	}
	off := 8
	for off+60 <= len(data) {
		hdr := data[off : off+60]
		name := strings.TrimSpace(string(hdr[:16]))
		sizeField := strings.TrimSpace(string(hdr[48:58]))
		var size int
		if _, err := fmt.Sscanf(sizeField, "%d", &size); err != nil {
			return nil, "", fmt.Errorf("invalid deb member size")
		}
		off += 60
		if off+size > len(data) {
			return nil, "", fmt.Errorf("invalid deb member bounds")
		}
		member := data[off : off+size]
		cleanName := strings.TrimSuffix(name, "/")
		if strings.HasPrefix(cleanName, "data.tar") {
			return member, cleanName, nil
		}
		off += size
		if off%2 != 0 {
			off++
		}
	}
	return nil, "", fmt.Errorf("deb archive does not contain data.tar payload")
}

func newVerifiedReader(r io.ReadCloser, digest string) (io.ReadCloser, func() error, error) {
	algo, want, ok := strings.Cut(digest, ":")
	if !ok {
		return nil, nil, fmt.Errorf("invalid digest %q", digest)
	}
	if algo != "sha256" {
		return nil, nil, fmt.Errorf("unsupported digest algorithm %q", algo)
	}

	h := sha256.New()
	vr := &verifyingReadCloser{r: r, tee: io.TeeReader(r, h)}
	verify := func() error {
		got := fmt.Sprintf("%x", h.Sum(nil))
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("docker blob digest mismatch for %s", digest)
		}
		return nil
	}
	return vr, verify, nil
}

type verifyingReadCloser struct {
	r   io.ReadCloser
	tee io.Reader
}

func (v *verifyingReadCloser) Read(p []byte) (int, error) { return v.tee.Read(p) }
func (v *verifyingReadCloser) Close() error               { return v.r.Close() }

func progressBar(pct, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := width * pct / 100
	return "\033[32m" + strings.Repeat("\u25b0", filled) +
		"\033[90m" + strings.Repeat("\u25b1", width-filled) + "\033[0m"
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func fmtRate(bytesPerSec float64) string {
	if bytesPerSec <= 0 {
		return "-- B/s"
	}
	return fmtBytes(int64(bytesPerSec)) + "/s"
}

func fmtDuration(secs float64) string {
	s := int(secs)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%ds", s/60, s%60)
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return "..." + string(runes[len(runes)-(max-3):])
}
