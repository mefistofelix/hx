package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
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
		return stop_to_nil(yield(hx_item{
			src_stream:    file,
			type_name:     "file",
			src_url:       s.url,
			src_full_path: filepath.Base(local_path),
			size:          info.Size(),
		}, nil))
	}
	return stream_items(filepath.Base(local_path), s.url, info.Size(), open_local_stream(local_path), yield)
}

func (s hx_src) items_from_http(src_url *url.URL, yield func(hx_item, error) bool) error {
	if s.download_only {
		resp, err := http.Get(src_url.String())
		if err != nil {
			return err
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			defer resp.Body.Close()
			return fmt.Errorf("download failed: %s", resp.Status)
		}
		return stop_to_nil(yield(hx_item{
			src_stream:      resp.Body,
			type_name:       "file",
			src_url:         src_url.String(),
			src_full_path:   download_name(src_url),
			size_compressed: resp.ContentLength,
			size:            resp.ContentLength,
		}, nil))
	}
	if looks_like_zip(src_url.Path) {
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
		return stream_items(filepath.Base(src_url.Path), src_url.String(), info.Size(), open_local_stream(tmp_path), yield)
	}

	resp, err := http.Get(src_url.String())
	if err != nil {
		return err
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
	return filepath.Join(d.path, ".hx.done."+hex.EncodeToString(sum[:16])+".json")
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
	item := hx_item{
		type_name:     "file",
		src_url:       src_url,
		src_full_path: normalize_rel_path(rel_path),
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

func open_local_stream(local_path string) io.ReadCloser {
	file, err := os.Open(local_path)
	if err != nil {
		return error_read_closer{err: err}
	}
	return file
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
	if err := worktree.Checkout(resolve_checkout_options(ref)); err != nil {
		os.RemoveAll(work_dir)
		return "", err
	}
	return work_dir, nil
}

func resolve_checkout_options(ref string) *git.CheckoutOptions {
	if is_hex_hash(ref) {
		return &git.CheckoutOptions{Hash: plumbing.NewHash(ref)}
	}
	return &git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(ref)}
}

// -----------------------------------------------------------------------------
// Format readers
// -----------------------------------------------------------------------------

func stream_items(name string, src_url string, src_size int64, src_stream io.ReadCloser, yield func(hx_item, error) bool) error {
	lower_name := strings.ToLower(name)
	switch {
	case looks_like_tar_gz(lower_name):
		defer src_stream.Close()
		gz_reader, err := gzip.NewReader(src_stream)
		if err != nil {
			return err
		}
		defer gz_reader.Close()
		return tar_items(tar.NewReader(gz_reader), src_url, yield)
	case strings.HasSuffix(lower_name, ".tar"):
		defer src_stream.Close()
		return tar_items(tar.NewReader(src_stream), src_url, yield)
	case looks_like_gzip(lower_name):
		defer src_stream.Close()
		gz_reader, err := gzip.NewReader(src_stream)
		if err != nil {
			return err
		}
		defer gz_reader.Close()
		return stop_to_nil(yield(hx_item{
			src_stream:      io.NopCloser(gz_reader),
			type_name:       "file",
			src_url:         src_url,
			src_full_path:   strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)),
			size_compressed: src_size,
		}, nil))
	case looks_like_zip(lower_name):
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
		return stop_to_nil(yield(hx_item{
			src_stream:      src_stream,
			type_name:       "file",
			src_url:         src_url,
			src_full_path:   filepath.Base(name),
			size_compressed: src_size,
			size:            src_size,
		}, nil))
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
		item := hx_item{
			type_name:       "file",
			src_url:         src_url,
			src_full_path:   normalize_rel_path(header.Name),
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
		item := hx_item{
			type_name:       "file",
			src_url:         src_url,
			src_full_path:   normalize_rel_path(file.Name),
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
	resp, err := http.Get(src_url)
	if err != nil {
		return "", err
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
		return "download"
	}
	return name
}

// -----------------------------------------------------------------------------
// Source parsing helpers
// -----------------------------------------------------------------------------

func parse_src_url(raw_value string) (*url.URL, string) {
	if looks_like_windows_path(raw_value) {
		return &url.URL{}, raw_value
	}
	parsed, err := url.Parse(raw_value)
	if err != nil || parsed.Scheme == "" {
		return &url.URL{}, raw_value
	}
	if parsed.Scheme == "file" {
		return parsed, file_url_path(parsed)
	}
	return parsed, raw_value
}

func file_url_path(parsed *url.URL) string {
	if parsed.Host == "" {
		return filepath.FromSlash(parsed.Path)
	}
	return filepath.FromSlash("//" + parsed.Host + parsed.Path)
}

func normalize_rel_path(raw_path string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(raw_path)), "./")
}

func looks_like_tar_gz(name string) bool {
	return strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz")
}

func looks_like_gzip(name string) bool {
	return strings.HasSuffix(name, ".gz") && !looks_like_tar_gz(name)
}

func looks_like_zip(name string) bool {
	return strings.HasSuffix(name, ".zip")
}

func looks_like_windows_path(raw_value string) bool {
	if len(raw_value) < 3 {
		return false
	}
	drive := raw_value[0]
	if raw_value[1] != ':' {
		return false
	}
	return ((drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')) &&
		(raw_value[2] == '\\' || raw_value[2] == '/')
}

// GitHub HTTP URLs are rewritten so the rest of the source switch stays schema-based.
func is_github_http_url(src_url *url.URL) bool {
	if src_url == nil {
		return false
	}
	if src_url.Scheme != "http" && src_url.Scheme != "https" {
		return false
	}
	return strings.EqualFold(src_url.Hostname(), github_http_host())
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
	repo := strings.TrimSuffix(parts[1], ".git")
	base_url := strings.TrimRight(os.Getenv("HX_GITHUB_CLONE_BASE_URL"), "/")
	if base_url != "" {
		return base_url + "/" + owner + "/" + repo + ".git"
	}
	return "https://" + src_url.Host + "/" + owner + "/" + repo + ".git"
}

func github_http_host() string {
	host := strings.TrimSpace(os.Getenv("HX_GITHUB_HTTP_HOST"))
	if host == "" {
		return "github.com"
	}
	return host
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

func is_hex_hash(raw_value string) bool {
	if len(raw_value) != 40 {
		return false
	}
	for _, c := range raw_value {
		if c >= '0' && c <= '9' {
			continue
		}
		if c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

func stop_to_nil(keep_going bool) error {
	if keep_going {
		return nil
	}
	return nil
}

type error_read_closer struct {
	err error
}

func (e error_read_closer) Read(_ []byte) (int, error) {
	return 0, e.err
}

func (e error_read_closer) Close() error {
	return nil
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
