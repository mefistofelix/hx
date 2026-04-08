package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mholt/archives"
)

func TestHTTPArchiveAndSentinel(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "-strip", "1", "https://github.com/go-git/go-billy/archive/refs/heads/master.tar.gz", dst_dir)

	data, err := os.ReadFile(filepath.Join(dst_dir, "file.go"))
	must(t, err)
	if !strings.Contains(string(data), "package memfs") {
		t.Fatalf("unexpected file.go content")
	}

	output := run_hx(t, "-strip", "1", "https://github.com/go-git/go-billy/archive/refs/heads/master.tar.gz", dst_dir)
	if !strings.Contains(output, "already matches") {
		t.Fatalf("expected sentinel skip warning, got %q", output)
	}
}

func TestTarXzExtraction(t *testing.T) {
	root_dir := t.TempDir()
	input_dir := filepath.Join(root_dir, "input")
	dst_dir := filepath.Join(root_dir, "out")
	archive_path := filepath.Join(root_dir, "sample.tar.xz")

	must(t, os.MkdirAll(filepath.Join(input_dir, "nested"), 0o755))
	must(t, os.WriteFile(filepath.Join(input_dir, "nested", "hello.txt"), []byte("hello tar.xz"), 0o644))

	files, err := archives.FilesFromDisk(context.Background(), nil, map[string]string{
		filepath.Join(input_dir, "nested"): "nested",
	})
	must(t, err)
	out_file, err := os.Create(archive_path)
	must(t, err)
	must(t, archives.CompressedArchive{
		Compression: archives.Xz{},
		Archival:    archives.Tar{},
	}.Archive(context.Background(), out_file, files))
	must(t, out_file.Close())

	run_hx(t, archive_path, dst_dir)

	data, err := os.ReadFile(filepath.Join(dst_dir, "nested", "hello.txt"))
	must(t, err)
	if string(data) != "hello tar.xz" {
		t.Fatalf("unexpected tar.xz content")
	}
}

func TestZstdDecompression(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")
	archive_path := filepath.Join(root_dir, "payload.txt.zst")

	out_file, err := os.Create(archive_path)
	must(t, err)
	writer, err := (archives.Zstd{}).OpenWriter(out_file)
	must(t, err)
	_, err = writer.Write([]byte("hello zstd"))
	must(t, err)
	must(t, writer.Close())
	must(t, out_file.Close())

	run_hx(t, archive_path, dst_dir)

	data, err := os.ReadFile(filepath.Join(dst_dir, "payload.txt"))
	must(t, err)
	if string(data) != "hello zstd" {
		t.Fatalf("unexpected zstd content")
	}
}

func TestBooleanStyleFlags(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")
	src_path := filepath.Join(root_dir, "payload.txt")

	must(t, os.WriteFile(src_path, []byte("plain"), 0o644))
	run_hx(t, "-q", "1", "-symlinks", "0", src_path, dst_dir)

	data, err := os.ReadFile(filepath.Join(dst_dir, "payload.txt"))
	must(t, err)
	if string(data) != "plain" {
		t.Fatalf("unexpected boolean flag run content")
	}
}

func TestGitHubRepositoryExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "https://github.com/go-git/go-billy", dst_dir)

	data, err := os.ReadFile(filepath.Join(dst_dir, "go.mod"))
	must(t, err)
	if !strings.Contains(string(data), "github.com/go-git/go-billy/v6") {
		t.Fatalf("unexpected go.mod content")
	}
}

func TestPyPIExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "-strip", "1", "pypi://requests@2.32.3", dst_dir)

	if _, err := os.Stat(filepath.Join(dst_dir, "pyproject.toml")); err != nil {
		t.Fatalf("expected pyproject.toml, err=%v", err)
	}
}

func TestNuGetExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "nuget://Newtonsoft.Json@13.0.3", dst_dir)

	if _, err := os.Stat(filepath.Join(dst_dir, "lib", "net45", "Newtonsoft.Json.dll")); err != nil {
		t.Fatalf("expected Newtonsoft.Json.dll, err=%v", err)
	}
}

func TestWinGetExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "-platform", "windows/amd64", "winget://Git.Git@2.46.0", dst_dir)

	matches, err := filepath.Glob(filepath.Join(dst_dir, "Git-*-64-bit.exe"))
	must(t, err)
	if len(matches) == 0 {
		t.Fatalf("expected Git for Windows installer")
	}
}

func TestNPMExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "npm://lodash@4.17.21", dst_dir)

	if _, err := os.Stat(filepath.Join(dst_dir, "package", "package.json")); err != nil {
		t.Fatalf("expected package.json, err=%v", err)
	}
}

func TestDockerExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "-platform", "linux/amd64", "docker://busybox:1.36.1", dst_dir)

	if _, err := os.Stat(filepath.Join(dst_dir, "bin", "busybox")); err != nil {
		t.Fatalf("expected busybox binary, err=%v", err)
	}
}

func TestDockerDownloadOnly(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "-download-only", "1", "-platform", "linux/amd64", "docker://busybox:1.36.1", dst_dir)

	if _, err := os.Stat(filepath.Join(dst_dir, "manifest.json")); err != nil {
		t.Fatalf("expected manifest.json, err=%v", err)
	}
	layer_files, err := filepath.Glob(filepath.Join(dst_dir, "sha256-*.tar*"))
	must(t, err)
	if len(layer_files) == 0 {
		t.Fatalf("expected docker layer blobs")
	}
}

func TestAPKExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "-target", "v3.22/main", "-platform", "linux/amd64", "apk://curl", dst_dir)

	if _, err := os.Stat(filepath.Join(dst_dir, "usr", "bin", "curl")); err != nil {
		t.Fatalf("expected curl binary, err=%v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dst_dir, "usr", "lib", "libcurl.so.4*"))
	must(t, err)
	if len(matches) == 0 {
		t.Fatalf("expected libcurl dependency files")
	}
}

func TestAPTExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "-registry", "https://deb.debian.org/debian", "-target", "bookworm/main", "-platform", "linux/amd64", "apt://curl", dst_dir)

	if _, err := os.Stat(filepath.Join(dst_dir, "usr", "bin", "curl")); err != nil {
		t.Fatalf("expected curl binary, err=%v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dst_dir, "usr", "lib", "x86_64-linux-gnu", "libcurl.so.4*"))
	must(t, err)
	if len(matches) == 0 {
		t.Fatalf("expected libcurl dependency files")
	}
}

func TestRPMExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "-registry", "https://download.fedoraproject.org/pub/fedora/linux/releases", "-target", "41/Everything", "-platform", "linux/amd64", "rpm://jq", dst_dir)

	if _, err := os.Stat(filepath.Join(dst_dir, "usr", "bin", "jq")); err != nil {
		t.Fatalf("expected jq binary, err=%v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dst_dir, "usr", "lib64", "libonig.so.5*"))
	must(t, err)
	if len(matches) == 0 {
		t.Fatalf("expected libonig dependency files")
	}
}

func run_hx(t *testing.T, args ...string) string {
	t.Helper()
	command_args := append([]string{"run", "../src", "-quiet"}, args...)
	go_exe := os.Getenv("HX_GO_EXE")
	if go_exe == "" {
		for _, candidate := range []string{
			filepath.Join(project_root(t), "build_cache", "go_sdk", "bin", "go.exe"),
			filepath.Join(project_root(t), "build_cache", "go", "bin", "go"),
		} {
			if _, err := os.Stat(candidate); err == nil {
				go_exe = candidate
				break
			}
		}
	}
	if go_exe == "" {
		go_exe = "go"
	}
	cmd := exec.Command(go_exe, command_args...)
	cmd.Dir = filepath.Join(project_root(t), "tests")
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(project_root(t), "tests_cache", "gocache"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hx failed: %v\n%s", err, output)
	}
	return string(output)
}

func project_root(t *testing.T) string {
	t.Helper()
	_, this_file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(this_file))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
