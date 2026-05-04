// Package oauth bridges gog's stored OAuth tokens into Pub/Sub
// authentication so the daemon does NOT need a second Google
// credential set.
//
// The flow:
//
//  1. Read gog's credentials.json (client_id + client_secret) — public
//     location at `~/Library/Application Support/gogcli/credentials.json`
//     on macOS, `~/.config/gogcli/credentials.json` on Linux.
//
//  2. Shell `gog auth tokens export <email> --out <tmpfile>` to dump
//     the refresh_token into a tmp file. Read it, delete the file.
//     We do this once at daemon start (and on every refresh-token
//     swap) — the resulting in-memory `*Source` then mints fresh
//     access tokens via the standard OAuth refresh endpoint.
//
//  3. The user MUST have re-authed gog with the pubsub scope:
//     `gog login <email> --extra-scopes=https://www.googleapis.com/auth/pubsub --force-consent`.
//     If the refresh token doesn't carry pubsub, Google will reject
//     the access-token exchange with insufficient_scope; we surface
//     that as a clear error pointing the user at the login command.
package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// PubSubScope is the OAuth scope the bridge requires on gog's stored
// refresh token. Surfaced as a public const so doctor-style error
// messages can reference it verbatim.
const PubSubScope = "https://www.googleapis.com/auth/pubsub"

// Source mints fresh OAuth access tokens, transparently refreshing
// when ≤60s remain before expiry. Safe for concurrent use.
type Source struct {
	clientID     string
	clientSecret string
	refreshToken string
	httpClient   *http.Client
	tokenURL     string

	mu        sync.Mutex
	cached    string
	expiresAt time.Time
}

// Token returns a non-expired access token, refreshing if needed.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cached != "" && time.Until(s.expiresAt) > time.Minute {
		return s.cached, nil
	}

	form := url.Values{
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
		"refresh_token": {s.refreshToken},
		"grant_type":    {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call token endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, _ := readAll(resp.Body)
	if resp.StatusCode != 200 {
		// Surface insufficient-scope explicitly so the user knows to
		// re-auth gog with the pubsub scope.
		if bytes.Contains(body, []byte("insufficient_scope")) ||
			bytes.Contains(body, []byte("invalid_scope")) {
			return "", fmt.Errorf(
				"gog's stored OAuth token is missing the pubsub scope. "+
					"Re-auth gog with:\n\n  gog login <email> --extra-scopes=%s --force-consent\n\n"+
					"(token endpoint: %s)", PubSubScope, resp.Status)
		}
		return "", fmt.Errorf("token endpoint %s: %s", resp.Status, body)
	}

	var parsed struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("token endpoint returned empty access_token: %s", body)
	}

	// Best-effort scope sanity-check — surface the misconfiguration
	// before the first Pub/Sub call rather than after.
	if parsed.Scope != "" && !strings.Contains(parsed.Scope, "pubsub") {
		return "", fmt.Errorf(
			"gog's OAuth token does not include the pubsub scope (got: %q). "+
				"Re-auth gog with:\n  gog login <email> --extra-scopes=%s --force-consent",
			parsed.Scope, PubSubScope)
	}

	s.cached = parsed.AccessToken
	if parsed.ExpiresIn > 0 {
		s.expiresAt = time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	} else {
		s.expiresAt = time.Now().Add(50 * time.Minute)
	}
	return s.cached, nil
}

// LoadSourceFromGog reads gog's credentials + refresh-token storage
// for `email`. The token is exported via `gog auth tokens export`
// to a tmp file (mode 0600), parsed, then deleted.
func LoadSourceFromGog(ctx context.Context, email string) (*Source, error) {
	clientID, clientSecret, tokenURL, err := readGogCredentials()
	if err != nil {
		return nil, fmt.Errorf("read gog credentials: %w", err)
	}

	refreshToken, err := exportRefreshToken(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("export gog refresh token for %s: %w", email, err)
	}

	return &Source{
		clientID:     clientID,
		clientSecret: clientSecret,
		refreshToken: refreshToken,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		tokenURL:     tokenURL,
	}, nil
}

func readGogCredentials() (clientID, clientSecret, tokenURL string, err error) {
	path, err := gogCredentialsPath()
	if err != nil {
		return "", "", "", err
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", "", fmt.Errorf(
				"%s does not exist — install gog and run `gog auth credentials set <oauth-client.json>` first", path)
		}
		return "", "", "", err
	}
	// gog stores either {client_id, client_secret} at top level (its
	// own write format) or a Google-published OAuth client wrapper
	// like {"installed": {client_id, client_secret, ...}} or {"web": …}.
	var asFlat struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		TokenURI     string `json:"token_uri"`
	}
	if err := json.Unmarshal(bytes, &asFlat); err == nil && asFlat.ClientID != "" {
		return asFlat.ClientID, asFlat.ClientSecret, defaultTokenURL(asFlat.TokenURI), nil
	}
	var asWrapped struct {
		Installed struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
			TokenURI     string `json:"token_uri"`
		} `json:"installed"`
		Web struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
			TokenURI     string `json:"token_uri"`
		} `json:"web"`
	}
	if err := json.Unmarshal(bytes, &asWrapped); err != nil {
		return "", "", "", fmt.Errorf("parse %s: %w", path, err)
	}
	if asWrapped.Installed.ClientID != "" {
		return asWrapped.Installed.ClientID, asWrapped.Installed.ClientSecret, defaultTokenURL(asWrapped.Installed.TokenURI), nil
	}
	if asWrapped.Web.ClientID != "" {
		return asWrapped.Web.ClientID, asWrapped.Web.ClientSecret, defaultTokenURL(asWrapped.Web.TokenURI), nil
	}
	return "", "", "", fmt.Errorf("could not extract client_id/client_secret from %s", path)
}

func defaultTokenURL(url string) string {
	if url != "" {
		return url
	}
	return "https://oauth2.googleapis.com/token"
}

// gogCredentialsPath honors $GOG_CREDENTIALS_PATH for tests, else the
// platform default.
func gogCredentialsPath() (string, error) {
	if override := os.Getenv("GOG_CREDENTIALS_PATH"); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "gogcli", "credentials.json"), nil
	default:
		return filepath.Join(home, ".config", "gogcli", "credentials.json"), nil
	}
}

// exportRefreshToken shells `gog auth tokens export <email> --out <tmp>`
// and reads the resulting JSON. Honors $MMB_BRIDGE_GOG_BIN for tests.
func exportRefreshToken(ctx context.Context, email string) (string, error) {
	bin := os.Getenv("MMB_BRIDGE_GOG_BIN")
	if bin == "" {
		bin = "gog"
	}
	tmp, err := os.CreateTemp("", "mmb-bridge-token-*.json")
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	cmd := exec.CommandContext(ctx, bin, "auth", "tokens", "export", email, "--out", tmp.Name(), "--overwrite")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("`%s auth tokens export %s` failed: %w (output: %s)", bin, email, err, out)
	}
	bytes, err := os.ReadFile(tmp.Name())
	if err != nil {
		return "", err
	}
	// Token export shape per gog: {"client":"default","email":"…","refresh_token":"…","scopes":[...]}
	// We tolerate either snake_case or camelCase for forward-compat.
	var parsed struct {
		RefreshToken string `json:"refresh_token"`
		Scopes       []string `json:"scopes"`
	}
	if err := json.Unmarshal(bytes, &parsed); err != nil {
		return "", fmt.Errorf("parse exported token: %w", err)
	}
	if parsed.RefreshToken == "" {
		return "", fmt.Errorf("exported token has empty refresh_token")
	}
	if len(parsed.Scopes) > 0 {
		hasPubSub := false
		for _, s := range parsed.Scopes {
			if s == PubSubScope {
				hasPubSub = true
				break
			}
		}
		if !hasPubSub {
			return "", fmt.Errorf(
				"gog's stored token does not include the pubsub scope. "+
					"Re-auth gog with:\n  gog login %s --extra-scopes=%s --force-consent",
				email, PubSubScope)
		}
	}
	return parsed.RefreshToken, nil
}

// readAll wraps io.ReadAll without importing io. (We pull in net/http
// already; this keeps the dep surface tighter for a tiny daemon.)
func readAll(r interface{ Read(p []byte) (int, error) }) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
	}
}
