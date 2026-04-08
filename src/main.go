package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// -----------------------------------------------------------------------------
// Core state
// -----------------------------------------------------------------------------

type hx_src struct {
	url               string
	registry_base_url string
	target            string
	platform          string
	download_only     bool
	force_no_tmp      bool
}

type hx_dst struct {
	src              hx_src
	path             string
	skip_path_prefix int
	skip_symlinks    bool
	include_exclude  string
	overwrite        bool
	tui              *hx_tui
}

type hx_item struct {
	src_stream      io.ReadCloser
	type_name       string
	src_url         string
	src_full_path   string
	src_link_path   string
	dst_full_path   string
	size_compressed int64
	size_extracted  int64
	size            int64
}

type hx_tui struct {
	mode        string
	item_count  int
	total_bytes int64
}

var (
	tar_gz_suffixes      = []string{".tar.gz", ".tgz"}
	tar_suffix           = ".tar"
	gzip_suffix          = ".gz"
	zip_suffixes         = []string{".zip", ".nupkg"}
	git_suffix           = ".git"
	github_host          = "github.com"
	default_download     = "download"
	done_sentinel_prefix = ".hx.done."
	done_sentinel_suffix = ".json"
	tls_retry_message    = "warning: https certificate verification failed, retrying insecurely"
	hex_hash_rx          = regexp.MustCompile(`^[0-9a-f]{40}$`)
	tls_error_rx         = regexp.MustCompile(`(?i)(x509:|certificate|tls:)`)
)

// -----------------------------------------------------------------------------
// TUI
// -----------------------------------------------------------------------------

func (h *hx_tui) warn(msg string) {
	fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
}

func (h *hx_tui) show_item(item hx_item) {
	h.item_count++
	if item.size > 0 {
		h.total_bytes += item.size
	}
	if h.mode == "plain" {
		fmt.Printf("%s %s\n", item.type_name, item.dst_full_path)
		return
	}
	fmt.Printf("\ritems=%d bytes=%d last=%s", h.item_count, h.total_bytes, item.dst_full_path)
}

// -----------------------------------------------------------------------------
// Source iteration
// -----------------------------------------------------------------------------

// items normalizes the input source and exposes it as a single item stream.
func (s hx_src) items() iter.Seq2[hx_item, error] {
	return func(yield func(hx_item, error) bool) {
		if err := s.emit_items(yield); err != nil {
			yield(hx_item{}, err)
		}
	}
}

func (s hx_src) emit_items(yield func(hx_item, error) bool) error {
	src_url, local_path := parse_src_url(s.url)
	if is_github_http_url(src_url) {
		src_url = normalize_github_url(src_url)
	}

	switch src_url.Scheme {
	case "", "file":
		return s.items_from_local(local_path, yield)
	case "http", "https":
		return s.items_from_http(src_url, yield)
	case "git":
		return s.items_from_git(src_url.String(), src_url.Query().Get("ref"), yield)
	case "github":
		return s.items_from_git(github_clone_url(src_url), src_url.Query().Get("ref"), yield)
	case "pypi":
		return s.items_from_pypi(src_url, yield)
	case "nuget":
		return s.items_from_nuget(src_url, yield)
	default:
		return fmt.Errorf("unsupported source scheme: %s", src_url.Scheme)
	}
}

func (s hx_src) items_from_local(local_path string, yield func(hx_item, error) bool) error {
	info, err := os.Lstat(local_path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return walk_local_dir(local_path, false, s.url, yield)
	}
	if s.download_only {
		file, err := os.Open(local_path)
		if err != nil {
			return err
		}
		yield(hx_item{
			src_stream:    file,
			type_name:     "file",
			src_url:       s.url,
			src_full_path: filepath.Base(local_path),
			size:          info.Size(),
		}, nil)
		return nil
	}
	file, err := os.Open(local_path)
	if err != nil {
		return err
	}
	return stream_items(filepath.Base(local_path), s.url, info.Size(), file, yield)
}

func (s hx_src) items_from_http(src_url *url.URL, yield func(hx_item, error) bool) error {
	if looks_like_http_git_url(src_url) && !s.download_only {
		return s.items_from_git(src_url.String(), src_url.Query().Get("ref"), yield)
	}
	if s.download_only {
		resp, insecure_retry, err := http_get(src_url.String())
		if err != nil {
			return err
		}
		if insecure_retry {
			fmt.Fprintln(os.Stderr, tls_retry_message)
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			defer resp.Body.Close()
			return fmt.Errorf("download failed: %s", resp.Status)
		}
		yield(hx_item{
			src_stream:      resp.Body,
			type_name:       "file",
			src_url:         src_url.String(),
			src_full_path:   download_name(src_url),
			size_compressed: resp.ContentLength,
			size:            resp.ContentLength,
		}, nil)
		return nil
	}
	if has_suffix_fold(src_url.Path, zip_suffixes...) {
		if s.force_no_tmp {
			return errors.New("zip over http requires temp file unless -notmp 0")
		}
		tmp_path, err := download_to_temp(src_url.String())
		if err != nil {
			return err
		}
		defer os.Remove(tmp_path)
		info, err := os.Stat(tmp_path)
		if err != nil {
			return err
		}
		file, err := os.Open(tmp_path)
		if err != nil {
			return err
		}
		return stream_items(filepath.Base(src_url.Path), src_url.String(), info.Size(), file, yield)
	}

	resp, insecure_retry, err := http_get(src_url.String())
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return fmt.Errorf("download failed: %s", resp.Status)
	}
	return stream_items(filepath.Base(src_url.Path), src_url.String(), resp.ContentLength, resp.Body, yield)
}

func (s hx_src) items_from_git(clone_url string, ref string, yield func(hx_item, error) bool) error {
	if s.download_only {
		return errors.New("download-only is not implemented for git sources")
	}
	work_dir, err := clone_git_repo(clone_url, ref)
	if err != nil {
		return err
	}
	defer os.RemoveAll(work_dir)
	return walk_local_dir(work_dir, true, clone_url, yield)
}

func (s hx_src) items_from_pypi(src_url *url.URL, yield func(hx_item, error) bool) error {
	package_name := src_url.Host
	version := ""
	if src_url.User != nil {
		package_name = src_url.User.Username()
		version = src_url.Host
	}
	if package_name == "" || package_name == "." {
		return errors.New("pypi source requires a package name")
	}

	registry_base_url := strings.TrimRight(s.registry_base_url, "/")
	if registry_base_url == "" {
		registry_base_url = "https://pypi.org"
	}

	metadata_path := "/pypi/" + package_name + "/json"
	if version != "" {
		metadata_path = "/pypi/" + package_name + "/" + version + "/json"
	}
	metadata_url := registry_base_url + metadata_path
	resp, insecure_retry, err := http_get(metadata_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("pypi metadata request failed: %s", resp.Status)
	}

	var payload struct {
		URLs []struct {
			URL         string `json:"url"`
			Filename    string `json:"filename"`
			PackageType string `json:"packagetype"`
		} `json:"urls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if len(payload.URLs) == 0 {
		return errors.New("pypi metadata returned no files")
	}

	artifact_url := payload.URLs[0].URL
	for _, file := range payload.URLs {
		if file.PackageType == "sdist" {
			artifact_url = file.URL
			break
		}
	}

	artifact_resp, insecure_retry, err := http_get(artifact_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	if artifact_resp.StatusCode < 200 || artifact_resp.StatusCode > 299 {
		defer artifact_resp.Body.Close()
		return fmt.Errorf("pypi download failed: %s", artifact_resp.Status)
	}
	if s.download_only {
		yield(hx_item{
			src_stream:      artifact_resp.Body,
			type_name:       "file",
			src_url:         artifact_url,
			src_full_path:   path.Base(artifact_url),
			size_compressed: artifact_resp.ContentLength,
			size:            artifact_resp.ContentLength,
		}, nil)
		return nil
	}

	return stream_items(path.Base(artifact_url), artifact_url, artifact_resp.ContentLength, artifact_resp.Body, yield)
}

func (s hx_src) items_from_nuget(src_url *url.URL, yield func(hx_item, error) bool) error {
	package_name := src_url.Host
	version := ""
	if src_url.User != nil {
		package_name = src_url.User.Username()
		version = src_url.Host
	}
	if package_name == "" || package_name == "." {
		return errors.New("nuget source requires a package name")
	}

	registry_base_url := strings.TrimRight(s.registry_base_url, "/")
	if registry_base_url == "" {
		registry_base_url = "https://api.nuget.org"
	}

	if version == "" {
		index_url := registry_base_url + "/v3-flatcontainer/" + strings.ToLower(package_name) + "/index.json"
		resp, insecure_retry, err := http_get(index_url)
		if err != nil {
			return err
		}
		if insecure_retry {
			fmt.Fprintln(os.Stderr, tls_retry_message)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("nuget metadata request failed: %s", resp.Status)
		}
		var payload struct {
			Versions []string `json:"versions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return err
		}
		if len(payload.Versions) == 0 {
			return errors.New("nuget metadata returned no versions")
		}
		version = payload.Versions[len(payload.Versions)-1]
	}

	artifact_url := registry_base_url + "/v3-flatcontainer/" +
		strings.ToLower(package_name) + "/" + strings.ToLower(version) + "/" +
		strings.ToLower(package_name) + "." + strings.ToLower(version) + ".nupkg"

	resp, insecure_retry, err := http_get(artifact_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return fmt.Errorf("nuget download failed: %s", resp.Status)
	}
	if s.download_only {
		yield(hx_item{
			src_stream:      resp.Body,
			type_name:       "file",
			src_url:         artifact_url,
			src_full_path:   path.Base(artifact_url),
			size_compressed: resp.ContentLength,
			size:            resp.ContentLength,
		}, nil)
		return nil
	}

	return stream_items(path.Base(artifact_url), artifact_url, resp.ContentLength, resp.Body, yield)
}

// -----------------------------------------------------------------------------
// Destination flow
// -----------------------------------------------------------------------------

func (d hx_dst) get_done_sentinel_path() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		d.src.url,
		d.src.registry_base_url,
		d.src.target,
		d.src.platform,
		fmt.Sprintf("%t", d.src.download_only),
		fmt.Sprintf("%t", d.src.force_no_tmp),
		d.path,
		fmt.Sprintf("%d", d.skip_path_prefix),
		fmt.Sprintf("%t", d.skip_symlinks),
		d.include_exclude,
		fmt.Sprintf("%t", d.overwrite),
	}, "\n")))
	return filepath.Join(d.path, done_sentinel_prefix+hex.EncodeToString(sum[:16])+done_sentinel_suffix)
}

func (d hx_dst) set_done_sentinel(done bool) error {
	sentinel_path := d.get_done_sentinel_path()
	if !done {
		return os.Remove(sentinel_path)
	}
	payload := map[string]any{
		"source":      d.src.url,
		"written_at":  time.Now().UTC().Format(time.RFC3339),
		"destination": d.path,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(sentinel_path, data, 0o644)
}

func (d hx_dst) copy() error {
	if _, err := os.Stat(d.get_done_sentinel_path()); err == nil {
		d.tui.warn("destination already matches the same source/options, skipping")
		return nil
	}
	if err := os.MkdirAll(d.path, 0o755); err != nil {
		return err
	}

	for item, err := range d.src.items() {
		if err != nil {
			return err
		}
		if item.type_name == "link" && d.skip_symlinks {
			continue
		}

		dst_rel_path, keep := d.dst_rel_path(item.src_full_path)
		if !keep || !d.allow_item(dst_rel_path) {
			if item.src_stream != nil {
				_ = item.src_stream.Close()
			}
			continue
		}

		item.dst_full_path = filepath.Join(d.path, filepath.FromSlash(dst_rel_path))
		d.tui.show_item(item)
		if err := d.copy_item(item); err != nil {
			d.tui.warn(err.Error())
			return err
		}
	}

	if d.tui.mode != "plain" && d.tui.item_count > 0 {
		fmt.Println()
	}
	return nil
}

func (d hx_dst) dst_rel_path(src_full_path string) (string, bool) {
	clean_path := normalize_rel_path(src_full_path)
	parts := strings.Split(clean_path, "/")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" && part != "." {
			filtered = append(filtered, part)
		}
	}
	if len(filtered) == 0 || d.skip_path_prefix >= len(filtered) {
		return "", false
	}
	return strings.Join(filtered[d.skip_path_prefix:], "/"), true
}

// allow_item applies the ordered + / - rules after path stripping.
func (d hx_dst) allow_item(rel_path string) bool {
	rules := strings.TrimSpace(d.include_exclude)
	if rules == "" || rules == ":+ " || rules == ":+" {
		return true
	}

	allowed := false
	for _, raw_rule := range strings.FieldsFunc(rules, func(r rune) bool { return r == ',' || r == ';' }) {
		rule := strings.TrimSpace(raw_rule)
		if len(rule) < 2 {
			continue
		}
		mode := rule[0]
		pattern := strings.TrimPrefix(strings.TrimPrefix(rule[1:], ":"), " ")
		if pattern == "" || pattern == "*" {
			allowed = mode == '+'
			continue
		}
		matched, err := path.Match(pattern, rel_path)
		if err != nil {
			continue
		}
		if !matched && strings.HasPrefix(rel_path, strings.TrimSuffix(pattern, "/")+"/") {
			matched = true
		}
		if matched {
			allowed = mode == '+'
		}
	}
	return allowed
}

func (d hx_dst) copy_item(item hx_item) error {
	switch item.type_name {
	case "dir":
		return os.MkdirAll(item.dst_full_path, 0o755)
	case "link":
		return d.copy_link(item)
	case "file":
		return d.copy_file(item)
	default:
		if item.src_stream != nil {
			_ = item.src_stream.Close()
		}
		return fmt.Errorf("unsupported item type: %s", item.type_name)
	}
}

func (d hx_dst) copy_link(item hx_item) error {
	parent_path := filepath.Dir(item.dst_full_path)
	if err := os.MkdirAll(parent_path, 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(item.dst_full_path); err == nil {
		if !d.overwrite {
			return nil
		}
		if err := os.Remove(item.dst_full_path); err != nil {
			return err
		}
	}
	if runtime.GOOS == "windows" {
		d.tui.warn("symlink extraction is skipped on windows")
		return nil
	}
	return os.Symlink(item.src_link_path, item.dst_full_path)
}

func (d hx_dst) copy_file(item hx_item) error {
	defer func() {
		if item.src_stream != nil {
			_ = item.src_stream.Close()
		}
	}()
	parent_path := filepath.Dir(item.dst_full_path)
	if err := os.MkdirAll(parent_path, 0o755); err != nil {
		return err
	}
	if !d.overwrite {
		if _, err := os.Stat(item.dst_full_path); err == nil {
			return nil
		}
	}
	out_file, err := os.Create(item.dst_full_path)
	if err != nil {
		return err
	}
	defer out_file.Close()
	_, err = io.Copy(out_file, item.src_stream)
	return err
}

// -----------------------------------------------------------------------------
// Local and git helpers
// -----------------------------------------------------------------------------

func local_fs_item(current_path string, rel_path string, src_url string) (hx_item, error) {
	info, err := os.Lstat(current_path)
	if err != nil {
		return hx_item{}, err
	}
	normalized_path := normalize_rel_path(rel_path)
	if normalized_path == "" {
		return hx_item{}, fmt.Errorf("invalid relative path: %s", rel_path)
	}
	item := hx_item{
		type_name:     "file",
		src_url:       src_url,
		src_full_path: normalized_path,
		size:          info.Size(),
	}
	if info.IsDir() {
		item.type_name = "dir"
		return item, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link_path, err := os.Readlink(current_path)
		if err != nil {
			return hx_item{}, err
		}
		item.type_name = "link"
		item.src_link_path = link_path
		item.size = 0
		return item, nil
	}
	file, err := os.Open(current_path)
	if err != nil {
		return hx_item{}, err
	}
	item.src_stream = file
	return item, nil
}

// walk_local_dir is shared by real local sources and cloned git worktrees.
func walk_local_dir(local_path string, skip_git_dir bool, src_url string, yield func(hx_item, error) bool) error {
	return filepath.WalkDir(local_path, func(current_path string, entry os.DirEntry, walk_err error) error {
		if walk_err != nil {
			return walk_err
		}
		if current_path == local_path {
			return nil
		}
		if skip_git_dir && entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		rel_path, err := filepath.Rel(local_path, current_path)
		if err != nil {
			return err
		}
		item, err := local_fs_item(current_path, rel_path, src_url)
		if err != nil {
			return err
		}
		if !yield(item, nil) {
			if item.src_stream != nil {
				_ = item.src_stream.Close()
			}
			return io.EOF
		}
		return nil
	})
}

// clone_git_repo materializes the repo once so the rest of the pipeline stays path-based.
func clone_git_repo(clone_url string, ref string) (string, error) {
	work_dir, err := os.MkdirTemp("", "hx-git-*")
	if err != nil {
		return "", err
	}
	_, err = git.PlainClone(work_dir, false, &git.CloneOptions{
		URL:      clone_url,
		Progress: io.Discard,
	})
	if err != nil {
		os.RemoveAll(work_dir)
		return "", err
	}
	if ref == "" {
		return work_dir, nil
	}
	repo, err := git.PlainOpen(work_dir)
	if err != nil {
		os.RemoveAll(work_dir)
		return "", err
	}
	worktree, err := repo.Worktree()
	if err != nil {
		os.RemoveAll(work_dir)
		return "", err
	}
	checkout_opts := &git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(ref)}
	if hex_hash_rx.MatchString(ref) {
		checkout_opts = &git.CheckoutOptions{Hash: plumbing.NewHash(ref)}
	}
	if err := worktree.Checkout(checkout_opts); err != nil {
		os.RemoveAll(work_dir)
		return "", err
	}
	return work_dir, nil
}

// -----------------------------------------------------------------------------
// Format readers
// -----------------------------------------------------------------------------

func stream_items(name string, src_url string, src_size int64, src_stream io.ReadCloser, yield func(hx_item, error) bool) error {
	lower_name := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower_name, tar_gz_suffixes[0]), strings.HasSuffix(lower_name, tar_gz_suffixes[1]):
		defer src_stream.Close()
		gz_reader, err := gzip.NewReader(src_stream)
		if err != nil {
			return err
		}
		defer gz_reader.Close()
		return tar_items(tar.NewReader(gz_reader), src_url, yield)
	case strings.HasSuffix(lower_name, tar_suffix):
		defer src_stream.Close()
		return tar_items(tar.NewReader(src_stream), src_url, yield)
	case strings.HasSuffix(lower_name, gzip_suffix) &&
		!strings.HasSuffix(lower_name, tar_gz_suffixes[0]) &&
		!strings.HasSuffix(lower_name, tar_gz_suffixes[1]):
		defer src_stream.Close()
		gz_reader, err := gzip.NewReader(src_stream)
		if err != nil {
			return err
		}
		defer gz_reader.Close()
		yield(hx_item{
			src_stream:      io.NopCloser(gz_reader),
			type_name:       "file",
			src_url:         src_url,
			src_full_path:   strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)),
			size_compressed: src_size,
		}, nil)
		return nil
	case has_suffix_fold(lower_name, zip_suffixes...):
		defer src_stream.Close()
		tmp_file, err := os.CreateTemp("", "hx-local-zip-*.zip")
		if err != nil {
			return err
		}
		tmp_path := tmp_file.Name()
		if _, err := io.Copy(tmp_file, src_stream); err != nil {
			tmp_file.Close()
			os.Remove(tmp_path)
			return err
		}
		tmp_file.Close()
		defer os.Remove(tmp_path)
		return zip_items(tmp_path, src_url, yield)
	default:
		yield(hx_item{
			src_stream:      src_stream,
			type_name:       "file",
			src_url:         src_url,
			src_full_path:   filepath.Base(name),
			size_compressed: src_size,
			size:            src_size,
		}, nil)
		return nil
	}
}

func tar_items(tr *tar.Reader, src_url string, yield func(hx_item, error) bool) error {
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		normalized_path := normalize_rel_path(header.Name)
		if normalized_path == "" {
			return fmt.Errorf("invalid archive path: %s", header.Name)
		}
		item := hx_item{
			type_name:       "file",
			src_url:         src_url,
			src_full_path:   normalized_path,
			src_link_path:   header.Linkname,
			size_compressed: header.Size,
			size_extracted:  header.Size,
			size:            header.Size,
		}
		switch header.Typeflag {
		case tar.TypeDir:
			item.type_name = "dir"
		case tar.TypeSymlink:
			item.type_name = "link"
			item.size = 0
		default:
			item.src_stream = io.NopCloser(io.LimitReader(tr, header.Size))
		}
		if !yield(item, nil) {
			if item.src_stream != nil {
				_ = item.src_stream.Close()
			}
			return nil
		}
	}
}

func zip_items(zip_path string, src_url string, yield func(hx_item, error) bool) error {
	reader, err := zip.OpenReader(zip_path)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		normalized_path := normalize_rel_path(file.Name)
		if normalized_path == "" {
			return fmt.Errorf("invalid archive path: %s", file.Name)
		}
		item := hx_item{
			type_name:       "file",
			src_url:         src_url,
			src_full_path:   normalized_path,
			size_compressed: int64(file.CompressedSize64),
			size_extracted:  int64(file.UncompressedSize64),
			size:            int64(file.UncompressedSize64),
		}
		if file.FileInfo().IsDir() {
			item.type_name = "dir"
		} else if file.Mode()&os.ModeSymlink != 0 {
			item.type_name = "link"
			rc, err := file.Open()
			if err != nil {
				return err
			}
			target_data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return err
			}
			item.src_link_path = string(target_data)
			item.size = 0
		} else {
			rc, err := file.Open()
			if err != nil {
				return err
			}
			item.src_stream = rc
		}
		if !yield(item, nil) {
			if item.src_stream != nil {
				_ = item.src_stream.Close()
			}
			return nil
		}
	}
	return nil
}

func download_to_temp(src_url string) (string, error) {
	resp, insecure_retry, err := http_get(src_url)
	if err != nil {
		return "", err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	tmp_file, err := os.CreateTemp("", "hx-http-*.tmp")
	if err != nil {
		return "", err
	}
	defer tmp_file.Close()
	if _, err := io.Copy(tmp_file, resp.Body); err != nil {
		os.Remove(tmp_file.Name())
		return "", err
	}
	return tmp_file.Name(), nil
}

func download_name(src_url *url.URL) string {
	name := path.Base(src_url.Path)
	if name == "." || name == "/" || name == "" {
		return default_download
	}
	return name
}

// -----------------------------------------------------------------------------
// Source parsing helpers
// -----------------------------------------------------------------------------

func parse_src_url(raw_value string) (*url.URL, string) {
	if filepath.IsAbs(raw_value) {
		return &url.URL{}, raw_value
	}
	if _, err := os.Lstat(raw_value); err == nil {
		return &url.URL{}, raw_value
	}
	parsed, err := url.Parse(raw_value)
	if err != nil || parsed.Scheme == "" {
		return &url.URL{}, raw_value
	}
	if parsed.Scheme == "file" {
		if parsed.Host == "" {
			file_path := filepath.FromSlash(parsed.Path)
			if len(file_path) >= 3 && (file_path[0] == '\\' || file_path[0] == '/') && file_path[2] == ':' {
				return parsed, file_path[1:]
			}
			return parsed, file_path
		}
		return parsed, filepath.FromSlash("//" + parsed.Host + parsed.Path)
	}
	return parsed, raw_value
}

func normalize_rel_path(raw_path string) string {
	clean_path := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(raw_path)), "./")
	clean_path = strings.TrimPrefix(clean_path, "/")
	if clean_path == "" || clean_path == "." || !fs.ValidPath(clean_path) {
		return ""
	}
	return clean_path
}

func looks_like_http_git_url(src_url *url.URL) bool {
	if src_url == nil {
		return false
	}
	if src_url.Scheme != "http" && src_url.Scheme != "https" {
		return false
	}
	return strings.HasSuffix(strings.ToLower(src_url.Path), git_suffix)
}

// GitHub HTTP URLs are rewritten so the rest of the source switch stays schema-based.
func is_github_http_url(src_url *url.URL) bool {
	if src_url == nil {
		return false
	}
	if src_url.Scheme != "http" && src_url.Scheme != "https" {
		return false
	}
	return strings.EqualFold(src_url.Hostname(), github_host)
}

func normalize_github_url(src_url *url.URL) *url.URL {
	parts := split_clean_path(src_url.Path)
	if len(parts) < 2 {
		return src_url
	}
	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")
	ref := ""
	if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "commit") {
		ref = strings.Join(parts[3:], "/")
	}
	github_url := &url.URL{
		Scheme: "github",
		Host:   src_url.Hostname(),
		Path:   "/" + owner + "/" + repo,
	}
	if ref != "" {
		query := url.Values{}
		query.Set("ref", ref)
		github_url.RawQuery = query.Encode()
	}
	return github_url
}

func github_clone_url(src_url *url.URL) string {
	parts := split_clean_path(src_url.Path)
	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], git_suffix)
	return (&url.URL{
		Scheme: "https",
		Host:   src_url.Host,
		Path:   path.Join(owner, repo) + git_suffix,
	}).String()
}

func split_clean_path(raw_path string) []string {
	parts := strings.Split(strings.Trim(path.Clean(raw_path), "/"), "/")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" && part != "." {
			filtered = append(filtered, part)
		}
	}
	return filtered
}

func http_get(src_url string) (*http.Response, bool, error) {
	resp, err := http.DefaultClient.Get(src_url)
	if err == nil {
		return resp, false, nil
	}
	if !looks_like_tls_verify_error(err) {
		return nil, false, err
	}
	insecure_client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, retry_err := insecure_client.Get(src_url)
	if retry_err != nil {
		return nil, false, retry_err
	}
	return resp, true, nil
}

func looks_like_tls_verify_error(err error) bool {
	return tls_error_rx.MatchString(err.Error())
}

func has_suffix_fold(raw_value string, suffixes ...string) bool {
	lower_value := strings.ToLower(raw_value)
	for _, suffix := range suffixes {
		if strings.HasSuffix(lower_value, strings.ToLower(suffix)) {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// CLI
// -----------------------------------------------------------------------------

func main() {
	src := hx_src{}
	dst := hx_dst{}
	tui := &hx_tui{mode: "ansi"}
	keep_symlinks := false

	flag.IntVar(&dst.skip_path_prefix, "strip", 0, "strip N leading path components")
	flag.IntVar(&dst.skip_path_prefix, "skip", 0, "strip N leading path components")
	flag.BoolVar(&keep_symlinks, "symlinks", false, "keep symlinks when supported")
	flag.BoolVar(&src.download_only, "download-only", false, "download without extraction")
	flag.BoolVar(&src.download_only, "do", false, "download without extraction")
	flag.BoolVar(&src.force_no_tmp, "notmp", false, "avoid temp-file fallback")
	flag.BoolVar(&src.force_no_tmp, "no-tempfile", false, "avoid temp-file fallback")
	flag.StringVar(&src.platform, "platform", runtime.GOOS+"/"+runtime.GOARCH, "target platform")
	flag.StringVar(&src.platform, "plat", runtime.GOOS+"/"+runtime.GOARCH, "target platform")
	flag.StringVar(&src.registry_base_url, "registry", "", "registry override")
	flag.StringVar(&src.registry_base_url, "reg", "", "registry override")
	flag.StringVar(&src.target, "target", "", "target override")
	flag.StringVar(&src.target, "t", "", "target override")
	flag.StringVar(&dst.include_exclude, "incexc", ":+", "include/exclude rules")
	quiet := flag.Bool("quiet", false, "plain output")
	flag.BoolVar(quiet, "q", false, "plain output")
	overwrite := flag.Bool("overwrite", true, "overwrite files")
	flag.Parse()

	if *quiet {
		tui.mode = "plain"
	}
	dst.skip_symlinks = !keep_symlinks
	dst.overwrite = *overwrite
	dst.tui = tui

	args := flag.Args()
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: hx [flags] <source> [dest]")
		os.Exit(2)
	}

	src.url = args[0]
	dst.src = src
	dst.path = "."
	if len(args) == 2 {
		dst.path = args[1]
	}

	if err := dst.copy(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := dst.set_done_sentinel(true); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
