// Package client wraps the monstermailbox HTTP API.
//
// Auth resolution order (first non-empty wins):
//
//  1. Explicit profile name passed to NewWithProfile (--profile flag)
//  2. MONSTERMAILBOX_API_KEY env var (with MONSTERMAILBOX_API_URL)
//  3. Config file's default_profile (~/.config/mmb/config.json)
//  4. Empty Client (calls to authenticated endpoints will 401)
//
// The client deliberately exposes the raw `Do` method (returning
// the http.Response untouched) so test code can assert on the
// request shape without an extra layer of mocking — the tests in
// `cmd/*_test.go` spin up an httptest.NewServer and verify the
// CLI's outgoing request matches the OpenAPI contract.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/theinventor/monstermailbox-cli/internal/config"
	"github.com/theinventor/monstermailbox-cli/internal/credstore"
)

const (
	// EnvAPIKey is the environment variable the CLI reads for the
	// agent API key. Per the v0 spec, scoped keys (inbox:read,
	// outbox:write, etc.) are issued from the dashboard; this
	// variable carries one of them.
	EnvAPIKey = "MONSTERMAILBOX_API_KEY"

	// EnvAPIURL overrides the default production base URL — useful
	// for local dev (set to http://localhost:3000) and for tests
	// (the test server URL).
	EnvAPIURL = "MONSTERMAILBOX_API_URL"

	// DefaultAPIURL is the production target. Override via EnvAPIURL.
	// `monstermailbox.com` is the marketing site — the API lives on the
	// `api.` subdomain. v0.2.0 had this wrong: a fresh CLI with no profile
	// and no MONSTERMAILBOX_API_URL set would POST /agents/register to the
	// marketing site, get a 405 with an empty body, and the user saw "no
	// output, no error, exit 0." See bug repro discussion in v0.2.1.
	DefaultAPIURL = "https://api.monstermailbox.com"

	// AuthScheme — Bearer per OpenAPI's bearerAuth security scheme.
	AuthScheme = "Bearer"

	// UserAgentPrefix is prepended to the CLI's User-Agent so the
	// server can identify CLI traffic and version-pin if needed.
	UserAgentPrefix = "mmb-cli"
)

// Client is the HTTP client wrapper. Construct via New() or NewWithProfile().
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client

	// StreamHTTPClient is used for long-lived Server-Sent Events streams
	// (GET /events). It deliberately has NO total request Timeout —
	// http.Client.Timeout caps the ENTIRE request including body reads,
	// so a 30s timeout would murder the SSE connection every 30 seconds
	// (the bug behind "inbox watch never sees events"). Liveness for the
	// stream is enforced by the caller via a context idle-watchdog reset
	// on each event/heartbeat, plus the transport's dial/header timeouts
	// below for connection-establishment failures.
	StreamHTTPClient *http.Client

	Version string

	// Source describes where APIKey came from. Useful for `mmb auth status`
	// and for clear errors that point users at `mmb auth login`. One of:
	// "env", "profile:<name>", "" (no credentials).
	Source string

	// Backend describes the per-profile storage backend the resolved
	// key came from. One of "keychain", "file", "env", or "" when no
	// credentials are loaded. Surfaces in `auth status` so users can
	// see whether their secret lives on disk or in the OS keyring.
	Backend string
}

// New constructs a Client using the default resolution order (no explicit
// --profile passed). Equivalent to NewWithProfile("").
func New() *Client {
	return NewWithProfile("")
}

// NewWithProfile resolves credentials in the documented order. An explicit
// profile name (from --profile) wins outright; otherwise ENV beats the
// config file's default_profile. MONSTERMAILBOX_API_URL is always honored
// when set (even without an API key) so test/dev setups can target a stub
// server before any creds exist.
func NewWithProfile(profile string) *Client {
	c := &Client{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		StreamHTTPClient: &http.Client{
			// No total Timeout — see the field doc on Client. Connection
			// establishment is still bounded so a dead host fails fast.
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
	}

	// Explicit --profile: must exist or we fall through to nothing (the
	// caller will see "auth required" on the first authenticated call).
	// Do not silently use env/default credentials for a misspelled
	// profile; that would run the command as the wrong identity.
	if profile != "" {
		if f, err := config.Load(); err == nil {
			if p, ok := f.Get(profile); ok {
				return c.loadFromProfile(profile, p).fillDefaults()
			}
		}
		if envURL := os.Getenv(EnvAPIURL); envURL != "" {
			c.BaseURL = strings.TrimRight(envURL, "/")
		}
		return c.fillDefaults()
	}

	// ENV var beats the persisted default.
	if envKey := os.Getenv(EnvAPIKey); envKey != "" {
		c.BaseURL = strings.TrimRight(os.Getenv(EnvAPIURL), "/")
		c.APIKey = envKey
		c.Source = "env"
		c.Backend = credstore.BackendEnv
		return c.fillDefaults()
	}

	// Persisted default profile.
	if f, err := config.Load(); err == nil {
		if p, ok := f.Get(""); ok {
			return c.loadFromProfile(f.DefaultProfile, p).fillDefaults()
		}
	}

	// Nothing configured. Public endpoints (/health, /version,
	// /agents/register) still work — Authorization header is just omitted.
	// Honor MONSTERMAILBOX_API_URL even without a key, so test/dev setups
	// can point at a stub server before logging in.
	if envURL := os.Getenv(EnvAPIURL); envURL != "" {
		c.BaseURL = strings.TrimRight(envURL, "/")
	}
	return c.fillDefaults()
}

func (c *Client) fillDefaults() *Client {
	if c.BaseURL == "" {
		c.BaseURL = DefaultAPIURL
	}
	return c
}

// loadFromProfile populates BaseURL/APIKey/Source/Backend from a Profile,
// resolving the secret through credstore. A keychain miss leaves APIKey
// empty (the caller will see "auth required" on the next authenticated
// call) so a partially-broken keychain doesn't hard-fail every command.
func (c *Client) loadFromProfile(name string, p *config.Profile) *Client {
	c.BaseURL = strings.TrimRight(p.APIURL, "/")
	c.Source = "profile:" + name
	if p.Backend == "" {
		c.Backend = credstore.BackendFile
	} else {
		c.Backend = p.Backend
	}
	if secret, err := credstore.Get(name, p.Backend, p.APIKey); err == nil {
		c.APIKey = secret
	}
	return c
}

// MaskedAPIKey returns a fingerprint suitable for logging — first 8
// chars of the key followed by "…<last4>". Returns "(none)" when no key
// is set. Never returns the raw secret.
func (c *Client) MaskedAPIKey() string {
	k := c.APIKey
	if k == "" {
		return "(none)"
	}
	if len(k) < 12 {
		return "***"
	}
	return k[:8] + "…" + k[len(k)-4:]
}

// Do builds + sends an HTTP request. Path is the API path
// ("/inbox", "/msg/123") — base URL is prepended. Body, if non-nil,
// is JSON-encoded and Content-Type is set. Authorization header is
// set if APIKey is non-empty.
//
// Returns the *http.Response untouched so callers (and tests) can
// assert on status, headers, and body directly.
func (c *Client) Do(method, path string, body any, query url.Values) (*http.Response, error) {
	return c.DoWithHeaders(method, path, body, query, nil)
}

// DoWithHeaders is the extended form of Do that lets callers attach
// additional request headers (e.g. `Idempotency-Key` for principle 4
// safe-retries). Pass nil for `extra` to behave exactly like Do.
func (c *Client) DoWithHeaders(method, path string, body any, query url.Values, extra map[string]string) (*http.Response, error) {
	u := c.BaseURL + path
	if query != nil && len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequest(method, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgentPrefix+"/"+c.Version)

	if c.APIKey != "" {
		req.Header.Set("Authorization", AuthScheme+" "+c.APIKey)
	}

	for k, v := range extra {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	return c.HTTPClient.Do(req)
}

// DoStream opens a long-lived streaming GET (Server-Sent Events) bound
// to ctx, using StreamHTTPClient (no total timeout). Callers MUST drive
// liveness themselves: run an idle watchdog that cancels ctx when no
// bytes arrive for longer than the server's heartbeat interval, and
// close resp.Body when done. `extra` carries request headers such as
// `Last-Event-ID` for reconnect-replay. Auth + User-Agent are set the
// same way as Do/DoWithHeaders.
func (c *Client) DoStream(ctx context.Context, path string, extra map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build stream request: %w", err)
	}

	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", UserAgentPrefix+"/"+c.Version)
	if c.APIKey != "" {
		req.Header.Set("Authorization", AuthScheme+" "+c.APIKey)
	}
	for k, v := range extra {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	sc := c.StreamHTTPClient
	if sc == nil {
		sc = c.HTTPClient // defensive: a hand-built Client without the stream client
	}
	return sc.Do(req)
}
