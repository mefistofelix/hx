package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

func TestAPKExtraction(t *testing.T) {
	root_dir := t.TempDir()
	dst_dir := filepath.Join(root_dir, "out")

	run_hx(t, "-target", "edge/main", "-platform", "linux/amd64", "apk://busybox", dst_dir)

	if _, err := os.Stat(filepath.Join(dst_dir, "bin", "busybox")); err != nil {
		t.Fatalf("expected busybox binary, err=%v", err)
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

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
