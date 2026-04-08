package tests

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalDirectoryCopy(t *testing.T) {
	root_dir := t.TempDir()
	src_dir := filepath.Join(root_dir, "src")
	dst_dir := filepath.Join(root_dir, "dst")
	must(t, os.MkdirAll(filepath.Join(src_dir, "nested"), 0o755))
	must(t, os.WriteFile(filepath.Join(src_dir, "nested", "hello.txt"), []byte("hello"), 0o644))

	run_hx(t, src_dir, dst_dir)

	data, err := os.ReadFile(filepath.Join(dst_dir, "nested", "hello.txt"))
	must(t, err)
	if string(data) != "hello" {
		t.Fatalf("unexpected file content: %q", data)
	}
}

func TestLocalZipExtraction(t *testing.T) {
	root_dir := t.TempDir()
	zip_path := filepath.Join(root_dir, "sample.zip")
	dst_dir := filepath.Join(root_dir, "out")

	buffer := &bytes.Buffer{}
	zw := zip.NewWriter(buffer)
	file_writer, err := zw.Create("pkg/file.txt")
	must(t, err)
	_, err = file_writer.Write([]byte("zip-data"))
	must(t, err)
	must(t, zw.Close())
	must(t, os.WriteFile(zip_path, buffer.Bytes(), 0o644))

	run_hx(t, zip_path, dst_dir)

	data, err := os.ReadFile(filepath.Join(dst_dir, "pkg", "file.txt"))
	must(t, err)
	if string(data) != "zip-data" {
		t.Fatalf("unexpected zip content: %q", data)
	}
}

func TestHTTPArchiveAndSentinel(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")
	archive_data := tar_gz_bytes(t, "pkg/http.txt", "over-http")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive_data)
	}))
	defer server.Close()

	run_hx(t, server.URL+"/sample.tar.gz", dst_dir)
	data, err := os.ReadFile(filepath.Join(dst_dir, "pkg", "http.txt"))
	must(t, err)
	if string(data) != "over-http" {
		t.Fatalf("unexpected http content: %q", data)
	}

	output := run_hx(t, server.URL+"/sample.tar.gz", dst_dir)
	if !strings.Contains(output, "already matches") {
		t.Fatalf("expected sentinel skip warning, got %q", output)
	}
}

func TestHTTPSArchiveInsecureFallback(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")
	archive_data := tar_gz_bytes(t, "pkg/secure.txt", "fallback-ok")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive_data)
	}))
	defer server.Close()

	output := run_hx(t, server.URL+"/sample.tar.gz", dst_dir)
	data, err := os.ReadFile(filepath.Join(dst_dir, "pkg", "secure.txt"))
	must(t, err)
	if string(data) != "fallback-ok" {
		t.Fatalf("unexpected https content: %q", data)
	}
	if !strings.Contains(output, "retrying insecurely") {
		t.Fatalf("expected insecure retry warning, got %q", output)
	}
}

func TestPyPIExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")
	archive_data := tar_gz_bytes(t, "demo-1.2.3/pkg/pypi.txt", "from-pypi")

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pypi/demo/1.2.3/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"urls":[{"url":"` + server.URL + `/packages/demo-1.2.3.tar.gz","filename":"demo-1.2.3.tar.gz","packagetype":"sdist"}]}`))
		case "/packages/demo-1.2.3.tar.gz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(archive_data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	output := run_hx(t, "-registry", server.URL, "-target", "1.2.3", "pypi://demo", dst_dir)
	if strings.Contains(output, "error:") {
		t.Fatalf("unexpected output: %q", output)
	}

	data, err := os.ReadFile(filepath.Join(dst_dir, "demo-1.2.3", "pkg", "pypi.txt"))
	must(t, err)
	if string(data) != "from-pypi" {
		t.Fatalf("unexpected pypi content: %q", data)
	}
}

func run_hx(t *testing.T, args ...string) string {
	t.Helper()
	command_args := append([]string{"run", "../src", "-quiet"}, args...)
	cmd := exec.Command("go", command_args...)
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

func tar_gz_bytes(t *testing.T, name string, body string) []byte {
	t.Helper()
	buffer := &bytes.Buffer{}
	gzw := gzip.NewWriter(buffer)
	tw := tar.NewWriter(gzw)
	header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
	must(t, tw.WriteHeader(header))
	_, err := tw.Write([]byte(body))
	must(t, err)
	must(t, tw.Close())
	must(t, gzw.Close())
	return buffer.Bytes()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
