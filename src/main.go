package main

import (
	"crypto/sha256"
	"encoding/hex"
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
)

// ----------------------------------------------------------------------
// Source model and iteration.

type hx_src struct {
	url               string
	registry_base_url string
	target            string
	platform          string
	download_only     bool
	force_no_tmp      bool
}

type hx_item struct {
	src_stream       io.ReadCloser
	type_name        string
	src_url          string
	src_full_path    string
	src_link_path    string
	dst_full_path    string
	size_compressed  int64
	size_extracted   int64
	size             int64
	close_after_copy bool
}

type source_iter_state struct {
	src        hx_src
	parsed_url *url.URL
	http_name  string
}

func (s hx_src) items(yield func(hx_item) bool) error {
	state, err := s.new_source_iter_state()
	if err != nil {
		return err
	}

	switch state.parsed_url.Scheme {
	case "", "file":
		return state.iter_local_items(yield)
	case "http", "https":
		return state.iter_http_items(yield)
	default:
		return fmt.Errorf("unsupported source scheme %q", state.parsed_url.Scheme)
	}
}

func (s hx_src) new_source_iter_state() (source_iter_state, error) {
	src_url := strings.TrimSpace(s.url)
	if src_url == "" {
		return source_iter_state{}, errors.New("source is required")
	}

	parsed_url, err := url.Parse(src_url)
	if err != nil || parsed_url.Scheme == "" || (parsed_url.Scheme != "file" && !strings.Contains(src_url, "://")) {
		local_path := src_url
		if strings.HasPrefix(local_path, "file://") {
			local_path = strings.TrimPrefix(local_path, "file://")
		}
		clean_path, clean_err := filepath.Abs(local_path)
		if clean_err != nil {
			return source_iter_state{}, clean_err
		}
		return source_iter_state{
			src:        s,
			parsed_url: &url.URL{Path: filepath.ToSlash(clean_path)},
		}, nil
	}

	if parsed_url.Scheme == "file" {
		local_path := parsed_url.Path
		if local_path == "" {
			local_path = parsed_url.Opaque
		}
		if runtime.GOOS == "windows" && strings.HasPrefix(local_path, "/") && len(local_path) > 2 && local_path[2] == ':' {
			local_path = local_path[1:]
		}
		clean_path, clean_err := filepath.Abs(filepath.FromSlash(local_path))
		if clean_err != nil {
			return source_iter_state{}, clean_err
		}
		parsed_url.Path = filepath.ToSlash(clean_path)
		return source_iter_state{src: s, parsed_url: parsed_url}, nil
	}

	if parsed_url.Scheme == "http" || parsed_url.Scheme == "https" {
		return source_iter_state{
			src:        s,
			parsed_url: parsed_url,
			http_name:  infer_http_name(parsed_url),
		}, nil
	}

	return source_iter_state{src: s, parsed_url: parsed_url}, nil
}

func (s source_iter_state) iter_local_items(yield func(hx_item) bool) error {
	root_path := filepath.FromSlash(s.parsed_url.Path)
	info, err := os.Lstat(root_path)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		file, open_err := os.Open(root_path)
		if open_err != nil {
			return open_err
		}
		item := hx_item{
			src_stream:       file,
			type_name:        "file",
			src_url:          s.src.url,
			src_full_path:    root_path,
			size:             info.Size(),
			size_extracted:   info.Size(),
			size_compressed:  info.Size(),
			close_after_copy: true,
		}
		if !yield(item) {
			file.Close()
		}
		return nil
	}

	root_clean := filepath.Clean(root_path)
	err = filepath.WalkDir(root_clean, func(path string, d os.DirEntry, walk_err error) error {
		if walk_err != nil {
			return walk_err
		}
		if path == root_clean {
			return nil
		}

		info, info_err := d.Info()
		if info_err != nil {
			return info_err
		}

		item := hx_item{
			type_name:       "dir",
			src_url:         s.src.url,
			src_full_path:   path,
			size:            info.Size(),
			size_extracted:  info.Size(),
			size_compressed: info.Size(),
		}

		if d.Type()&os.ModeSymlink != 0 {
			link_path, link_err := os.Readlink(path)
			if link_err != nil {
				return link_err
			}
			item.type_name = "link"
			item.src_link_path = link_path
		} else if !d.IsDir() {
			file, open_err := os.Open(path)
			if open_err != nil {
				return open_err
			}
			item.type_name = "file"
			item.src_stream = file
			item.close_after_copy = true
		}

		if !yield(item) {
			if item.close_after_copy && item.src_stream != nil {
				item.src_stream.Close()
			}
			return errors.New("iteration stopped")
		}
		return nil
	})
	if err != nil && err.Error() == "iteration stopped" {
		return nil
	}
	return err
}

func (s source_iter_state) iter_http_items(yield func(hx_item) bool) error {
	resp, err := http.Get(s.parsed_url.String())
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return fmt.Errorf("download failed with status %s", resp.Status)
	}

	item := hx_item{
		src_stream:       resp.Body,
		type_name:        "file",
		src_url:          s.parsed_url.String(),
		src_full_path:    s.http_name,
		size:             resp.ContentLength,
		size_extracted:   resp.ContentLength,
		size_compressed:  resp.ContentLength,
		close_after_copy: true,
	}
	if !yield(item) {
		resp.Body.Close()
	}
	return nil
}

func infer_http_name(parsed_url *url.URL) string {
	name := filepath.Base(parsed_url.Path)
	if name == "." || name == "/" || name == "" {
		return "download.bin"
	}
	return name
}

// ----------------------------------------------------------------------
// Destination copy and idempotency.

type hx_dst struct {
	src             hx_src
	path            string
	skip_path_prefix int
	skip_symlinks   bool
	include_exclude string
	overwrite       bool

	tui hx_tui
}

func (d hx_dst) get_done_sentinel_path() string {
	cache_key := strings.Join([]string{
		d.src.url,
		d.src.registry_base_url,
		d.src.target,
		d.src.platform,
		fmt.Sprintf("download_only=%t", d.src.download_only),
		fmt.Sprintf("force_no_tmp=%t", d.src.force_no_tmp),
		fmt.Sprintf("skip_path_prefix=%d", d.skip_path_prefix),
		fmt.Sprintf("skip_symlinks=%t", d.skip_symlinks),
		fmt.Sprintf("include_exclude=%s", d.include_exclude),
		fmt.Sprintf("overwrite=%t", d.overwrite),
	}, "\n")
	sum := sha256.Sum256([]byte(cache_key))
	return filepath.Join(d.path, ".hx", hex.EncodeToString(sum[:])+".done")
}

func (d hx_dst) set_done_sentinel(done bool) error {
	sentinel_path := d.get_done_sentinel_path()
	if !done {
		if err := os.Remove(sentinel_path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(sentinel_path), 0o755); err != nil {
		return err
	}
	content := []byte(time.Now().UTC().Format(time.RFC3339) + "\n")
	return os.WriteFile(sentinel_path, content, 0o644)
}

func (d hx_dst) copy() error {
	if d.path == "" {
		return errors.New("destination path is required")
	}
	if err := os.MkdirAll(d.path, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(d.get_done_sentinel_path()); err == nil {
		d.tui.warn("destination already up to date, skipping")
		return nil
	}

	root_path, root_is_dir, err := d.detect_source_root()
	if err != nil {
		return err
	}

	var copy_err error
	err = d.src.items(func(item hx_item) bool {
		if d.skip_symlinks && item.type_name == "link" {
			close_item_stream(item)
			return true
		}

		rel_path, rel_err := d.item_relative_path(item, root_path, root_is_dir)
		if rel_err != nil {
			close_item_stream(item)
			copy_err = rel_err
			return false
		}
		if rel_path == "" {
			close_item_stream(item)
			return true
		}
		if !match_include_exclude(d.include_exclude, rel_path) {
			close_item_stream(item)
			return true
		}

		item.dst_full_path = filepath.Join(d.path, rel_path)
		d.tui.show_item(item)
		if err := d.copy_item(item); err != nil {
			copy_err = err
			return false
		}
		return true
	})
	if copy_err != nil {
		_ = d.set_done_sentinel(false)
		return copy_err
	}
	if err != nil {
		_ = d.set_done_sentinel(false)
		return err
	}
	return d.set_done_sentinel(true)
}

func (d hx_dst) detect_source_root() (string, bool, error) {
	state, err := d.src.new_source_iter_state()
	if err != nil {
		return "", false, err
	}
	switch state.parsed_url.Scheme {
	case "", "file":
		root_path := filepath.FromSlash(state.parsed_url.Path)
		info, stat_err := os.Lstat(root_path)
		if stat_err != nil {
			return "", false, stat_err
		}
		return root_path, info.IsDir(), nil
	case "http", "https":
		return state.http_name, false, nil
	default:
		return "", false, nil
	}
}

func (d hx_dst) item_relative_path(item hx_item, root_path string, root_is_dir bool) (string, error) {
	if item.src_full_path == "" {
		return "", errors.New("missing source path")
	}

	rel_path := item.src_full_path
	if filepath.IsAbs(item.src_full_path) {
		if root_is_dir {
			next_path, rel_err := filepath.Rel(root_path, item.src_full_path)
			if rel_err != nil {
				return "", rel_err
			}
			rel_path = next_path
		} else {
			rel_path = filepath.Base(item.src_full_path)
		}
	}

	if !root_is_dir && rel_path == item.src_full_path && !filepath.IsAbs(item.src_full_path) {
		rel_path = filepath.Base(item.src_full_path)
	}

	rel_path = filepath.ToSlash(filepath.Clean(rel_path))
	if rel_path == "." {
		rel_path = ""
	}
	rel_parts := split_clean_path(rel_path)
	if d.skip_path_prefix >= len(rel_parts) {
		if len(rel_parts) == 0 {
			return "", nil
		}
		return "", nil
	}
	rel_parts = rel_parts[d.skip_path_prefix:]
	return filepath.Join(rel_parts...), nil
}

func split_clean_path(path string) []string {
	clean_path := strings.Trim(filepath.ToSlash(filepath.Clean(path)), "/")
	if clean_path == "" || clean_path == "." {
		return nil
	}
	return strings.Split(clean_path, "/")
}

func (d hx_dst) copy_item(item hx_item) error {
	switch item.type_name {
	case "dir":
		return os.MkdirAll(item.dst_full_path, 0o755)
	case "link":
		if err := os.MkdirAll(filepath.Dir(item.dst_full_path), 0o755); err != nil {
			return err
		}
		if d.overwrite {
			_ = os.Remove(item.dst_full_path)
		}
		return os.Symlink(item.src_link_path, item.dst_full_path)
	case "file":
		return d.copy_file(item)
	default:
		return fmt.Errorf("unsupported item type %q", item.type_name)
	}
}

func (d hx_dst) copy_file(item hx_item) error {
	if item.src_stream == nil {
		return errors.New("missing source stream")
	}
	defer func() {
		if item.close_after_copy {
			item.src_stream.Close()
		}
	}()

	if err := os.MkdirAll(filepath.Dir(item.dst_full_path), 0o755); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if !d.overwrite {
		flags = os.O_CREATE | os.O_WRONLY | os.O_EXCL
	}
	file, err := os.OpenFile(item.dst_full_path, flags, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	buf := make([]byte, 32*1024)
	var copied int64
	for {
		n, read_err := item.src_stream.Read(buf)
		if n > 0 {
			written, write_err := file.Write(buf[:n])
			if write_err != nil {
				return write_err
			}
			copied += int64(written)
			item.size_extracted = copied
			d.tui.show_item(item)
		}
		if errors.Is(read_err, io.EOF) {
			break
		}
		if read_err != nil {
			return read_err
		}
	}
	return nil
}

func close_item_stream(item hx_item) {
	if item.close_after_copy && item.src_stream != nil {
		item.src_stream.Close()
	}
}

func match_include_exclude(rules string, path string) bool {
	if strings.TrimSpace(rules) == "" {
		return true
	}
	include := true
	for _, rule := range strings.Split(rules, ",") {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		prefix := rule[0]
		pattern := strings.TrimSpace(rule[1:])
		if pattern == "" {
			continue
		}
		matched, err := filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(path))
		if err != nil {
			continue
		}
		if matched {
			include = prefix != '-'
		}
	}
	return include
}

// ----------------------------------------------------------------------
// TUI.

type hx_tui struct {
	mode            string
	last_rendered   string
	warned_messages []string
}

func (t *hx_tui) warn(msg string) {
	if msg == "" {
		return
	}
	t.warned_messages = append(t.warned_messages, msg)
	fmt.Fprintln(os.Stderr, "warning:", msg)
}

func (t *hx_tui) show_item(item hx_item) {
	line := fmt.Sprintf("%s %s %d", item.type_name, item.dst_full_path, item.size_extracted)
	if line == t.last_rendered {
		return
	}
	t.last_rendered = line
	if t.mode == "ansi" {
		fmt.Fprintf(os.Stderr, "\r%s", line)
		return
	}
	fmt.Fprintln(os.Stderr, line)
}

// ----------------------------------------------------------------------
// CLI.

func main() {
	src, dst, err := parse_args(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := dst.copy(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if dst.tui.mode == "ansi" {
		fmt.Fprintln(os.Stderr)
	}
	_ = src
}

func parse_args(args []string) (hx_src, hx_dst, error) {
	fs := flag.NewFlagSet("hx", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	strip := fs.Int("strip", 0, "")
	skip := fs.Int("skip", 0, "")
	symlinks := fs.Int("symlinks", 1, "")
	download_only := fs.Int("download-only", 0, "")
	do_short := fs.Int("do", 0, "")
	notmp := fs.Int("notmp", 0, "")
	no_tempfile := fs.Int("no-tempfile", 0, "")
	platform := fs.String("platform", runtime.GOOS+"/"+runtime.GOARCH, "")
	plat := fs.String("plat", "", "")
	registry := fs.String("registry", "", "")
	reg := fs.String("reg", "", "")
	target := fs.String("target", "", "")
	target_short := fs.String("t", "", "")
	incexc := fs.String("incexc", "", "")
	quiet := fs.Int("quiet", 0, "")
	q := fs.Int("q", 0, "")
	overwrite := fs.Int("overwrite", 1, "")

	if err := fs.Parse(args); err != nil {
		return hx_src{}, hx_dst{}, err
	}
	rest := fs.Args()
	if len(rest) < 1 || len(rest) > 2 {
		return hx_src{}, hx_dst{}, errors.New("usage: hx [flags] <source> [dest]")
	}

	dst_path := "."
	if len(rest) == 2 {
		dst_path = rest[1]
	}
	abs_dst, err := filepath.Abs(dst_path)
	if err != nil {
		return hx_src{}, hx_dst{}, err
	}

	src := hx_src{
		url:               rest[0],
		registry_base_url: first_non_empty(*registry, *reg),
		target:            first_non_empty(*target, *target_short),
		platform:          first_non_empty(*plat, *platform),
		download_only:     *download_only != 0 || *do_short != 0,
		force_no_tmp:      *notmp != 0 || *no_tempfile != 0,
	}
	tui := hx_tui{mode: "ansi"}
	if *quiet != 0 || *q != 0 {
		tui.mode = "plain"
	}

	dst := hx_dst{
		src:              src,
		path:             abs_dst,
		skip_path_prefix: max_int(*strip, *skip),
		skip_symlinks:    *symlinks == 0,
		include_exclude:  *incexc,
		overwrite:        *overwrite != 0,
		tui:              tui,
	}
	return src, dst, nil
}

func first_non_empty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func max_int(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
