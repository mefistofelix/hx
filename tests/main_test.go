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

	output := run_hx(t, "-registry", server.URL, "pypi://demo@1.2.3", dst_dir)
	if strings.Contains(output, "error:") {
		t.Fatalf("unexpected output: %q", output)
	}

	data, err := os.ReadFile(filepath.Join(dst_dir, "demo-1.2.3", "pkg", "pypi.txt"))
	must(t, err)
	if string(data) != "from-pypi" {
		t.Fatalf("unexpected pypi content: %q", data)
	}
}

func TestNuGetExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	buffer := &bytes.Buffer{}
	zw := zip.NewWriter(buffer)
	file_writer, err := zw.Create("pkg/nuget.txt")
	must(t, err)
	_, err = file_writer.Write([]byte("from-nuget"))
	must(t, err)
	must(t, zw.Close())

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3-flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(buffer.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	output := run_hx(t, "-registry", server.URL, "nuget://Newtonsoft.Json@13.0.3", dst_dir)
	if strings.Contains(output, "error:") {
		t.Fatalf("unexpected output: %q", output)
	}

	data, err := os.ReadFile(filepath.Join(dst_dir, "pkg", "nuget.txt"))
	must(t, err)
	if string(data) != "from-nuget" {
		t.Fatalf("unexpected nuget content: %q", data)
	}
}

func TestNPMExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")
	archive_data := tar_gz_bytes(t, "package/pkg/npm.txt", "from-npm")

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lodash":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dist-tags":{"latest":"4.17.21"},"versions":{"4.17.21":{"dist":{"tarball":"` + server.URL + `/lodash/-/lodash-4.17.21.tgz"}}}}`))
		case "/lodash/-/lodash-4.17.21.tgz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(archive_data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	output := run_hx(t, "-registry", server.URL, "npm://lodash@4.17.21", dst_dir)
	if strings.Contains(output, "error:") {
		t.Fatalf("unexpected output: %q", output)
	}

	data, err := os.ReadFile(filepath.Join(dst_dir, "package", "pkg", "npm.txt"))
	must(t, err)
	if string(data) != "from-npm" {
		t.Fatalf("unexpected npm content: %q", data)
	}
}

func TestDockerExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")
	layer_one := tar_gz_bytes2(t, map[string]string{
		"root/keep.txt": "keep",
		"root/live.txt": "live",
	})
	layer_two := tar_gz_bytes2(t, map[string]string{
		"root/.wh.keep.txt": "",
	})

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/library/busybox/manifests/latest":
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			_, _ = w.Write([]byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
				"config": {"digest": "sha256:config"},
				"layers": [
					{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "digest": "sha256:layer1"},
					{"mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip", "digest": "sha256:layer2"}
				]
			}`))
		case "/v2/library/busybox/blobs/sha256:layer1":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(layer_one)
		case "/v2/library/busybox/blobs/sha256:layer2":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(layer_two)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	output := run_hx(t, "-registry", server.URL, "docker://busybox:latest", dst_dir)
	if strings.Contains(output, "error:") {
		t.Fatalf("unexpected output: %q", output)
	}

	if _, err := os.Stat(filepath.Join(dst_dir, "root", "keep.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected whiteout to remove keep.txt, err=%v", err)
	}
	data, err := os.ReadFile(filepath.Join(dst_dir, "root", "live.txt"))
	must(t, err)
	if string(data) != "live" {
		t.Fatalf("unexpected docker content: %q", data)
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
	return tar_gz_bytes2(t, map[string]string{name: body})
}

func tar_gz_bytes2(t *testing.T, files map[string]string) []byte {
	t.Helper()
	buffer := &bytes.Buffer{}
	gzw := gzip.NewWriter(buffer)
	tw := tar.NewWriter(gzw)
	for name, body := range files {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}
		must(t, tw.WriteHeader(header))
		_, err := tw.Write([]byte(body))
		must(t, err)
	}
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
