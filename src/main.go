package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
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
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/mholt/archives"
)

type inputSource struct {
	display string
	id      string
	hint    string
	local   bool
	path    string
	git     *gitSource
	docker  *dockerSource
	npm     *npmSource
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

func main() {
	skip := flag.Int("skip", 0, "strip N leading path components from each archive entry")
	symlinks := flag.Bool("symlinks", false, "extract symbolic links (skipped by default for safety)")
	quiet := flag.Bool("quiet", false, "plain text output instead of rich ANSI progress")
	downloadOnly := flag.Bool("download-only", false, "download/copy the original source without extracting it")
	noTempFile := flag.Bool("no-tempfile", false, "buffer non-Range ZIP in memory instead of a temp file")
	platform := flag.String("platform", defaultDockerPlatform(), "platform for registry images (for example linux/amd64)")

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: hx [flags] <source> [dest]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  source  HTTP/HTTPS URL, docker:// image reference, npm:// package reference, Git repository URL, or local file path")
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

	src, err := resolveInputSource(sourceArg)
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
	if src.local {
		return runLocal(src, dest, skip, symlinks, downloadOnly, pr)
	}
	return runRemote(src, dest, skip, symlinks, downloadOnly, useTempFile, pr)
}

func runRemote(src inputSource, dest string, skip int, symlinks, downloadOnly, useTempFile bool, pr *printer) (int, error) {
	ctx := context.Background()
	client := &http.Client{}

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

func runDocker(src inputSource, dest string, skip int, symlinks, downloadOnly bool, platformRaw string, pr *printer) (int, error) {
	platform, err := parsePlatform(platformRaw)
	if err != nil {
		return 0, err
	}

	ctx := context.Background()
	pr.info(fmt.Sprintf("source: %s", src.display))
	pr.info(fmt.Sprintf("format: docker  %s", platform.normalized))

	rc := &dockerRegistryClient{
		client:     &http.Client{},
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
	client := &http.Client{}

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

func resolveInputSource(arg string) (inputSource, error) {
	if ns, ok, err := parseNPMSource(arg); err != nil {
		return inputSource{}, err
	} else if ok {
		return inputSource{
			display: arg,
			id:      npmSourceID(ns),
			hint:    npmHint(ns),
			npm:     ns,
		}, nil
	}

	if ds, ok, err := parseDockerSource(arg); err != nil {
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

func parseNPMSource(raw string) (*npmSource, bool, error) {
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
		registry: "https://registry.npmjs.org",
		name:     name,
		selector: selector,
	}, true, nil
}

func parseDockerSource(raw string) (*dockerSource, bool, error) {
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
	if registry == "" {
		registry = "registry-1.docker.io"
		if len(repoParts) == 1 {
			repoParts = []string{"library", repoParts[0]}
		}
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

func platformKey(src inputSource, raw string) string {
	if src.docker == nil {
		return ""
	}
	p, err := parsePlatform(raw)
	if err != nil {
		return raw
	}
	return p.normalized
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
