package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/spf13/cobra"
)

// inbox is the parent for `inbox list` + `inbox watch`.
func newInboxCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "inbox",
		Short: "Read sanitized inbound messages",
	}
	c.AddCommand(newInboxListCmd())
	c.AddCommand(newInboxWatchCmd())
	return c
}

// `mmb inbox list` → GET /inbox.
//
// Filters: --state (trusted|quarantined|rejected) maps onto the
// OpenAPI query param. --limit + --since are passed through.
//
// Read state (default: only unread):
//
//	--all     show every message, including ones already read
//	--peek    do NOT mark returned messages as read
//
// Why unread-by-default: this CLI is for agents, and an agent
// pulling its inbox almost always means "what's new for me." If you
// want everything (e.g. for an audit), pass --all. After the JSON
// payload, a one-line hint goes to stderr (never stdout) so the JSON
// stays clean for piping into jq.
func newInboxListCmd() *cobra.Command {
	var state, since string
	var limit int
	var all, peek bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List recent inbound messages (default: unread, trust-state trusted)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli := client.New()
			q := url.Values{}
			if state != "" {
				q.Set("state", state)
			}
			if since != "" {
				q.Set("since", since)
			}
			if limit > 0 {
				q.Set("limit", fmt.Sprintf("%d", limit))
			}
			if !all {
				q.Set("unread", "true")
			}
			if peek {
				q.Set("peek", "true")
			}

			resp, err := cli.Do(http.MethodGet, "/inbox", nil, q)
			if err != nil {
				return fmt.Errorf("GET /inbox: %w", err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("read /inbox body: %w", err)
			}
			if _, err := cmd.OutOrStdout().Write(body); err != nil {
				return err
			}

			// Hint to stderr — keeps stdout pure JSON for `| jq`.
			// Only on success.
			if resp.StatusCode == http.StatusOK {
				if hint := buildInboxHint(body, all); hint != "" {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), hint)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&state, "state", "", "filter by state (trusted|quarantined|rejected)")
	c.Flags().StringVar(&since, "since", "", "ISO8601 lower bound on received_at")
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = server default)")
	c.Flags().BoolVar(&all, "all", false, "include messages already marked read (default: unread only)")
	c.Flags().BoolVar(&peek, "peek", false, "do NOT mark the returned messages as read")
	return c
}

// inboxEnvelope is the response shape for /inbox — only the fields
// we need for the human-friendly stderr hint.
type inboxEnvelope struct {
	Messages []json.RawMessage `json:"messages"`
	Meta     *struct {
		Showing struct {
			State  string `json:"state"`
			Unread bool   `json:"unread"`
			Peek   bool   `json:"peek"`
		} `json:"showing"`
		Returned int `json:"returned"`
		Counts   map[string]struct {
			Unread int `json:"unread"`
			Total  int `json:"total"`
		} `json:"counts"`
	} `json:"meta"`
}

// buildInboxHint composes a single-line summary like:
//
//	# 3 unread trusted shown · totals: trusted 50 (3 unread) · quarantined 5 (5 unread) · rejected 2 (0 unread) · use --all for already-read
//
// Returns "" if the response can't be parsed (don't pollute stderr
// with garbage).
func buildInboxHint(body []byte, allFlag bool) string {
	var env inboxEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	if env.Meta == nil {
		return ""
	}

	showing := env.Meta.Showing
	descriptor := showing.State
	if showing.Unread {
		descriptor = "unread " + descriptor
	}
	prefix := fmt.Sprintf("# %d %s shown", env.Meta.Returned, descriptor)

	parts := []string{}
	for _, s := range []string{"trusted", "quarantined", "rejected"} {
		c, ok := env.Meta.Counts[s]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d (%d unread)", s, c.Total, c.Unread))
	}
	totals := strings.Join(parts, " · ")

	tail := ""
	if !allFlag {
		tail = " · use --all for already-read"
	}

	return fmt.Sprintf("%s · totals: %s%s", prefix, totals, tail)
}

// `mmb inbox watch --json` → GET /events (SSE long-poll).
//
// v0 stub: makes the GET and streams the response body to stdout.
// A future loop swaps in a real SSE parser; the wire shape stays.
func newInboxWatchCmd() *cobra.Command {
	var jsonMode bool
	c := &cobra.Command{
		Use:   "watch",
		Short: "Stream inbound events as they arrive",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli := client.New()
			resp, err := cli.Do(http.MethodGet, "/events", nil, nil)
			if err != nil {
				return fmt.Errorf("GET /events: %w", err)
			}
			defer resp.Body.Close()
			_, copyErr := io.Copy(cmd.OutOrStdout(), resp.Body)
			_ = jsonMode // accepted but the server response IS already SSE w/ JSON payloads
			return copyErr
		},
	}
	c.Flags().BoolVar(&jsonMode, "json", false, "emit raw JSON events (default)")
	return c
}

// passthroughJSON pretty-prints a JSON-ish response body. If the
// body isn't JSON it's written through verbatim — useful for error
// responses the server returns as text.
//
// Non-2xx responses return a non-nil error so cobra surfaces them
// to the user with a non-zero exit. Without this, an empty 4xx body
// (e.g. 405 with content-length: 0, which is what the old marketing-
// host default URL produced) printed nothing AND exited 0 — the
// user thought their command silently succeeded. The body, when
// non-empty, is still written so JSON error envelopes from the
// server are visible.
func passthroughJSON(w io.Writer, resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if len(body) > 0 {
		var pretty any
		if err := json.Unmarshal(body, &pretty); err != nil {
			if _, werr := w.Write(body); werr != nil {
				return werr
			}
		} else {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			if werr := enc.Encode(pretty); werr != nil {
				return werr
			}
		}
	}

	if resp.StatusCode >= 400 {
		hint := ""
		if len(body) == 0 {
			// Empty 4xx/5xx is almost always wrong-host or wrong-path.
			// Surface the URL so the user can see immediately if they're
			// hitting the marketing site, a stale dev URL, etc.
			hint = fmt.Sprintf(" (empty body — check %s is the right API URL)", resp.Request.URL.String())
		}
		return fmt.Errorf("HTTP %d %s%s", resp.StatusCode, http.StatusText(resp.StatusCode), hint)
	}
	return nil
}
