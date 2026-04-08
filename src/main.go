package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
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
	"github.com/klauspost/compress/zstd"
	"github.com/mholt/archives"
	rpmutils "github.com/sassoftware/go-rpmutils"
	"github.com/ulikunitz/xz"
	"gopkg.in/yaml.v3"
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

type bool_flag struct {
	value *bool
}

var (
	tar_gz_suffixes      = []string{".tar.gz", ".tgz"}
	tar_suffix           = ".tar"
	gzip_suffix          = ".gz"
	apk_suffix           = ".apk"
	deb_suffix           = ".deb"
	rpm_suffix           = ".rpm"
	zip_suffixes         = []string{".zip", ".nupkg"}
	archives_suffixes    = []string{".7z", ".rar", ".cpio", ".br", ".bz2", ".lz", ".lz4", ".mz", ".s2", ".sz", ".xz", ".zz", ".zst"}
	git_suffix           = ".git"
	github_host          = "github.com"
	default_download     = "download"
	done_sentinel_prefix = ".hx.done."
	done_sentinel_suffix = ".json"
	tls_retry_message    = "warning: https certificate verification failed, retrying insecurely"
	apt_default_registry = "https://deb.debian.org/debian"
	apt_default_target   = "stable/main"
	rpm_default_registry = "https://download.fedoraproject.org/pub/fedora/linux/releases"
	rpm_default_target   = "41/Everything"
	winget_default_api   = "https://api.github.com/repos/microsoft/winget-pkgs/contents/manifests"
	hex_hash_rx          = regexp.MustCompile(`^[0-9a-f]{40}$`)
	tls_error_rx         = regexp.MustCompile(`(?i)(x509:|certificate|tls:)`)
	apt_dep_name_rx      = regexp.MustCompile(`[a-z0-9][a-z0-9+.-]*`)
)

// -----------------------------------------------------------------------------
// TUI
// -----------------------------------------------------------------------------

func (b bool_flag) String() string {
	if b.value == nil || !*b.value {
		return "0"
	}
	return "1"
}

func (b bool_flag) Set(raw_value string) error {
	switch strings.TrimSpace(strings.ToLower(raw_value)) {
	case "", "1", "true", "t", "yes", "y", "on":
		*b.value = true
		return nil
	case "0", "false", "f", "no", "n", "off":
		*b.value = false
		return nil
	default:
		return fmt.Errorf("invalid boolean value: %s", raw_value)
	}
}

func looks_like_bool_value(raw_value string) bool {
	switch strings.TrimSpace(strings.ToLower(raw_value)) {
	case "0", "1", "true", "false", "t", "f", "yes", "no", "y", "n", "on", "off":
		return true
	default:
		return false
	}
}

func normalize_bool_flag_args(args []string, bool_flags map[string]bool) []string {
	normalized := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !bool_flags[arg] || strings.Contains(arg, "=") {
			normalized = append(normalized, arg)
			continue
		}
		if i+1 < len(args) && looks_like_bool_value(args[i+1]) {
			normalized = append(normalized, arg+"="+args[i+1])
			i++
			continue
		}
		normalized = append(normalized, arg+"=1")
	}
	return normalized
}

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
	case "npm":
		return s.items_from_npm(src_url, yield)
	case "winget":
		return s.items_from_winget(src_url, yield)
	case "docker":
		return s.items_from_docker(src_url, yield)
	case "apt":
		return s.items_from_apt(src_url, yield)
	case "rpm":
		return s.items_from_rpm(src_url, yield)
	case "apk":
		return s.items_from_apk(src_url, yield)
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

func (s hx_src) items_from_npm(src_url *url.URL, yield func(hx_item, error) bool) error {
	package_name := src_url.Host
	version := ""
	if src_url.User != nil {
		package_name = src_url.User.Username()
		version = src_url.Host
	}
	if package_name == "" || package_name == "." {
		return errors.New("npm source requires a package name")
	}

	registry_base_url := strings.TrimRight(s.registry_base_url, "/")
	if registry_base_url == "" {
		registry_base_url = "https://registry.npmjs.org"
	}

	metadata_url := registry_base_url + "/" + package_name
	resp, insecure_retry, err := http_get(metadata_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("npm metadata request failed: %s", resp.Status)
	}

	var payload struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist struct {
				Tarball string `json:"tarball"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if version == "" {
		version = payload.DistTags["latest"]
	}
	if version == "" {
		return errors.New("npm metadata returned no version")
	}
	package_version, ok := payload.Versions[version]
	if !ok || package_version.Dist.Tarball == "" {
		return errors.New("npm metadata returned no tarball for selected version")
	}

	tarball_url := package_version.Dist.Tarball
	tarball_resp, insecure_retry, err := http_get(tarball_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	if tarball_resp.StatusCode < 200 || tarball_resp.StatusCode > 299 {
		defer tarball_resp.Body.Close()
		return fmt.Errorf("npm download failed: %s", tarball_resp.Status)
	}
	if s.download_only {
		yield(hx_item{
			src_stream:      tarball_resp.Body,
			type_name:       "file",
			src_url:         tarball_url,
			src_full_path:   path.Base(tarball_url),
			size_compressed: tarball_resp.ContentLength,
			size:            tarball_resp.ContentLength,
		}, nil)
		return nil
	}

	return stream_items(path.Base(tarball_url), tarball_url, tarball_resp.ContentLength, tarball_resp.Body, yield)
}

func (s hx_src) items_from_winget(src_url *url.URL, yield func(hx_item, error) bool) error {
	package_id := src_url.Host
	version := ""
	if src_url.User != nil {
		package_id = src_url.User.Username()
		version = src_url.Host
	}
	if package_id == "" || package_id == "." {
		return errors.New("winget source requires a package identifier")
	}

	registry_base_url := strings.TrimRight(s.registry_base_url, "/")
	if registry_base_url == "" {
		registry_base_url = winget_default_api
	}

	package_parts := strings.Split(package_id, ".")
	if len(package_parts) < 2 || package_parts[0] == "" {
		return errors.New("winget source requires an identifier like Publisher.Package")
	}
	manifest_dir := "/" + strings.ToLower(package_parts[0][:1]) + "/" + strings.Join(package_parts, "/")

	type github_content struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		Type        string `json:"type"`
		DownloadURL string `json:"download_url"`
	}

	if version == "" {
		body, _, err := http_get_with_headers(registry_base_url+manifest_dir, map[string]string{"User-Agent": "hx"})
		if err != nil {
			return err
		}
		defer body.Close()

		entries := []github_content{}
		if err := json.NewDecoder(body).Decode(&entries); err != nil {
			return err
		}
		best_version := ""
		best_version_key := ""
		for _, entry := range entries {
			if entry.Type != "dir" || entry.Name == "" {
				continue
			}
			current_key := ""
			for _, token := range strings.FieldsFunc(strings.ToLower(entry.Name), func(r rune) bool {
				return (r < '0' || r > '9') && (r < 'a' || r > 'z')
			}) {
				if token == "" {
					continue
				}
				if current_key != "" {
					current_key += "\x00"
				}
				if token[0] >= '0' && token[0] <= '9' {
					current_key += fmt.Sprintf("%08s", token)
				} else {
					current_key += token
				}
			}
			if current_key > best_version_key {
				best_version = entry.Name
				best_version_key = current_key
			}
		}
		if best_version == "" {
			return errors.New("winget metadata returned no versions")
		}
		version = best_version
	}

	body, _, err := http_get_with_headers(registry_base_url+manifest_dir+"/"+version, map[string]string{"User-Agent": "hx"})
	if err != nil {
		return err
	}
	defer body.Close()

	entries := []github_content{}
	if err := json.NewDecoder(body).Decode(&entries); err != nil {
		return err
	}

	installer_manifest_url := ""
	for _, entry := range entries {
		if entry.Type != "file" || !strings.HasSuffix(strings.ToLower(entry.Name), ".yaml") {
			continue
		}
		lower_name := strings.ToLower(entry.Name)
		if strings.Contains(lower_name, ".installer.") || strings.HasSuffix(lower_name, ".installer.yaml") {
			installer_manifest_url = entry.DownloadURL
			break
		}
		if installer_manifest_url == "" {
			installer_manifest_url = entry.DownloadURL
		}
	}
	if installer_manifest_url == "" {
		return errors.New("winget metadata returned no installer manifest")
	}

	manifest_resp, insecure_retry, err := http_get(installer_manifest_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	defer manifest_resp.Body.Close()
	if manifest_resp.StatusCode < 200 || manifest_resp.StatusCode > 299 {
		return fmt.Errorf("winget manifest request failed: %s", manifest_resp.Status)
	}

	var manifest struct {
		PackageVersion string `yaml:"PackageVersion"`
		Installers     []struct {
			Architecture string `yaml:"Architecture"`
			InstallerURL string `yaml:"InstallerUrl"`
		} `yaml:"Installers"`
	}
	if err := yaml.NewDecoder(manifest_resp.Body).Decode(&manifest); err != nil {
		return err
	}
	if len(manifest.Installers) == 0 {
		return errors.New("winget installer manifest returned no installers")
	}

	selected_arch := split_platform_name(s.platform)[1]
	switch selected_arch {
	case "", "amd64":
		selected_arch = "x64"
	case "386":
		selected_arch = "x86"
	case "arm64":
		selected_arch = "arm64"
	}

	installer_url := ""
	for _, installer := range manifest.Installers {
		if installer.InstallerURL == "" {
			continue
		}
		if installer.Architecture == "" || strings.EqualFold(installer.Architecture, selected_arch) {
			installer_url = installer.InstallerURL
			break
		}
	}
	if installer_url == "" {
		installer_url = manifest.Installers[0].InstallerURL
	}
	if installer_url == "" {
		return errors.New("winget installer manifest returned no installer url")
	}

	installer_resp, insecure_retry, err := http_get(installer_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	if installer_resp.StatusCode < 200 || installer_resp.StatusCode > 299 {
		defer installer_resp.Body.Close()
		return fmt.Errorf("winget download failed: %s", installer_resp.Status)
	}
	if s.download_only {
		yield(hx_item{
			src_stream:      installer_resp.Body,
			type_name:       "file",
			src_url:         installer_url,
			src_full_path:   path.Base(strings.Split(installer_url, "?")[0]),
			size_compressed: installer_resp.ContentLength,
			size:            installer_resp.ContentLength,
		}, nil)
		return nil
	}

	return stream_items(path.Base(strings.Split(installer_url, "?")[0]), installer_url, installer_resp.ContentLength, installer_resp.Body, yield)
}

func (s hx_src) items_from_docker(src_url *url.URL, yield func(hx_item, error) bool) error {
	image_name := strings.Trim(strings.TrimSpace(src_url.Opaque), "/")
	if image_name == "" {
		image_name = strings.Trim(strings.TrimSpace(src_url.Host+src_url.Path), "/")
	}
	if image_name == "" {
		return errors.New("docker source requires an image reference")
	}

	image_tag := "latest"
	if at := strings.LastIndex(image_name, "@"); at > 0 && at < len(image_name)-1 {
		image_tag = image_name[at+1:]
		image_name = image_name[:at]
	} else if colon := strings.LastIndex(image_name, ":"); colon > strings.LastIndex(image_name, "/") {
		image_tag = image_name[colon+1:]
		image_name = image_name[:colon]
	}
	if !strings.Contains(image_name, "/") {
		image_name = "library/" + image_name
	}

	registry_base_url := strings.TrimRight(s.registry_base_url, "/")
	if registry_base_url == "" {
		registry_base_url = "https://registry-1.docker.io"
	}

	manifest, err := fetch_docker_manifest(registry_base_url, image_name, image_tag, s.platform)
	if err != nil {
		return err
	}
	if s.download_only {
		return errors.New("download-only is not implemented for docker sources")
	}

	work_dir, err := os.MkdirTemp("", "hx-docker-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work_dir)

	for _, layer := range manifest.Layers {
		if err := apply_docker_layer(work_dir, registry_base_url, image_name, layer); err != nil {
			return err
		}
	}

	return walk_local_dir(work_dir, false, src_url.String(), yield)
}

func (s hx_src) items_from_apt(src_url *url.URL, yield func(hx_item, error) bool) error {
	package_name := src_url.Host
	version := ""
	if src_url.User != nil {
		package_name = src_url.User.Username()
		version = src_url.Host
	}
	if package_name == "" || package_name == "." {
		return errors.New("apt source requires a package name")
	}

	registry_base_url := strings.TrimRight(s.registry_base_url, "/")
	if registry_base_url == "" {
		registry_base_url = apt_default_registry
	}
	repo_target := strings.Trim(strings.TrimSpace(s.target), "/")
	if repo_target == "" {
		repo_target = apt_default_target
	}

	platform_arch := split_platform_name(s.platform)[1]
	if platform_arch == "" {
		platform_arch = runtime.GOARCH
	}
	switch platform_arch {
	case "386":
		platform_arch = "i386"
	}

	index_url := registry_base_url + "/dists/" + repo_target + "/binary-" + platform_arch + "/Packages.gz"
	resp, insecure_retry, err := http_get(index_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("apt index request failed: %s", resp.Status)
	}

	gz_reader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gz_reader.Close()
	index_data, err := io.ReadAll(gz_reader)
	if err != nil {
		return err
	}

	trimmed_index := strings.ReplaceAll(string(index_data), "\r\n", "\n")
	apt_packages := map[string][]map[string]string{}
	for _, block := range strings.Split(trimmed_index, "\n\n") {
		fields := map[string]string{}
		last_key := ""
		for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				if last_key != "" {
					fields[last_key] += "\n" + strings.TrimSpace(line)
				}
				continue
			}
			pair := strings.SplitN(line, ": ", 2)
			if len(pair) != 2 {
				continue
			}
			fields[pair[0]] = pair[1]
			last_key = pair[0]
		}
		if fields["Package"] != "" && fields["Filename"] != "" {
			apt_packages[fields["Package"]] = append(apt_packages[fields["Package"]], fields)
		}
	}

	resolved_packages := map[string]map[string]string{}
	resolving_packages := map[string]bool{}
	resolved_order := make([]map[string]string, 0)

	var resolve_package func(string, string) error
	resolve_package = func(current_package string, requested_version string) error {
		if resolved_packages[current_package] != nil {
			if requested_version == "" || resolved_packages[current_package]["Version"] == requested_version {
				return nil
			}
		}
		if resolving_packages[current_package] {
			return nil
		}

		selected_package := map[string]string(nil)
		for _, candidate := range apt_packages[current_package] {
			if requested_version != "" && candidate["Version"] != requested_version {
				continue
			}
			selected_package = candidate
			break
		}
		if selected_package == nil {
			return fmt.Errorf("apt index returned no matching package: %s", current_package)
		}

		resolving_packages[current_package] = true
		for _, dep_group_list := range []string{selected_package["Pre-Depends"], selected_package["Depends"]} {
			for _, dep_group := range strings.Split(dep_group_list, ",") {
				dependency_name := ""
				for _, dep_alternative := range strings.Split(dep_group, "|") {
					dependency_match := apt_dep_name_rx.FindString(strings.ToLower(dep_alternative))
					if dependency_match == "" {
						continue
					}
					if len(apt_packages[dependency_match]) == 0 {
						continue
					}
					dependency_name = dependency_match
					break
				}
				if dependency_name == "" {
					continue
				}
				if err := resolve_package(dependency_name, ""); err != nil {
					return err
				}
			}
		}
		resolving_packages[current_package] = false
		resolved_packages[current_package] = selected_package
		resolved_order = append(resolved_order, selected_package)
		return nil
	}

	if err := resolve_package(package_name, version); err != nil {
		return err
	}

	for _, resolved_package := range resolved_order {
		artifact_url := registry_base_url + "/" + strings.TrimPrefix(resolved_package["Filename"], "/")
		artifact_resp, insecure_retry, err := http_get(artifact_url)
		if err != nil {
			return err
		}
		if insecure_retry {
			fmt.Fprintln(os.Stderr, tls_retry_message)
		}
		if artifact_resp.StatusCode < 200 || artifact_resp.StatusCode > 299 {
			defer artifact_resp.Body.Close()
			return fmt.Errorf("apt download failed: %s", artifact_resp.Status)
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
			continue
		}
		if err := stream_items(path.Base(artifact_url), artifact_url, artifact_resp.ContentLength, artifact_resp.Body, yield); err != nil {
			return err
		}
	}
	return nil
}

func (s hx_src) items_from_rpm(src_url *url.URL, yield func(hx_item, error) bool) error {
	package_name := src_url.Host
	version := ""
	if src_url.User != nil {
		package_name = src_url.User.Username()
		version = src_url.Host
	}
	if package_name == "" || package_name == "." {
		return errors.New("rpm source requires a package name")
	}

	registry_base_url := strings.TrimRight(s.registry_base_url, "/")
	if registry_base_url == "" {
		registry_base_url = rpm_default_registry
	}
	repo_target := strings.Trim(strings.TrimSpace(s.target), "/")
	if repo_target == "" {
		repo_target = rpm_default_target
	}

	platform_arch := split_platform_name(s.platform)[1]
	if platform_arch == "" {
		platform_arch = runtime.GOARCH
	}
	switch platform_arch {
	case "amd64":
		platform_arch = "x86_64"
	case "386":
		platform_arch = "i686"
	case "arm64":
		platform_arch = "aarch64"
	}

	repo_base_url := registry_base_url + "/" + repo_target + "/" + platform_arch + "/os"
	repomd_url := repo_base_url + "/repodata/repomd.xml"
	repomd_resp, insecure_retry, err := http_get(repomd_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	defer repomd_resp.Body.Close()
	if repomd_resp.StatusCode < 200 || repomd_resp.StatusCode > 299 {
		return fmt.Errorf("rpm metadata request failed: %s", repomd_resp.Status)
	}

	var repomd struct {
		Data []struct {
			Type     string `xml:"type,attr"`
			Location struct {
				Href string `xml:"href,attr"`
			} `xml:"location"`
		} `xml:"data"`
	}
	if err := xml.NewDecoder(repomd_resp.Body).Decode(&repomd); err != nil {
		return err
	}

	primary_href := ""
	for _, data := range repomd.Data {
		if data.Type == "primary" {
			primary_href = data.Location.Href
			break
		}
	}
	if primary_href == "" {
		return errors.New("rpm metadata returned no primary index")
	}

	primary_url := repo_base_url + "/" + strings.TrimPrefix(primary_href, "/")
	primary_resp, insecure_retry, err := http_get(primary_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	defer primary_resp.Body.Close()
	if primary_resp.StatusCode < 200 || primary_resp.StatusCode > 299 {
		return fmt.Errorf("rpm primary index request failed: %s", primary_resp.Status)
	}

	var primary_reader io.Reader = primary_resp.Body
	if strings.HasSuffix(strings.ToLower(primary_url), gzip_suffix) {
		gz_reader, err := gzip.NewReader(primary_resp.Body)
		if err != nil {
			return err
		}
		defer gz_reader.Close()
		primary_reader = gz_reader
	}

	decoder := xml.NewDecoder(primary_reader)
	rpm_packages := map[string][]map[string]any{}
	rpm_providers := map[string]string{}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "package" {
			continue
		}

		var pkg struct {
			Name    string `xml:"name"`
			Arch    string `xml:"arch"`
			Version struct {
				Epoch string `xml:"epoch,attr"`
				Ver   string `xml:"ver,attr"`
				Rel   string `xml:"rel,attr"`
			} `xml:"version"`
			Location struct {
				Href string `xml:"href,attr"`
			} `xml:"location"`
			Format struct {
				Provides []struct {
					Name string `xml:"name,attr"`
				} `xml:"provides>entry"`
				Requires []struct {
					Name string `xml:"name,attr"`
				} `xml:"requires>entry"`
			} `xml:"format"`
		}
		if err := decoder.DecodeElement(&pkg, &start); err != nil {
			return err
		}
		if pkg.Name == "" || pkg.Location.Href == "" {
			continue
		}
		if pkg.Arch != platform_arch && pkg.Arch != "noarch" {
			continue
		}

		version_text := pkg.Version.Ver
		if pkg.Version.Rel != "" {
			version_text += "-" + pkg.Version.Rel
		}
		full_version_text := version_text
		if pkg.Version.Epoch != "" && pkg.Version.Epoch != "0" {
			full_version_text = pkg.Version.Epoch + ":" + version_text
		}

		rpm_package := map[string]any{
			"name":     pkg.Name,
			"arch":     pkg.Arch,
			"version":  version_text,
			"fullver":  full_version_text,
			"location": pkg.Location.Href,
			"requires": []string{},
			"provides": []string{},
		}
		for _, provide := range pkg.Format.Provides {
			if provide.Name == "" {
				continue
			}
			rpm_package["provides"] = append(rpm_package["provides"].([]string), provide.Name)
			if rpm_providers[provide.Name] == "" {
				rpm_providers[provide.Name] = pkg.Name
			}
		}
		if rpm_providers[pkg.Name] == "" {
			rpm_providers[pkg.Name] = pkg.Name
		}
		for _, require := range pkg.Format.Requires {
			if require.Name == "" || strings.HasPrefix(require.Name, "rpmlib(") {
				continue
			}
			rpm_package["requires"] = append(rpm_package["requires"].([]string), require.Name)
		}
		rpm_packages[pkg.Name] = append(rpm_packages[pkg.Name], rpm_package)
	}

	resolved_packages := map[string]map[string]any{}
	resolving_packages := map[string]bool{}
	resolved_order := make([]map[string]any, 0)

	var resolve_package func(string, string) error
	resolve_package = func(current_package string, requested_version string) error {
		if resolved_packages[current_package] != nil {
			if requested_version == "" ||
				resolved_packages[current_package]["version"] == requested_version ||
				resolved_packages[current_package]["fullver"] == requested_version {
				return nil
			}
		}
		if resolving_packages[current_package] {
			return nil
		}

		selected_package := map[string]any(nil)
		for _, candidate := range rpm_packages[current_package] {
			if requested_version != "" &&
				candidate["version"] != requested_version &&
				candidate["fullver"] != requested_version {
				continue
			}
			selected_package = candidate
			if candidate["arch"] == platform_arch {
				break
			}
		}
		if selected_package == nil {
			return fmt.Errorf("rpm metadata returned no matching package: %s", current_package)
		}

		resolving_packages[current_package] = true
		for _, dependency_name := range selected_package["requires"].([]string) {
			if dependency_name == "" {
				continue
			}
			if rpm_providers[dependency_name] != "" {
				dependency_name = rpm_providers[dependency_name]
			}
			if rpm_packages[dependency_name] == nil || dependency_name == current_package {
				continue
			}
			if err := resolve_package(dependency_name, ""); err != nil {
				return err
			}
		}
		resolving_packages[current_package] = false
		resolved_packages[current_package] = selected_package
		resolved_order = append(resolved_order, selected_package)
		return nil
	}

	if err := resolve_package(package_name, version); err != nil {
		return err
	}

	for _, resolved_package := range resolved_order {
		artifact_url := repo_base_url + "/" + strings.TrimPrefix(resolved_package["location"].(string), "/")
		artifact_resp, insecure_retry, err := http_get(artifact_url)
		if err != nil {
			return err
		}
		if insecure_retry {
			fmt.Fprintln(os.Stderr, tls_retry_message)
		}
		if artifact_resp.StatusCode < 200 || artifact_resp.StatusCode > 299 {
			defer artifact_resp.Body.Close()
			return fmt.Errorf("rpm download failed: %s", artifact_resp.Status)
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
			continue
		}
		if err := stream_items(path.Base(artifact_url), artifact_url, artifact_resp.ContentLength, artifact_resp.Body, yield); err != nil {
			return err
		}
	}
	return nil
}

func (s hx_src) items_from_apk(src_url *url.URL, yield func(hx_item, error) bool) error {
	package_name := src_url.Host
	version := ""
	if src_url.User != nil {
		package_name = src_url.User.Username()
		version = src_url.Host
	}
	if package_name == "" || package_name == "." {
		return errors.New("apk source requires a package name")
	}

	registry_base_url := strings.TrimRight(s.registry_base_url, "/")
	if registry_base_url == "" {
		registry_base_url = "https://dl-cdn.alpinelinux.org/alpine"
	}
	repo_target := strings.Trim(strings.TrimSpace(s.target), "/")
	if repo_target == "" {
		repo_target = "edge/main"
	}
	platform_arch := split_platform_name(s.platform)[1]
	if platform_arch == "" {
		platform_arch = runtime.GOARCH
	}
	switch platform_arch {
	case "amd64":
		platform_arch = "x86_64"
	case "arm64":
		platform_arch = "aarch64"
	}

	index_url := registry_base_url + "/" + repo_target + "/" + platform_arch + "/APKINDEX.tar.gz"
	resp, insecure_retry, err := http_get(index_url)
	if err != nil {
		return err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("apk index request failed: %s", resp.Status)
	}

	index_data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	index_text, err := apkindex_text(index_data)
	if err != nil {
		return err
	}

	apk_packages := map[string]map[string]string{}
	apk_providers := map[string]string{}
	for _, block := range strings.Split(index_text, "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
			if len(line) < 3 || line[1] != ':' {
				continue
			}
			fields[line[:1]] = line[2:]
		}
		if fields["P"] == "" || fields["V"] == "" {
			continue
		}
		apk_packages[fields["P"]] = fields
		apk_providers[fields["P"]] = fields["P"]
		for _, provider := range strings.Fields(fields["p"]) {
			provider_name := strings.TrimSpace(provider)
			if provider_name == "" {
				continue
			}
			provider_name = strings.FieldsFunc(provider_name, func(r rune) bool {
				return r == '=' || r == '<' || r == '>' || r == '~'
			})[0]
			if provider_name != "" && apk_providers[provider_name] == "" {
				apk_providers[provider_name] = fields["P"]
			}
		}
	}

	resolved_packages := map[string]map[string]string{}
	resolving_packages := map[string]bool{}
	resolved_order := make([]map[string]string, 0)

	var resolve_package func(string, string) error
	resolve_package = func(current_package string, requested_version string) error {
		if resolved_packages[current_package] != nil {
			if requested_version == "" || resolved_packages[current_package]["V"] == requested_version {
				return nil
			}
		}
		if resolving_packages[current_package] {
			return nil
		}

		selected_package := apk_packages[current_package]
		if selected_package == nil {
			return fmt.Errorf("apk index returned no matching package: %s", current_package)
		}
		if requested_version != "" && selected_package["V"] != requested_version {
			return fmt.Errorf("apk index returned no matching package version: %s@%s", current_package, requested_version)
		}

		resolving_packages[current_package] = true
		for _, dep_token := range strings.Fields(selected_package["D"]) {
			if strings.HasPrefix(dep_token, "!") {
				continue
			}
			dependency_name := strings.FieldsFunc(dep_token, func(r rune) bool {
				return r == '=' || r == '<' || r == '>' || r == '~'
			})
			if len(dependency_name) == 0 || dependency_name[0] == "" {
				continue
			}
			dependency_key := dependency_name[0]
			if dependency_key == "" {
				continue
			}
			if apk_providers[dependency_key] != "" {
				dependency_key = apk_providers[dependency_key]
			}
			if apk_packages[dependency_key] == nil {
				continue
			}
			if err := resolve_package(dependency_key, ""); err != nil {
				return err
			}
		}
		resolving_packages[current_package] = false
		resolved_packages[current_package] = selected_package
		resolved_order = append(resolved_order, selected_package)
		return nil
	}

	if err := resolve_package(package_name, version); err != nil {
		return err
	}

	for _, resolved_package := range resolved_order {
		artifact_name := resolved_package["P"] + "-" + resolved_package["V"] + ".apk"
		artifact_url := registry_base_url + "/" + repo_target + "/" + platform_arch + "/" + artifact_name
		artifact_resp, insecure_retry, err := http_get(artifact_url)
		if err != nil {
			return err
		}
		if insecure_retry {
			fmt.Fprintln(os.Stderr, tls_retry_message)
		}
		if artifact_resp.StatusCode < 200 || artifact_resp.StatusCode > 299 {
			defer artifact_resp.Body.Close()
			return fmt.Errorf("apk download failed: %s", artifact_resp.Status)
		}
		if s.download_only {
			yield(hx_item{
				src_stream:      artifact_resp.Body,
				type_name:       "file",
				src_url:         artifact_url,
				src_full_path:   artifact_name,
				size_compressed: artifact_resp.ContentLength,
				size:            artifact_resp.ContentLength,
			}, nil)
			continue
		}
		if err := stream_items(artifact_name, artifact_url, artifact_resp.ContentLength, artifact_resp.Body, yield); err != nil {
			return err
		}
	}
	return nil
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
	case strings.HasSuffix(lower_name, tar_gz_suffixes[0]),
		strings.HasSuffix(lower_name, tar_gz_suffixes[1]),
		strings.HasSuffix(lower_name, apk_suffix):
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
	case strings.HasSuffix(lower_name, deb_suffix):
		defer src_stream.Close()
		return deb_items(src_url, src_stream, yield)
	case strings.HasSuffix(lower_name, rpm_suffix):
		defer src_stream.Close()
		return rpm_items(src_url, src_stream, yield)
	case has_suffix_fold(lower_name, archives_suffixes...):
		defer src_stream.Close()
		return archives_items(name, src_url, src_size, src_stream, yield)
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

func archives_items(name string, src_url string, src_size int64, src_stream io.ReadCloser, yield func(hx_item, error) bool) error {
	format, rewinded_stream, err := archives.Identify(context.Background(), filepath.Base(name), src_stream)
	if err != nil {
		return err
	}

	if extractor, ok := format.(archives.Extractor); ok {
		return extractor.Extract(context.Background(), rewinded_stream, func(ctx context.Context, info archives.FileInfo) error {
			normalized_path := normalize_rel_path(info.NameInArchive)
			if normalized_path == "" {
				if cleaned_name := strings.Trim(path.Clean(strings.ReplaceAll(info.NameInArchive, "\\", "/")), "/"); cleaned_name == "" || cleaned_name == "." {
					return nil
				}
				return fmt.Errorf("invalid archive path: %s", info.NameInArchive)
			}

			item := hx_item{
				type_name:      "file",
				src_url:        src_url,
				src_full_path:  normalized_path,
				src_link_path:  info.LinkTarget,
				size_extracted: info.Size(),
				size:           info.Size(),
			}
			if info.IsDir() {
				item.type_name = "dir"
				item.size = 0
			} else if info.Mode()&os.ModeSymlink != 0 {
				item.type_name = "link"
				item.size = 0
			} else {
				file_reader, err := info.Open()
				if err != nil {
					return err
				}
				item.src_stream = file_reader
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

	if decompressor, ok := format.(archives.Decompressor); ok {
		decompressed_stream, err := decompressor.OpenReader(rewinded_stream)
		if err != nil {
			return err
		}
		yield(hx_item{
			src_stream:      decompressed_stream,
			type_name:       "file",
			src_url:         src_url,
			src_full_path:   strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)),
			size_compressed: src_size,
		}, nil)
		return nil
	}

	return archives.NoMatch
}

func rpm_items(src_url string, src_stream io.Reader, yield func(hx_item, error) bool) error {
	pkg, err := rpmutils.ReadRpm(src_stream)
	if err != nil {
		return err
	}
	payload_reader, err := pkg.PayloadReaderExtended()
	if err != nil {
		return err
	}
	for {
		header, err := payload_reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		normalized_path := normalize_rel_path(header.Name())
		if normalized_path == "" {
			if cleaned_header := strings.Trim(path.Clean(strings.ReplaceAll(header.Name(), "\\", "/")), "/"); cleaned_header == "" || cleaned_header == "." {
				continue
			}
			return fmt.Errorf("invalid archive path: %s", header.Name())
		}

		item := hx_item{
			type_name:      "file",
			src_url:        src_url,
			src_full_path:  normalized_path,
			size_extracted: header.Size(),
			size:           header.Size(),
		}
		switch header.Mode() & 0o170000 {
		case 0o040000:
			item.type_name = "dir"
			item.size = 0
		case 0o120000:
			item.type_name = "link"
			item.src_link_path = header.Linkname()
			item.size = 0
		default:
			if payload_reader.IsLink() {
				item.size = 0
				item.src_stream = io.NopCloser(strings.NewReader(""))
			} else {
				item.src_stream = io.NopCloser(io.LimitReader(payload_reader, header.Size()))
			}
		}
		if !yield(item, nil) {
			if item.src_stream != nil {
				_ = item.src_stream.Close()
			}
			return nil
		}
	}
}

func deb_items(src_url string, src_stream io.Reader, yield func(hx_item, error) bool) error {
	header := make([]byte, 8)
	if _, err := io.ReadFull(src_stream, header); err != nil {
		return err
	}
	if string(header) != "!<arch>\n" {
		return errors.New("invalid deb archive header")
	}

	for {
		file_header := make([]byte, 60)
		_, err := io.ReadFull(src_stream, file_header)
		if errors.Is(err, io.EOF) {
			return errors.New("deb archive has no data tar member")
		}
		if err != nil {
			return err
		}
		if string(file_header[58:60]) != "`\n" {
			return errors.New("invalid deb archive file header")
		}

		member_name := strings.TrimSpace(string(file_header[:16]))
		member_name = strings.TrimSuffix(member_name, "/")
		member_size_text := strings.TrimSpace(string(file_header[48:58]))
		var member_size int
		if _, err := fmt.Sscanf(member_size_text, "%d", &member_size); err != nil {
			return err
		}

		member_reader := io.LimitReader(src_stream, int64(member_size))
		if strings.HasPrefix(member_name, "data.tar") {
			switch {
			case strings.HasSuffix(member_name, ".gz"):
				gz_reader, err := gzip.NewReader(member_reader)
				if err != nil {
					return err
				}
				defer gz_reader.Close()
				return tar_items(tar.NewReader(gz_reader), src_url, yield)
			case strings.HasSuffix(member_name, ".xz"):
				xz_reader, err := xz.NewReader(member_reader)
				if err != nil {
					return err
				}
				return tar_items(tar.NewReader(xz_reader), src_url, yield)
			case strings.HasSuffix(member_name, ".zst"):
				zstd_reader, err := zstd.NewReader(member_reader)
				if err != nil {
					return err
				}
				defer zstd_reader.Close()
				return tar_items(tar.NewReader(zstd_reader), src_url, yield)
			default:
				return tar_items(tar.NewReader(member_reader), src_url, yield)
			}
		}

		if _, err := io.Copy(io.Discard, member_reader); err != nil {
			return err
		}
		if member_size%2 != 0 {
			padding := make([]byte, 1)
			if _, err := io.ReadFull(src_stream, padding); err != nil {
				return err
			}
		}
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
			if cleaned_header := strings.Trim(path.Clean(strings.ReplaceAll(header.Name, "\\", "/")), "/"); cleaned_header == "" || cleaned_header == "." {
				continue
			}
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

func apkindex_text(index_data []byte) (string, error) {
	gz_reader, err := gzip.NewReader(bytes.NewReader(index_data))
	if err != nil {
		return "", err
	}
	defer gz_reader.Close()
	tar_reader := tar.NewReader(gz_reader)
	for {
		header, err := tar_reader.Next()
		if errors.Is(err, io.EOF) {
			return "", errors.New("apk index archive has no APKINDEX")
		}
		if err != nil {
			return "", err
		}
		if path.Base(header.Name) != "APKINDEX" {
			continue
		}
		body, err := io.ReadAll(tar_reader)
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
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
	if err != nil {
		if strings.HasPrefix(raw_value, "docker://") {
			return &url.URL{
				Scheme: "docker",
				Opaque: strings.TrimPrefix(raw_value, "docker://"),
			}, raw_value
		}
		return &url.URL{}, raw_value
	}
	if parsed.Scheme == "" {
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

type docker_manifest struct {
	SchemaVersion int `json:"schemaVersion"`
	MediaType     string
	Config        struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
	} `json:"layers"`
	Manifests []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Platform  struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
			Variant      string `json:"variant"`
		} `json:"platform"`
	} `json:"manifests"`
}

func fetch_docker_manifest(registry_base_url string, image_name string, image_ref string, platform_name string) (docker_manifest, error) {
	manifest_url := registry_base_url + "/v2/" + image_name + "/manifests/" + image_ref
	accept_types := strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", ")
	body, _, err := http_get_with_headers(manifest_url, map[string]string{"Accept": accept_types})
	if err != nil {
		return docker_manifest{}, err
	}
	defer body.Close()

	var manifest docker_manifest
	if err := json.NewDecoder(body).Decode(&manifest); err != nil {
		return docker_manifest{}, err
	}
	if len(manifest.Manifests) == 0 {
		return manifest, nil
	}

	selected_digest := manifest.Manifests[0].Digest
	selected_platform := split_platform_name(platform_name)
	for _, item := range manifest.Manifests {
		if item.Platform.OS == selected_platform[0] && item.Platform.Architecture == selected_platform[1] {
			if selected_platform[2] == "" || item.Platform.Variant == selected_platform[2] {
				selected_digest = item.Digest
				break
			}
		}
	}
	return fetch_docker_manifest(registry_base_url, image_name, selected_digest, platform_name)
}

func apply_docker_layer(root_dir string, registry_base_url string, image_name string, layer struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
}) error {
	layer_url := registry_base_url + "/v2/" + image_name + "/blobs/" + layer.Digest
	body, _, err := http_get_with_headers(layer_url, nil)
	if err != nil {
		return err
	}
	defer body.Close()

	layer_reader := io.Reader(body)
	if strings.Contains(layer.MediaType, "gzip") || strings.Contains(layer.MediaType, "zstd") || strings.Contains(layer.MediaType, "tar+gzip") {
		gz_reader, err := gzip.NewReader(body)
		if err != nil {
			return err
		}
		defer gz_reader.Close()
		layer_reader = gz_reader
	}

	tar_reader := tar.NewReader(layer_reader)
	for {
		header, err := tar_reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rel_path := normalize_rel_path(header.Name)
		if rel_path == "" {
			continue
		}
		base_name := path.Base(rel_path)
		if strings.HasPrefix(base_name, ".wh.") {
			whiteout_target := path.Join(path.Dir(rel_path), strings.TrimPrefix(base_name, ".wh."))
			if whiteout_target == "." {
				continue
			}
			if err := os.RemoveAll(filepath.Join(root_dir, filepath.FromSlash(whiteout_target))); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		dst_path := filepath.Join(root_dir, filepath.FromSlash(rel_path))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst_path, 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if runtime.GOOS == "windows" {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst_path), 0o755); err != nil {
				return err
			}
			_ = os.Remove(dst_path)
			if err := os.Symlink(header.Linkname, dst_path); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(dst_path), 0o755); err != nil {
				return err
			}
			out_file, err := os.Create(dst_path)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out_file, tar_reader); err != nil {
				out_file.Close()
				return err
			}
			if err := out_file.Close(); err != nil {
				return err
			}
		}
	}
}

func http_get_with_headers(src_url string, headers map[string]string) (io.ReadCloser, bool, error) {
	req, err := http.NewRequest(http.MethodGet, src_url, nil)
	if err != nil {
		return nil, false, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, insecure_retry, err := http_do(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		token, token_err := docker_bearer_token(resp.Header.Get("Www-Authenticate"))
		if token_err == nil && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
			resp.Body.Close()
			resp, insecure_retry, err = http_do(req)
		}
	}
	if err != nil {
		return nil, false, err
	}
	if insecure_retry {
		fmt.Fprintln(os.Stderr, tls_retry_message)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, false, fmt.Errorf("request failed: %s", resp.Status)
	}
	return resp.Body, false, nil
}

func http_do(req *http.Request) (*http.Response, bool, error) {
	resp, err := http.DefaultClient.Do(req)
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
	resp, retry_err := insecure_client.Do(req)
	if retry_err != nil {
		return nil, false, retry_err
	}
	return resp, true, nil
}

func docker_bearer_token(auth_header string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(auth_header), "bearer ") {
		return "", errors.New("unsupported auth challenge")
	}
	params := map[string]string{}
	for _, item := range strings.Split(strings.TrimSpace(auth_header[7:]), ",") {
		pair := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(pair) != 2 {
			continue
		}
		params[pair[0]] = strings.Trim(pair[1], `"`)
	}
	if params["realm"] == "" {
		return "", errors.New("missing bearer realm")
	}
	query := url.Values{}
	if params["service"] != "" {
		query.Set("service", params["service"])
	}
	if params["scope"] != "" {
		query.Set("scope", params["scope"])
	}
	token_url := params["realm"]
	if encoded := query.Encode(); encoded != "" {
		token_url += "?" + encoded
	}
	body, _, err := http_get_with_headers(token_url, nil)
	if err != nil {
		return "", err
	}
	defer body.Close()
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		return "", err
	}
	return payload.Token, nil
}

func split_platform_name(platform_name string) [3]string {
	selected := [3]string{"", "", ""}
	parts := strings.Split(platform_name, "/")
	for i := 0; i < len(parts) && i < 3; i++ {
		selected[i] = parts[i]
	}
	return selected
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
	os.Args = append([]string{os.Args[0]}, normalize_bool_flag_args(os.Args[1:], map[string]bool{
		"-symlinks":      true,
		"-download-only": true,
		"-do":            true,
		"-notmp":         true,
		"-no-tempfile":   true,
		"-quiet":         true,
		"-q":             true,
		"-overwrite":     true,
	})...)

	src := hx_src{}
	dst := hx_dst{}
	tui := &hx_tui{mode: "ansi"}
	keep_symlinks := true
	quiet := false
	overwrite := true

	flag.IntVar(&dst.skip_path_prefix, "strip", 0, "strip N leading path components")
	flag.IntVar(&dst.skip_path_prefix, "skip", 0, "strip N leading path components")
	flag.StringVar(&src.platform, "platform", runtime.GOOS+"/"+runtime.GOARCH, "target platform")
	flag.StringVar(&src.platform, "plat", runtime.GOOS+"/"+runtime.GOARCH, "target platform")
	flag.StringVar(&src.registry_base_url, "registry", "", "registry override")
	flag.StringVar(&src.registry_base_url, "reg", "", "registry override")
	flag.StringVar(&src.target, "target", "", "target override")
	flag.StringVar(&src.target, "t", "", "target override")
	flag.StringVar(&dst.include_exclude, "incexc", ":+", "include/exclude rules")
	flag.Var(bool_flag{&keep_symlinks}, "symlinks", "keep symlinks when supported")
	flag.Var(bool_flag{&src.download_only}, "download-only", "download without extraction")
	flag.Var(bool_flag{&src.download_only}, "do", "download without extraction")
	flag.Var(bool_flag{&src.force_no_tmp}, "notmp", "avoid temp-file fallback")
	flag.Var(bool_flag{&src.force_no_tmp}, "no-tempfile", "avoid temp-file fallback")
	flag.Var(bool_flag{&quiet}, "quiet", "plain output")
	flag.Var(bool_flag{&quiet}, "q", "plain output")
	flag.Var(bool_flag{&overwrite}, "overwrite", "overwrite files")
	flag.Parse()

	if quiet {
		tui.mode = "plain"
	}
	dst.skip_symlinks = !keep_symlinks
	dst.overwrite = overwrite
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
