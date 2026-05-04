package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.3.0", "v0.4.0", true},
		{"v0.4.0", "v0.3.0", false},
		{"v0.3.0", "v0.3.0", false},
		{"v0.10.0", "v0.3.0", false}, // 10 > 3 — the case lex-compare gets wrong
		{"v0.3.0", "v0.10.0", true},
		{"v0.3.0", "v0.3.1", true},
		{"v1.0.0", "v0.99.99", false},
		{"0.3.0", "v0.4.0", true}, // leading-v insensitive
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestCheckForUpdate_DevSkipped(t *testing.T) {
	got := CheckForUpdate("dev")
	if !got.Skipped {
		t.Errorf("dev should be skipped, got %+v", got)
	}
	got = CheckForUpdate("")
	if !got.Skipped {
		t.Errorf("empty version should be skipped, got %+v", got)
	}
}

func TestCheckForUpdate_HitsAPIAndCaches(t *testing.T) {
	t.Setenv("MMB_UPDATE_CACHE", filepath.Join(t.TempDir(), "cache.json"))

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(Release{
			TagName: "v0.5.0",
			HTMLURL: "https://example/release",
			Assets:  []Asset{{Name: "mmb-darwin-arm64", BrowserDownloadURL: "https://example/bin"}},
		})
	}))
	defer srv.Close()
	t.Setenv("MMB_RELEASES_URL", srv.URL)

	got := CheckForUpdate("v0.4.0")
	if got.Skipped {
		t.Fatalf("unexpected skip: %+v", got)
	}
	if got.Latest != "v0.5.0" || !got.Available {
		t.Errorf("first call: latest=%q available=%v", got.Latest, got.Available)
	}
	if calls != 1 {
		t.Errorf("first call should hit API once, got %d", calls)
	}

	// Second call within TTL hits cache, not API.
	got2 := CheckForUpdate("v0.4.0")
	if got2.Latest != "v0.5.0" {
		t.Errorf("cache miss: got %+v", got2)
	}
	if calls != 1 {
		t.Errorf("second call should hit cache, got %d API calls", calls)
	}
}

func TestCheckForUpdate_UpToDate(t *testing.T) {
	t.Setenv("MMB_UPDATE_CACHE", filepath.Join(t.TempDir(), "cache.json"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Release{TagName: "v1.0.0"})
	}))
	defer srv.Close()
	t.Setenv("MMB_RELEASES_URL", srv.URL)

	got := CheckForUpdate("v1.0.0")
	if got.Available {
		t.Errorf("same version must not be marked available: %+v", got)
	}
	if got.Latest != "v1.0.0" || got.Current != "v1.0.0" {
		t.Errorf("metadata wrong: %+v", got)
	}
}

func TestCheckForUpdate_APIFailureIsSkipped(t *testing.T) {
	t.Setenv("MMB_UPDATE_CACHE", filepath.Join(t.TempDir(), "cache.json"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("MMB_RELEASES_URL", srv.URL)

	got := CheckForUpdate("v0.1.0")
	if !got.Skipped {
		t.Errorf("API failure should yield Skipped, got %+v", got)
	}
}

// goreleaserArchive returns (archiveBytes, archiveSHA256) for a tar.gz
// (or .zip on Windows) containing `mmb` (or `mmb.exe`) with the given
// content. Mirrors what goreleaser actually uploads so tests exercise
// the real wire format.
func goreleaserArchive(t *testing.T, binContent []byte) ([]byte, string) {
	t.Helper()
	binName := "mmb"
	if runtime.GOOS == "windows" {
		binName = "mmb.exe"
	}

	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		fw, err := zw.Create(binName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(binContent); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		hdr := &tar.Header{Name: binName, Mode: 0o755, Size: int64(len(binContent)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(binContent); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes(), sha256Hex(buf.Bytes())
}

func TestGoreleaserArchiveNamePinsTheNamingConvention(t *testing.T) {
	// v0.2.0 bug — the updater looked for `mmb-darwin-arm64` but
	// goreleaser uploads `mmb_0.2.0_darwin_arm64.tar.gz`. Pin the
	// pattern so a future regression can't slip through review.
	cases := []struct {
		tag, goos, goarch, want string
	}{
		{"v0.2.0", "darwin", "arm64", "mmb_0.2.0_darwin_arm64.tar.gz"},
		{"v0.2.0", "darwin", "amd64", "mmb_0.2.0_darwin_amd64.tar.gz"},
		{"v0.2.0", "linux", "amd64", "mmb_0.2.0_linux_amd64.tar.gz"},
		{"v0.2.0", "linux", "arm64", "mmb_0.2.0_linux_arm64.tar.gz"},
		{"v0.2.0", "windows", "amd64", "mmb_0.2.0_windows_amd64.zip"},
		{"v1.10.3", "darwin", "arm64", "mmb_1.10.3_darwin_arm64.tar.gz"},
		// Both v-prefixed and bare versions should produce the same
		// asset name (goreleaser strips the v).
		{"0.2.0", "linux", "amd64", "mmb_0.2.0_linux_amd64.tar.gz"},
	}
	for _, c := range cases {
		got := goreleaserArchiveName(c.tag, c.goos, c.goarch)
		if got != c.want {
			t.Errorf("goreleaserArchiveName(%q,%q,%q) = %q, want %q",
				c.tag, c.goos, c.goarch, got, c.want)
		}
	}
}

func TestInstall_VerifiesChecksumAndReplaces(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mmb")
	if err := os.WriteFile(dest, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	newBytes := []byte("NEW BINARY CONTENT")
	archiveBytes, archiveHash := goreleaserArchive(t, newBytes)
	archiveName := goreleaserArchiveName("v0.5.0", runtime.GOOS, runtime.GOARCH)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + archiveName:
			_, _ = w.Write(archiveBytes)
		case "/checksums.txt":
			// goreleaser format: `<hash>  <filename>\n` per line, one
			// line per artifact in the release.
			fmt.Fprintf(w, "%s  %s\n", archiveHash, archiveName)
			fmt.Fprintf(w, "deadbeef  some_other_file.tar.gz\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	rel := Release{
		TagName: "v0.5.0",
		Assets: []Asset{
			{Name: archiveName, BrowserDownloadURL: srv.URL + "/" + archiveName},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}
	if err := Install(rel, dest, srv.Client()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, _ := os.ReadFile(dest)
	if string(got) != string(newBytes) {
		t.Errorf("binary not replaced (extracted contents): %q", got)
	}
	st, _ := os.Stat(dest)
	// Windows doesn't carry a POSIX executable bit — every file is
	// "executable" if its name ends in .exe. Skip the bit check there.
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		t.Errorf("replaced binary not executable: %o", st.Mode().Perm())
	}
}

func TestInstall_FailsWhenChecksumsTxtMissing(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mmb")
	if err := os.WriteFile(dest, []byte("ORIGINAL"), 0o755); err != nil {
		t.Fatal(err)
	}
	archiveName := goreleaserArchiveName("v0.5.0", runtime.GOOS, runtime.GOARCH)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// Release with the archive but no checksums.txt — must refuse.
	rel := Release{
		TagName: "v0.5.0",
		Assets: []Asset{
			{Name: archiveName, BrowserDownloadURL: srv.URL + "/x"},
		},
	}
	err := Install(rel, dest, srv.Client())
	if err == nil || !strings.Contains(err.Error(), "checksums.txt") {
		t.Errorf("expected checksums.txt missing error, got %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "ORIGINAL" {
		t.Errorf("dest should be untouched on a refusal; got %q", got)
	}
}

func TestInstall_FailsWhenArchiveMissingForPlatform(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mmb")
	_ = os.WriteFile(dest, []byte("ORIGINAL"), 0o755)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	rel := Release{
		TagName: "v0.5.0",
		Assets: []Asset{
			// Old (broken) naming — should NOT be matched.
			{Name: "mmb-" + runtime.GOOS + "-" + runtime.GOARCH, BrowserDownloadURL: srv.URL + "/x"},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/c"},
		},
	}
	err := Install(rel, dest, srv.Client())
	if err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Errorf("expected 'no asset' error when only the legacy raw-binary name is present, got %v", err)
	}
}

func TestInstall_RejectsCorruptDownload(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mmb")
	if err := os.WriteFile(dest, []byte("ORIGINAL"), 0o755); err != nil {
		t.Fatal(err)
	}
	archiveBytes, _ := goreleaserArchive(t, []byte("CORRUPT"))
	archiveName := goreleaserArchiveName("v0.5.0", runtime.GOOS, runtime.GOARCH)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/archive":
			_, _ = w.Write(archiveBytes)
		case "/checksums.txt":
			// Claims a different content. Verify must fail.
			fmt.Fprintf(w, "%s  %s\n", sha256Hex([]byte("DIFFERENT")), archiveName)
		}
	}))
	defer srv.Close()

	rel := Release{
		TagName: "v0.5.0",
		Assets: []Asset{
			{Name: archiveName, BrowserDownloadURL: srv.URL + "/archive"},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}
	err := Install(rel, dest, srv.Client())
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("expected sha256 mismatch error, got: %v", err)
	}
	// Original must be untouched after a failed install.
	got, _ := os.ReadFile(dest)
	if string(got) != "ORIGINAL" {
		t.Errorf("destination corrupted on failed install: %q", got)
	}
}

func TestInstall_FailsWhenArchiveDoesNotContainBinary(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mmb")
	_ = os.WriteFile(dest, []byte("ORIGINAL"), 0o755)

	// Build an archive with the WRONG entry name so extraction fails.
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		fw, _ := zw.Create("not-mmb.exe")
		_, _ = fw.Write([]byte("x"))
		_ = zw.Close()
	} else {
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(&tar.Header{Name: "not-mmb", Mode: 0o755, Size: 1, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("x"))
		_ = tw.Close()
		_ = gz.Close()
	}
	bogusArchive := buf.Bytes()
	archiveName := goreleaserArchiveName("v0.5.0", runtime.GOOS, runtime.GOARCH)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + archiveName:
			_, _ = w.Write(bogusArchive)
		case "/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n", sha256Hex(bogusArchive), archiveName)
		}
	}))
	defer srv.Close()

	rel := Release{
		TagName: "v0.5.0",
		Assets: []Asset{
			{Name: archiveName, BrowserDownloadURL: srv.URL + "/" + archiveName},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}
	err := Install(rel, dest, srv.Client())
	if err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Errorf("expected extract error when archive lacks mmb binary, got %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "ORIGINAL" {
		t.Errorf("dest must be untouched on extract failure; got %q", got)
	}
}

func sha256Hex(b []byte) string {
	h := sha256.New()
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func TestCacheTTL_StaleCacheIsRefreshed(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	t.Setenv("MMB_UPDATE_CACHE", cachePath)

	// Seed cache with an old timestamp.
	old := cacheEntry{
		CheckedAt: time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339),
		Release:   Release{TagName: "v0.1.0"},
	}
	raw, _ := json.Marshal(old)
	_ = os.MkdirAll(filepath.Dir(cachePath), 0o700)
	_ = os.WriteFile(cachePath, raw, 0o600)

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(Release{TagName: "v0.99.0"})
	}))
	defer srv.Close()
	t.Setenv("MMB_RELEASES_URL", srv.URL)

	got := CheckForUpdate("v0.5.0")
	if got.Latest != "v0.99.0" {
		t.Errorf("stale cache not refreshed: %+v", got)
	}
	if calls != 1 {
		t.Errorf("expected one API call to refresh stale cache, got %d", calls)
	}
}
