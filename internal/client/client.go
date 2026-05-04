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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/theinventor/monstermailbox-cli/internal/config"
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
	Version    string

	// Source describes where APIKey came from. Useful for `mmb auth status`
	// and for clear errors that point users at `mmb auth login`. One of:
	// "env", "profile:<name>", "" (no credentials).
	Source string
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
	}

	// Explicit --profile: must exist or we fall through to nothing (the
	// caller will see "auth required" on the first authenticated call).
	if profile != "" {
		if f, err := config.Load(); err == nil {
			if p, ok := f.Get(profile); ok {
				c.BaseURL = strings.TrimRight(p.APIURL, "/")
				c.APIKey = p.APIKey
				c.Source = "profile:" + profile
				return c.fillDefaults()
			}
		}
	}

	// ENV var beats the persisted default.
	if envKey := os.Getenv(EnvAPIKey); envKey != "" {
		c.BaseURL = strings.TrimRight(os.Getenv(EnvAPIURL), "/")
		c.APIKey = envKey
		c.Source = "env"
		return c.fillDefaults()
	}

	// Persisted default profile.
	if f, err := config.Load(); err == nil {
		if p, ok := f.Get(""); ok {
			c.BaseURL = strings.TrimRight(p.APIURL, "/")
			c.APIKey = p.APIKey
			c.Source = "profile:" + f.DefaultProfile
			return c.fillDefaults()
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

	return c.HTTPClient.Do(req)
}

