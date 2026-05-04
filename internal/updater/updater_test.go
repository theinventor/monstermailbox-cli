package updater

import (
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

func TestInstall_VerifiesChecksumAndReplaces(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mmb")
	if err := os.WriteFile(dest, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	newBytes := []byte("NEW BINARY CONTENT")
	hash := sha256Hex(newBytes)
	binAssetName := fmt.Sprintf("mmb-%s-%s", runtime.GOOS, runtime.GOARCH)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + binAssetName:
			_, _ = w.Write(newBytes)
		case "/" + binAssetName + ".sha256":
			fmt.Fprintf(w, "%s  %s\n", hash, binAssetName)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	rel := Release{
		TagName: "v0.5.0",
		Assets: []Asset{
			{Name: binAssetName, BrowserDownloadURL: srv.URL + "/" + binAssetName},
			{Name: binAssetName + ".sha256", BrowserDownloadURL: srv.URL + "/" + binAssetName + ".sha256"},
		},
	}
	if err := Install(rel, dest, srv.Client()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, _ := os.ReadFile(dest)
	if string(got) != string(newBytes) {
		t.Errorf("binary not replaced: %q", got)
	}
	st, _ := os.Stat(dest)
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("replaced binary not executable: %o", st.Mode().Perm())
	}
}

func TestInstall_RejectsCorruptDownload(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "mmb")
	if err := os.WriteFile(dest, []byte("ORIGINAL"), 0o755); err != nil {
		t.Fatal(err)
	}
	binAssetName := fmt.Sprintf("mmb-%s-%s", runtime.GOOS, runtime.GOARCH)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bin":
			_, _ = w.Write([]byte("CORRUPT"))
		case "/sha":
			// Hash claims a different content. Verify must fail.
			fmt.Fprintf(w, "%s  %s\n", sha256Hex([]byte("DIFFERENT")), binAssetName)
		}
	}))
	defer srv.Close()

	rel := Release{
		TagName: "v0.5.0",
		Assets: []Asset{
			{Name: binAssetName, BrowserDownloadURL: srv.URL + "/bin"},
			{Name: binAssetName + ".sha256", BrowserDownloadURL: srv.URL + "/sha"},
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
