package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mholt/archives"
)

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	skip       := flag.Int("skip", 0, "strip N leading path components from each archive entry")
	symlinks   := flag.Bool("symlinks", false, "extract symbolic links (skipped by default for safety)")
	doProgress := flag.Bool("progress", false, "rich ANSI progress: bar, speed, ETA, current file (for terminals)")
	noTempFile := flag.Bool("no-tempfile", false, "buffer non-Range ZIP in memory instead of a temp file")

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: hx [flags] <url> [dest]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "  url   HTTP/HTTPS URL of the archive to download and extract")
		fmt.Fprintln(os.Stderr, "  dest  destination folder (default: current directory); created if absent")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "flags:")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		flag.Usage()
		os.Exit(1)
	}

	rawURL := args[0]
	dest := "."
	if len(args) >= 2 {
		dest = args[1]
	}

	absDest, err := filepath.Abs(dest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot resolve dest path: %v\n", err)
		os.Exit(1)
	}
	dest = absDest

	if *skip < 0 {
		fmt.Fprintln(os.Stderr, "-skip must be a non-negative integer")
		os.Exit(1)
	}

	doneFile := filepath.Join(dest, doneFileName(rawURL, *skip, *symlinks))
	if _, err := os.Stat(doneFile); err == nil {
		fmt.Println("already extracted, skipping")
		return
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create destination: %v\n", err)
		os.Exit(1)
	}

	pr := newPrinter(*doProgress)
	_, err = run(rawURL, dest, *skip, *symlinks, !*noTempFile, pr)
	pr.commit()
	if err != nil {
		fmt.Fprintf(os.Stderr, "extraction failed: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(doneFile, nil, 0o666); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create done file: %v\n", err)
		os.Exit(1)
	}

	pr.done()
}

// ── Printer ───────────────────────────────────────────────────────────────────

type printer struct {
	ansi       bool
	start      time.Time
	inplace    bool      // last write used \r (not newline-terminated)
	lastRender time.Time // throttle for in-place refreshes

	// live state used by render()
	dlBytes        int64
	dlTotal        int64 // -1 = unknown
	fileCount      int
	totalExtracted int64 // sum of uncompressed sizes
	lastFile       string
	lastSize       int64
}

const renderInterval = 100 * time.Millisecond

func newPrinter(ansi bool) *printer {
	return &printer{ansi: ansi, start: time.Now(), dlTotal: -1, lastSize: -1}
}

// commit ends any pending in-place line with a newline.
func (p *printer) commit() {
	if p.inplace {
		fmt.Println()
		p.inplace = false
	}
}

func (p *printer) info(msg string) {
	p.commit()
	if p.ansi {
		// Dim the "key:" label prefix so the value stands out.
		if i := strings.IndexByte(msg, ':'); i > 0 && i < 12 {
			fmt.Printf("\033[2m%s\033[0m%s\n", msg[:i+1], msg[i+1:])
			return
		}
	}
	fmt.Println(msg)
}

func (p *printer) warn(msg string) {
	p.commit()
	if p.ansi {
		fmt.Printf("\033[1;33m[warn]\033[0;33m %s\033[0m\n", msg)
	} else {
		fmt.Println("[warn] " + msg)
	}
}

func (p *printer) done() {
	sizeInfo := ""
	if p.totalExtracted > 0 {
		sizeInfo = "  " + fmtBytes(p.totalExtracted)
	}
	elapsed := time.Since(p.start).Seconds()
	if p.ansi {
		fmt.Printf("\033[1;32mdone\033[0m  \033[1m%d files%s\033[0m  \033[2m(%.1fs)\033[0m\n",
			p.fileCount, sizeInfo, elapsed)
	} else {
		fmt.Printf("done  %d files%s  (%.1fs)\n", p.fileCount, sizeInfo, elapsed)
	}
}

// onDL is called as bytes are downloaded; total = -1 when unknown.
func (p *printer) onDL(downloaded, total int64) {
	p.dlBytes = downloaded
	p.dlTotal = total
	p.render()
}

// onFile is called for each file actually extracted (not dirs or skipped symlinks).
func (p *printer) onFile(name string, size int64) {
	p.fileCount++
	if size >= 0 {
		p.totalExtracted += size
	}
	p.lastFile = name
	p.lastSize = size
	// plain mode: no per-file output; summary is printed at the end by main.
	if p.ansi {
		p.render()
	}
}

// render repaints the single in-place ANSI line (throttled).
func (p *printer) render() {
	if !p.ansi {
		return
	}
	if time.Since(p.lastRender) < renderInterval {
		return
	}
	p.lastRender = time.Now()

	elapsed := time.Since(p.start).Seconds()
	var rate float64
	if elapsed > 0 && p.dlBytes > 0 {
		rate = float64(p.dlBytes) / elapsed
	}

	var line string
	if p.fileCount > 0 {
		// Extraction phase: current file, running totals, download info
		sizeStr := ""
		if p.lastSize >= 0 {
			sizeStr = "  \033[2m" + fmtBytes(p.lastSize) + "\033[0m"
		}
		progress := fmt.Sprintf("  \033[32mfile %d  %s extracted\033[0m",
			p.fileCount, fmtBytes(p.totalExtracted))
		dlInfo := ""
		if p.dlTotal > 0 {
			pct := int(100 * p.dlBytes / p.dlTotal)
			dlInfo = fmt.Sprintf("  \033[2m[%s %d%% @ %s]\033[0m",
				progressBar(pct, 14), pct, fmtRate(rate))
		} else if p.dlBytes > 0 {
			dlInfo = fmt.Sprintf("  \033[2m[%s @ %s]\033[0m", fmtBytes(p.dlBytes), fmtRate(rate))
		}
		line = fmt.Sprintf("\033[1mExtracting\033[0m  \033[36m%-44s\033[0m%s%s%s",
			truncate(p.lastFile, 44), sizeStr, progress, dlInfo)
	} else {
		// Download-only phase (ZIP temp-file download before extraction begins)
		if p.dlTotal > 0 {
			pct := int(100 * p.dlBytes / p.dlTotal)
			bar := progressBar(pct, 28)
			eta := ""
			if rate > 0 && p.dlBytes < p.dlTotal {
				eta = "  \033[2mETA " + fmtDuration(float64(p.dlTotal-p.dlBytes)/rate) + "\033[0m"
			}
			line = fmt.Sprintf("\033[1;33mDownloading\033[0m  %s  \033[1m%3d%%\033[0m  %s / %s  \033[2m%s\033[0m%s",
				bar, pct, fmtBytes(p.dlBytes), fmtBytes(p.dlTotal), fmtRate(rate), eta)
		} else {
			line = fmt.Sprintf("\033[1;33mDownloading\033[0m  %s  \033[2m%s\033[0m",
				fmtBytes(p.dlBytes), fmtRate(rate))
		}
	}

	fmt.Print("\033[2K\r" + line)
	p.inplace = true
}

// ── run ───────────────────────────────────────────────────────────────────────

func run(rawURL, dest string, skip int, symlinks, useTempFile bool, pr *printer) (int, error) {
	ctx := context.Background()
	client := &http.Client{}

	resp, err := client.Get(rawURL)
	if err != nil {
		return 0, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("server returned %s", resp.Status)
	}

	// Print url: before any body reads so it always appears first in ANSI mode.
	pr.info(fmt.Sprintf("url:    %s", rawURL))

	// Wrap body in a byte counter so download bytes are tracked as data flows.
	var dlBytes int64
	tracked := &countReader{
		r: resp.Body,
		onRead: func(n int64) {
			dlBytes += n
			pr.onDL(dlBytes, resp.ContentLength)
		},
	}

	br := bufio.NewReaderSize(tracked, 1<<16)
	hint := filepath.Base(strings.SplitN(rawURL, "?", 2)[0])

	format, reader, err := archives.Identify(ctx, hint, br)
	if err != nil {
		return 0, fmt.Errorf("identify format: %w", err)
	}

	ex, ok := format.(archives.Extractor)
	if !ok {
		return 0, fmt.Errorf("format %T does not support extraction", format)
	}

	// Print format: after detection (commits any in-place download line first).
	fmtExt := strings.Trim(format.Extension(), ".")
	sizeStr := ""
	if resp.ContentLength > 0 {
		sizeStr = "  " + fmtBytes(resp.ContentLength)
	}
	pr.info(fmt.Sprintf("format: %s%s", fmtExt, sizeStr))

	handler := func(ctx context.Context, f archives.FileInfo) error {
		return handleEntry(f, dest, skip, symlinks, pr)
	}

	if _, isZip := format.(archives.Zip); isZip {
		err = extractZip(ctx, rawURL, resp, reader, client, useTempFile, pr, handler)
	} else {
		err = ex.Extract(ctx, reader, handler)
	}
	return pr.fileCount, err
}

// ── countReader ───────────────────────────────────────────────────────────────

type countReader struct {
	r      io.Reader
	onRead func(n int64)
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.onRead(int64(n))
	}
	return n, err
}

// ── extractZip ────────────────────────────────────────────────────────────────

func extractZip(
	ctx context.Context,
	rawURL string,
	resp *http.Response,
	fallback io.Reader,
	client *http.Client,
	useTempFile bool,
	pr *printer,
	handler archives.FileHandler,
) error {
	ex := archives.Zip{}

	// Prefer HTTP Range requests: only the central directory + individual files are
	// fetched, so peak memory stays well below the archive size.
	if resp.Header.Get("Accept-Ranges") == "bytes" && resp.ContentLength > 0 {
		resp.Body.Close()
		// Use the final URL after redirects to skip the redirect hop on every ReadAt.
		finalURL := resp.Request.URL.String()
		rr := &httpRangeReader{ctx: ctx, url: finalURL, size: resp.ContentLength, client: client, pr: pr}
		return ex.Extract(ctx, rr, handler)
	}

	// Server does not support HTTP Range - must download the full ZIP first.
	reason := "no Accept-Ranges: bytes"
	if resp.ContentLength <= 0 {
		reason += ", no Content-Length"
	}

	if useTempFile {
		tmp, err := os.CreateTemp("", "hx-*.zip")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		// Close then remove; on Windows an open handle blocks Remove.
		defer func() { tmp.Close(); os.Remove(tmp.Name()) }()

		pr.warn(fmt.Sprintf(
			"server does not support HTTP Range (%s); downloading to temp file %s",
			reason, tmp.Name()))

		if _, err := io.Copy(tmp, fallback); err != nil {
			return fmt.Errorf("download to temp file: %w", err)
		}
		pr.commit() // end the download progress line before extraction begins

		// Rewind so archives.Zip can read from the beginning.
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek temp file: %w", err)
		}
		return ex.Extract(ctx, tmp, handler)
	}

	// -no-tempfile: buffer the whole archive in memory (original behaviour).
	pr.warn(fmt.Sprintf(
		"server does not support HTTP Range (%s); buffering archive in memory (-no-tempfile set)",
		reason))
	data, err := io.ReadAll(fallback)
	if err != nil {
		return fmt.Errorf("buffer zip: %w", err)
	}
	return ex.Extract(ctx, bytes.NewReader(data), handler)
}

// ── handleEntry ───────────────────────────────────────────────────────────────

func handleEntry(f archives.FileInfo, dest string, skip int, allowSymlinks bool, pr *printer) error {
	if f.IsDir() {
		return nil
	}
	if f.LinkTarget != "" {
		if !allowSymlinks {
			return nil
		}
		pr.onFile(f.NameInArchive, -1)
		return writeSymlink(f, dest, skip)
	}
	pr.onFile(f.NameInArchive, f.Size())
	return writeRegularFile(f, dest, skip)
}

// ── write helpers ─────────────────────────────────────────────────────────────

func writeRegularFile(f archives.FileInfo, dest string, skip int) error {
	path, err := outPath(dest, f.NameInArchive, skip)
	if err != nil || path == "" {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", f.NameInArchive, err)
	}
	defer rc.Close()
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeSymlink(f archives.FileInfo, dest string, skip int) error {
	path, err := outPath(dest, f.NameInArchive, skip)
	if err != nil || path == "" {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	_ = os.Remove(path)
	if err := os.Symlink(f.LinkTarget, path); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", path, f.LinkTarget, err)
	}
	return nil
}

// ── httpRangeReader ───────────────────────────────────────────────────────────
// Implements io.Reader, io.ReaderAt, and io.Seeker using HTTP Range requests.
// Each ReadAt call opens its own connection so concurrent access is safe.

type httpRangeReader struct {
	ctx     context.Context
	url     string
	size    int64
	client  *http.Client
	pr      *printer
	fetched int64 // total bytes fetched across all ReadAt calls
	pos     int64
}

func (r *httpRangeReader) Size() int64 { return r.size }

func (r *httpRangeReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, fmt.Errorf("unknown whence: %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("seek to negative offset")
	}
	r.pos = abs
	return abs, nil
}

func (r *httpRangeReader) Read(p []byte) (int, error) {
	n, err := r.ReadAt(p, r.pos)
	r.pos += int64(n)
	return n, err
}

func (r *httpRangeReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off >= r.size {
		return 0, io.EOF
	}
	end := off + int64(len(p)) - 1
	clampedToEOF := false
	if end >= r.size {
		end = r.size - 1
		clampedToEOF = true
	}

	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return 0, fmt.Errorf("build range request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("range request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("expected 206 Partial Content, got %s", resp.Status)
	}

	want := int(end-off) + 1
	n, err := io.ReadFull(resp.Body, p[:want])
	if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	if err == nil && clampedToEOF {
		err = io.EOF
	}
	if n > 0 && r.pr != nil {
		r.fetched += int64(n)
		r.pr.onDL(r.fetched, r.size)
	}
	return n, err
}

// ── path helpers ──────────────────────────────────────────────────────────────

func doneFileName(url string, skip int, symlinks bool) string {
	sl := 0
	if symlinks {
		sl = 1
	}
	return fmt.Sprintf("hx-%s-skip%d-sym%dargs.done", sanitizeForFilename(url), skip, sl)
}

func sanitizeForFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := b.String()
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func entryParts(nameInArchive string, skip int) []string {
	parts := strings.Split(filepath.ToSlash(nameInArchive), "/")
	kept := parts[:0]
	for _, p := range parts {
		if p != "" && p != "." {
			kept = append(kept, p)
		}
	}
	parts = kept
	if skip >= len(parts) {
		return nil
	}
	return parts[skip:]
}

func outPath(dest, nameInArchive string, skip int) (string, error) {
	parts := entryParts(nameInArchive, skip)
	if len(parts) == 0 {
		return "", nil
	}
	rel := filepath.Join(parts...)
	if rel == "" || rel == "." {
		return "", nil
	}
	full := filepath.Join(dest, rel)
	cleanDest := filepath.Clean(dest) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(full)+string(os.PathSeparator), cleanDest) {
		return "", fmt.Errorf("path traversal blocked: %s", nameInArchive)
	}
	return full, nil
}

// ── display helpers ───────────────────────────────────────────────────────────

func progressBar(pct, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := width * pct / 100
	return "\033[32m" + strings.Repeat("\u2588", filled) +
		"\033[90m" + strings.Repeat("\u2591", width-filled) + "\033[0m"
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func fmtRate(bytesPerSec float64) string {
	if bytesPerSec <= 0 {
		return "-- B/s"
	}
	return fmtBytes(int64(bytesPerSec)) + "/s"
}

func fmtDuration(secs float64) string {
	s := int(secs)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%ds", s/60, s%60)
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return "..." + string(runes[len(runes)-(max-3):])
}
