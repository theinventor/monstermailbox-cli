// Package updater handles "is there a newer mmb available, and how do I
// install it" — without the noisy stderr banners human-targeted CLIs
// use, because mmb's audience is agents that parse stdout.
//
// The shape:
//
//   - CheckForUpdate(currentVersion) consults a 24h-cached snapshot of
//     the latest GitHub release. Cache lives at $config_dir/update-check.json
//     so it round-trips between processes. Devs running unbuilt code
//     ("dev" version sentinel) get a Skipped result — we don't pester
//     people developing the tool.
//
//   - LatestRelease() does the live GitHub call (no cache). Used by
//     `mmb update --check --no-cache` and `mmb update` (install path
//     never trusts cache for the actual binary).
//
//   - Install() downloads the right asset for GOOS/GOARCH, verifies a
//     companion sha256, and atomically replaces the running binary
//     (write to mmb.new + os.Rename). xattr quarantine bit is cleared
//     on macOS so the next launch isn't blocked by Gatekeeper.
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// LatestReleaseURL is the GitHub API endpoint for the most recent
	// non-prerelease release of theinventor/monstermailbox-cli. Override
	// via the MMB_RELEASES_URL env var for tests.
	defaultReleasesURL = "https://api.github.com/repos/theinventor/monstermailbox-cli/releases/latest"

	// CacheTTL is how long a "checked, here's what's latest" result
	// stays valid before we re-hit GitHub. 24h is the sweet spot — long
	// enough that 99% of command invocations are cache-hits, short
	// enough that an agent running daily learns about new releases the
	// next morning.
	CacheTTL = 24 * time.Hour

	// DevVersion is the sentinel "this binary was `go build`'d locally,
	// don't try to update it." cmd.Version defaults to this string.
	DevVersion = "dev"
)

// Release is the slice of the GitHub Releases API JSON we care about.
type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// UpdateInfo is the structured answer to "should the user update?"
// Available + Current=Latest means up-to-date; Available + Current<Latest
// means an update is available; Skipped means we deliberately didn't
// check (dev build, no cache, etc.).
type UpdateInfo struct {
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`

	Current   string `json:"current,omitempty"`
	Latest    string `json:"latest,omitempty"`
	URL       string `json:"url,omitempty"`
	Available bool   `json:"available,omitempty"`
	CheckedAt string `json:"checked_at,omitempty"`
}

// cacheEntry is the on-disk shape of update-check.json.
type cacheEntry struct {
	CheckedAt string  `json:"checked_at"`
	Release   Release `json:"release"`
}

// CachePath returns the cache file location. Honors $MMB_CONFIG (test
// override) and $XDG_CONFIG_HOME, mirroring the auth config layout.
func CachePath() string {
	// Live alongside the auth config file.
	if explicit := os.Getenv("MMB_UPDATE_CACHE"); explicit != "" {
		return explicit
	}
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, "mmb", "update-check.json")
}

// CheckForUpdate returns availability info, hitting the cache first.
// Errors are non-fatal — they just yield a Skipped result so callers
// can be silent on offline machines.
func CheckForUpdate(currentVersion string) UpdateInfo {
	if currentVersion == "" || currentVersion == DevVersion {
		return UpdateInfo{Skipped: true, Reason: "dev build"}
	}

	if entry, ok := readCache(); ok && time.Since(parseTime(entry.CheckedAt)) < CacheTTL {
		return buildInfo(currentVersion, entry.Release, entry.CheckedAt)
	}

	rel, err := LatestRelease(nil)
	if err != nil {
		// Don't fail the caller — they were just curious. Surface as
		// Skipped so structured output is still well-formed.
		return UpdateInfo{Skipped: true, Reason: "check failed: " + err.Error()}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = writeCache(cacheEntry{CheckedAt: now, Release: rel})
	return buildInfo(currentVersion, rel, now)
}

// LatestRelease hits GitHub and returns the most recent release. No
// cache. Pass a nil client to use the default 10s-timeout one.
func LatestRelease(httpClient *http.Client) (Release, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	url := os.Getenv("MMB_RELEASES_URL")
	if url == "" {
		url = defaultReleasesURL
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return Release{}, fmt.Errorf("github: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("decode: %w", err)
	}
	return rel, nil
}

// Install downloads the right binary for the current GOOS/GOARCH from
// the given release, verifies the sha256 (must accompany every binary
// asset as <name>.sha256), and atomically replaces the running binary.
//
// destPath is the path of the binary to replace (typically os.Executable()).
// It must be writable by the current process — if mmb was installed
// to /usr/local/bin/ root-owned, this returns a clear "use sudo" error.
func Install(rel Release, destPath string, httpClient *http.Client) error {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Minute}
	}

	binAssetName := fmt.Sprintf("mmb-%s-%s", runtime.GOOS, runtime.GOARCH)
	shaAssetName := binAssetName + ".sha256"

	binAsset, ok := findAsset(rel, binAssetName)
	if !ok {
		return fmt.Errorf("release %s has no asset %q (built for %s/%s)",
			rel.TagName, binAssetName, runtime.GOOS, runtime.GOARCH)
	}
	shaAsset, ok := findAsset(rel, shaAssetName)
	if !ok {
		return fmt.Errorf("release %s has no checksum file %q — refusing install without sha verification",
			rel.TagName, shaAssetName)
	}

	// Verify destPath writability before downloading multiple MB.
	dir := filepath.Dir(destPath)
	if err := writableDir(dir); err != nil {
		return fmt.Errorf("cannot replace %s: %w (try `sudo mmb update` or move the binary to a user-writable location)", destPath, err)
	}

	// Download binary to a tmp file alongside destPath.
	tmpPath := destPath + ".update.tmp"
	if err := downloadTo(httpClient, binAsset.BrowserDownloadURL, tmpPath); err != nil {
		return fmt.Errorf("download binary: %w", err)
	}
	defer os.Remove(tmpPath) // best-effort cleanup if we error before rename

	// Download checksum file (small) into memory.
	expectedHash, err := downloadHash(httpClient, shaAsset.BrowserDownloadURL, binAssetName)
	if err != nil {
		return fmt.Errorf("download checksum: %w", err)
	}

	// Verify.
	gotHash, err := sha256File(tmpPath)
	if err != nil {
		return fmt.Errorf("hash tmp: %w", err)
	}
	if gotHash != expectedHash {
		return fmt.Errorf("sha256 mismatch (got %s, want %s) — refusing install", gotHash, expectedHash)
	}

	// Make executable and clear macOS quarantine attr so Gatekeeper
	// doesn't block the first launch of the replacement binary.
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	if runtime.GOOS == "darwin" {
		// Best-effort; not all binaries get the quarantine bit. The
		// `xattr` tool is in /usr/bin/xattr by default on macOS.
		_ = exec.Command("xattr", "-d", "com.apple.quarantine", tmpPath).Run()
	}

	// Atomic replace. On the same filesystem, os.Rename is atomic.
	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmpPath, destPath, err)
	}
	return nil
}

func findAsset(rel Release, name string) (Asset, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

func downloadTo(client *http.Client, url, dest string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

// downloadHash fetches a checksum file and extracts the hash for the
// expected filename. Supports both `<hash>` and `<hash>  <filename>` forms.
func downloadHash(client *http.Client, url, expectedFile string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		// Two common formats:
		//   "<hex>"                  (single-file file, just the digest)
		//   "<hex>  filename"        (sha256sum -b style)
		if len(fields) == 1 {
			return strings.ToLower(fields[0]), nil
		}
		// sha256sum prefixes filenames with '*'; strip if present.
		fname := strings.TrimPrefix(fields[1], "*")
		if fname == expectedFile || fname == "" {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksum file did not contain hash for %q", expectedFile)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writableDir(dir string) error {
	probe, err := os.CreateTemp(dir, ".mmb-update-probe-*")
	if err != nil {
		return err
	}
	probe.Close()
	return os.Remove(probe.Name())
}

func readCache() (cacheEntry, bool) {
	raw, err := os.ReadFile(CachePath())
	if err != nil {
		return cacheEntry{}, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return cacheEntry{}, false
	}
	if entry.CheckedAt == "" {
		return cacheEntry{}, false
	}
	return entry, true
}

func writeCache(entry cacheEntry) error {
	path := CachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func buildInfo(current string, rel Release, checkedAt string) UpdateInfo {
	latest := rel.TagName
	available := versionLess(current, latest)
	return UpdateInfo{
		Current:   current,
		Latest:    latest,
		URL:       rel.HTMLURL,
		Available: available,
		CheckedAt: checkedAt,
	}
}

// versionLess does a forgiving lexicographic-ish comparison on tags
// like "v0.3.0", "v0.10.0", "0.4.0". Strips a leading 'v' and splits
// on '.' so v0.10.0 > v0.3.0 (which lex-compare gets wrong).
func versionLess(a, b string) bool {
	if a == b {
		return false
	}
	pa := splitVersion(a)
	pb := splitVersion(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var aa, bb int
		if i < len(pa) {
			aa = pa[i]
		}
		if i < len(pb) {
			bb = pb[i]
		}
		if aa != bb {
			return aa < bb
		}
	}
	return false
}

func splitVersion(v string) []int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out = append(out, n)
	}
	return out
}
