// Package gogcli wraps `gog` invocations the daemon needs:
//
//   - WatchStart    — registers a Gmail Pub/Sub watch (idempotent;
//                     gog stores state per-account).
//   - History       — enumerates new message IDs since a historyId.
//   - GetRaw        — fetches a message's RFC 822 MIME bytes.
//   - GetMetadata   — fetches `from`+`subject` for fast whitelist match.
//
// `gog` is found via $PATH or $MMB_BRIDGE_GOG_BIN (tests inject a
// fake). All commands run with a context-bound timeout so a frozen
// gog can't stall the whole daemon.
package gogcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Client carries the account email + gog binary path used for every
// invocation.
type Client struct {
	Account string // gmail account, e.g. you@gmail.com
	Bin     string // path to gog binary; "" → look up "gog" on $PATH
}

// New returns a Client for the given account using whichever binary
// $MMB_BRIDGE_GOG_BIN points at, falling back to $PATH lookup.
func New(account string) *Client {
	bin := os.Getenv("MMB_BRIDGE_GOG_BIN")
	if bin == "" {
		bin = "gog"
	}
	return &Client{Account: account, Bin: bin}
}

// WatchStart calls `gog gmail watch start --topic … --account …`. The
// command is idempotent on gog's side: re-running with the same topic
// just refreshes the watch.
func (c *Client) WatchStart(ctx context.Context, topic string) error {
	out, err := c.run(ctx, "gmail", "watch", "start", "--topic", topic)
	if err != nil {
		return fmt.Errorf("`gog gmail watch start --topic %s` failed: %w (output: %s)", topic, err, out)
	}
	return nil
}

// HistoryItem is a slim slice of `gog gmail history --json`. Only
// fields the daemon needs are surfaced; gog's full schema is wider.
type HistoryItem struct {
	ID      string `json:"id"`
	HistID  string `json:"historyId,omitempty"`
	Added   []struct {
		Message struct {
			ID       string   `json:"id"`
			ThreadID string   `json:"threadId"`
			LabelIDs []string `json:"labelIds,omitempty"`
		} `json:"message"`
	} `json:"messagesAdded,omitempty"`
}

// History returns the list of (added) Gmail message IDs since
// `sinceHistoryID`. Empty list + `nextHistoryID` is the no-op case.
func (c *Client) History(ctx context.Context, sinceHistoryID string) (msgIDs []string, nextHistoryID string, err error) {
	out, err := c.runJSON(ctx, "gmail", "history", "--since", sinceHistoryID, "--all")
	if err != nil {
		return nil, "", fmt.Errorf("`gog gmail history --since %s`: %w", sinceHistoryID, err)
	}
	// Tolerate two shapes: a top-level array of HistoryItem, or
	// {"history":[...], "historyId":"…"} — gog's exact JSON envelope
	// has shifted across minor versions.
	var asArr []HistoryItem
	if err := json.Unmarshal(out, &asArr); err == nil {
		ids, hi := extractIDs(asArr)
		return ids, hi, nil
	}
	var asObj struct {
		History   []HistoryItem `json:"history"`
		HistoryID string        `json:"historyId"`
	}
	if err := json.Unmarshal(out, &asObj); err != nil {
		return nil, "", fmt.Errorf("parse gog history JSON: %w (raw: %s)", err, truncate(out, 500))
	}
	ids, hi := extractIDs(asObj.History)
	if asObj.HistoryID != "" {
		hi = asObj.HistoryID
	}
	return ids, hi, nil
}

func extractIDs(items []HistoryItem) ([]string, string) {
	var ids []string
	seen := make(map[string]struct{})
	var lastHID string
	for _, item := range items {
		if item.HistID != "" {
			lastHID = item.HistID
		}
		for _, a := range item.Added {
			id := a.Message.ID
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids, lastHID
}

// MessageMetadata is the slim shape returned by `gog gmail get
// --format=metadata --headers=From,Subject`.
type MessageMetadata struct {
	From    string
	Subject string
}

// GetMetadata fetches just the From + Subject headers — cheaper than
// raw, used to evaluate the whitelist before paying for the full
// MIME fetch.
func (c *Client) GetMetadata(ctx context.Context, messageID string) (MessageMetadata, error) {
	out, err := c.runJSON(ctx, "gmail", "get", messageID,
		"--format", "metadata",
		"--headers", "From,Subject")
	if err != nil {
		return MessageMetadata{}, fmt.Errorf("`gog gmail get %s --format=metadata`: %w", messageID, err)
	}
	// gog's metadata response: {payload: {headers: [{name, value}, ...]}, ...}
	var parsed struct {
		Payload struct {
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return MessageMetadata{}, fmt.Errorf("parse metadata: %w (raw: %s)", err, truncate(out, 500))
	}
	md := MessageMetadata{}
	for _, h := range parsed.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "from":
			md.From = h.Value
		case "subject":
			md.Subject = h.Value
		}
	}
	return md, nil
}

// GetRaw returns the message's raw RFC 822 MIME bytes (decoded from
// the base64url `raw` field gog emits when --format=raw). Tests can
// stub this out via $MMB_BRIDGE_GOG_BIN.
func (c *Client) GetRaw(ctx context.Context, messageID string) ([]byte, error) {
	out, err := c.runJSON(ctx, "gmail", "get", messageID, "--format", "raw")
	if err != nil {
		return nil, fmt.Errorf("`gog gmail get %s --format=raw`: %w", messageID, err)
	}
	// gog emits a JSON object with a base64url-encoded `raw` field
	// when --format=raw + JSON is asked for.
	var parsed struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parse raw response: %w (raw: %s)", err, truncate(out, 500))
	}
	if parsed.Raw == "" {
		return nil, errors.New("empty `raw` field in gog get response")
	}
	return decodeBase64URL(parsed.Raw)
}

// run executes `gog -a <account> -j <args...>` (or no -j if jsonOnly
// is false) and returns combined stdout. stderr is swallowed into the
// error message so callers see why a command failed.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"-a", c.Account}, args...)
	cmd := exec.CommandContext(ctx, c.Bin, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	return stdout.Bytes(), nil
}

func (c *Client) runJSON(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"-a", c.Account, "-j"}, args...)
	cmd := exec.CommandContext(ctx, c.Bin, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	return stdout.Bytes(), nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

func decodeBase64URL(s string) ([]byte, error) {
	return base64Decode(s)
}
