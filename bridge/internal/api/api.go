// Package api is the HTTP client for monstermailbox.
//
// Four endpoints map to four methods. All return descriptive errors
// containing the response status + body so tests + status output can
// surface server-side failures verbatim.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client carries the API base URL + bearer key for authenticated calls.
type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// New returns a Client with a 30s default HTTP timeout.
func New(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// EnrollResponse mirrors POST /bridges/enroll.
type EnrollResponse struct {
	APIBaseURL    string `json:"api_base_url"`
	APIKey        string `json:"api_key"`
	AgentEmail    string `json:"agent_email"`
	AccountEmail  string `json:"account_email"`
}

// Enroll exchanges a one-time enrollment token (no Bearer required).
func Enroll(ctx context.Context, baseURL, enrollmentToken string) (*EnrollResponse, error) {
	body, _ := json.Marshal(map[string]string{"enrollment_token": enrollmentToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/bridges/enroll", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("call /bridges/enroll: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("/bridges/enroll responded %s: %s", resp.Status, respBody)
	}
	var out EnrollResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("parse /bridges/enroll response: %w (body: %s)", err, respBody)
	}
	return &out, nil
}

// WhitelistEntry mirrors openapi `WhitelistEntry`.
type WhitelistEntry struct {
	ID            string `json:"id"`
	Sender        string `json:"sender"`
	SenderRegex   string `json:"sender_regex"`
	SubjectRegex  string `json:"subject_regex"`
	AutoAdded     bool   `json:"auto_added"`
	AutoAddedFrom string `json:"auto_added_source"`
	CreatedAt     string `json:"created_at"`
}

// PolicyResponse mirrors GET /bridge/policy.
type PolicyResponse struct {
	Whitelist     []WhitelistEntry `json:"whitelist"`
	Version       int              `json:"version"`
	LastUpdatedAt string           `json:"last_updated_at"`
}

// GetPolicy fetches the dashboard-synced whitelist + version.
func (c *Client) GetPolicy(ctx context.Context) (*PolicyResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/bridge/policy", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET /bridge/policy: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET /bridge/policy %s: %s", resp.Status, body)
	}
	var out PolicyResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse policy: %w (body: %s)", err, body)
	}
	return &out, nil
}

// PostInbound POSTs raw RFC822 MIME bytes to /bridge/inbound. Returns
// the inbound_email_id on success.
func (c *Client) PostInbound(ctx context.Context, raw []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/bridge/inbound", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "message/rfc822")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("POST /bridge/inbound: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 202 {
		return "", fmt.Errorf("POST /bridge/inbound %s: %s", resp.Status, body)
	}
	var parsed struct {
		InboundEmailID string `json:"inbound_email_id"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse inbound response: %w (body: %s)", err, body)
	}
	return parsed.InboundEmailID, nil
}

// RotateResponse mirrors POST /bridge/rotate.
type RotateResponse struct {
	APIKey        string `json:"api_key"`
	PreviousLast4 string `json:"previous_last4"`
}

// Rotate calls POST /bridge/rotate. The OLD key is revoked atomically;
// caller MUST persist the new key before exiting or a rotation
// without persistence kills the bridge.
func (c *Client) Rotate(ctx context.Context) (*RotateResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/bridge/rotate", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST /bridge/rotate: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("POST /bridge/rotate %s: %s", resp.Status, body)
	}
	var out RotateResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse rotate response: %w (body: %s)", err, body)
	}
	return &out, nil
}
