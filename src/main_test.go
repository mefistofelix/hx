package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func Test_parse_args_defaults(t *testing.T) {
	_, dst, err := parse_args([]string{"./src"})
	if err != nil {
		t.Fatalf("parse_args returned error: %v", err)
	}
	if dst.src.url != "./src" {
		t.Fatalf("unexpected src url: %q", dst.src.url)
	}
	if dst.path == "" {
		t.Fatal("expected destination path")
	}
	if dst.tui.mode != "ansi" {
		t.Fatalf("unexpected tui mode: %q", dst.tui.mode)
	}
}

func Test_copy_local_directory_with_skip_and_filters(t *testing.T) {
	temp_dir := t.TempDir()
	src_dir := filepath.Join(temp_dir, "src")
	dst_dir := filepath.Join(temp_dir, "dst")
	if err := os.MkdirAll(filepath.Join(src_dir, "top", "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src_dir, "top", "drop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src_dir, "top", "keep", "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src_dir, "top", "drop", "b.txt"), []byte("beta"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := hx_src{url: src_dir}
	dst := hx_dst{
		src:              src,
		path:             dst_dir,
		skip_path_prefix: 1,
		include_exclude:  "-drop/*,+*",
		overwrite:        true,
		tui:              hx_tui{mode: "plain"},
	}
	if err := dst.copy(); err != nil {
		t.Fatalf("copy returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst_dir, "keep", "a.txt")); err != nil {
		t.Fatalf("expected kept file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst_dir, "drop", "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected dropped file, got err=%v", err)
	}
	if _, err := os.Stat(dst.get_done_sentinel_path()); err != nil {
		t.Fatalf("expected sentinel file: %v", err)
	}
}

func Test_copy_http_file(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer server.Close()

	dst_dir := t.TempDir()
	src := hx_src{url: server.URL + "/artifact.txt"}
	dst := hx_dst{
		src:       src,
		path:      dst_dir,
		overwrite: true,
		tui:       hx_tui{mode: "plain"},
	}
	if err := dst.copy(); err != nil {
		t.Fatalf("copy returned error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dst_dir, "artifact.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "payload" {
		t.Fatalf("unexpected content: %q", string(content))
	}
}

func Test_new_source_iter_state_file_url(t *testing.T) {
	temp_dir := t.TempDir()
	file_path := filepath.Join(temp_dir, "a.txt")
	if err := os.WriteFile(file_path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	src_url := "file://" + filepath.ToSlash(file_path)
	if runtime.GOOS == "windows" {
		src_url = "file:///" + filepath.ToSlash(file_path)
	}
	state, err := (hx_src{url: src_url}).new_source_iter_state()
	if err != nil {
		t.Fatalf("new_source_iter_state returned error: %v", err)
	}
	if filepath.Clean(filepath.FromSlash(state.parsed_url.Path)) != filepath.Clean(file_path) {
		t.Fatalf("unexpected parsed path: %q", state.parsed_url.Path)
	}
}
