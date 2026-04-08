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
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
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

func TestGitHubRepositoryExtraction(t *testing.T) {
	root_dir := t.TempDir()
	remote_base_dir := filepath.Join(root_dir, "remotes")
	commit_hash := create_remote_repo(t, remote_base_dir, "acme", "demo")
	dst_dir := filepath.Join(root_dir, "out")

	run_hx_env(t, map[string]string{
		"HX_GITHUB_CLONE_BASE_URL": "file:///" + filepath.ToSlash(remote_base_dir),
	}, "https://github.com/acme/demo/commit/"+commit_hash, dst_dir)

	data, err := os.ReadFile(filepath.Join(dst_dir, "pkg", "git.txt"))
	must(t, err)
	if string(data) != "git-data" {
		t.Fatalf("unexpected git content: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dst_dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("expected .git to be skipped, err=%v", err)
	}
}

func run_hx(t *testing.T, args ...string) string {
	t.Helper()
	return run_hx_env(t, nil, args...)
}

func run_hx_env(t *testing.T, extra_env map[string]string, args ...string) string {
	t.Helper()
	command_args := append([]string{"run", "../src", "-quiet"}, args...)
	cmd := exec.Command("go", command_args...)
	cmd.Dir = filepath.Join(project_root(t), "tests")
	env := append(os.Environ(), "GOCACHE="+filepath.Join(project_root(t), "tests_cache", "gocache"))
	for key, value := range extra_env {
		env = append(env, key+"="+value)
	}
	cmd.Env = env
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

func create_remote_repo(t *testing.T, remote_base_dir string, owner string, repo string) string {
	t.Helper()
	remote_repo_dir := filepath.Join(remote_base_dir, owner, repo+".git")
	must(t, os.MkdirAll(filepath.Dir(remote_repo_dir), 0o755))
	_, err := git.PlainInit(remote_repo_dir, true)
	must(t, err)

	work_dir := filepath.Join(t.TempDir(), "work")
	work_repo, err := git.PlainInit(work_dir, false)
	must(t, err)
	must(t, os.MkdirAll(filepath.Join(work_dir, "pkg"), 0o755))
	must(t, os.WriteFile(filepath.Join(work_dir, "pkg", "git.txt"), []byte("git-data"), 0o644))

	worktree, err := work_repo.Worktree()
	must(t, err)
	_, err = worktree.Add("pkg/git.txt")
	must(t, err)
	commit_hash, err := worktree.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "hx-tests",
			Email: "hx-tests@example.invalid",
			When:  time.Now(),
		},
	})
	must(t, err)
	_, err = work_repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{remote_repo_dir},
	})
	must(t, err)
	must(t, work_repo.Push(&git.PushOptions{RemoteName: "origin"}))
	return commit_hash.String()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
