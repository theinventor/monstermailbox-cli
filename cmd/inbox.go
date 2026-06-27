package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/enums"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
	"github.com/theinventor/monstermailbox-cli/internal/sse"
)

// streamIdleTimeout bounds how long the SSE connection may go without
// ANY traffic (event or heartbeat comment) before we treat it as dead
// and reconnect. Must be comfortably larger than the server's heartbeat
// interval (15s) so a single delayed/lost heartbeat doesn't cause a
// needless reconnect, but small enough that a truly dead connection is
// noticed promptly.
const streamIdleTimeout = 45 * time.Second

// inbox is the parent for `inbox list`, `inbox watch`, `inbox wait`.
func newInboxCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "inbox",
		Short: "Read sanitized inbound messages",
	}
	c.AddCommand(newInboxListCmd())
	c.AddCommand(newInboxWatchCmd())
	c.AddCommand(newInboxWaitCmd())
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
	var state, since, workState string
	var limit int
	var all, peek bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List recent inbound messages (default: unread, trust-state trusted)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := enums.Validate("state", state, enums.InboxStates); err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}
			if err := enums.Validate("work-state", workState, enums.WorkStates); err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}
			cli := newAPIClient()
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
			if workState != "" {
				q.Set("work_state", workState)
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
				return nil
			}

			// Non-2xx: surface the status so the agent sees the failure.
			// (The same invariant `passthroughJSON` enforces — restated
			// here because `inbox list` reads the body itself for the
			// hint logic.) See the v0.2.0 silent-register bug for why.
			hint := ""
			if len(body) == 0 {
				hint = fmt.Sprintf(" (empty body — check %s is the right API URL)", resp.Request.URL.String())
			}
			return exitcode.Wrap(exitcode.FromHTTPStatus(resp.StatusCode),
				fmt.Errorf("HTTP %d %s%s", resp.StatusCode, http.StatusText(resp.StatusCode), hint))
		},
	}
	c.Flags().StringVar(&state, "state", "", "filter by trust state (trusted|quarantined|rejected)")
	c.Flags().StringVar(&since, "since", "", "ISO8601 lower bound on received_at")
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = server default)")
	c.Flags().BoolVar(&all, "all", false, "include messages already marked read (default: unread only)")
	c.Flags().BoolVar(&peek, "peek", false, "do NOT mark the returned messages as read")
	c.Flags().StringVar(&workState, "work-state", "",
		"filter by agent-side work_state (e.g. inbox = your actual queue, blocked, awaiting_reply); see WorkStates enum")
	return c
}

// inboxEnvelope is the response shape for /inbox — only the fields
// we need for the human-friendly stderr hint.
type inboxEnvelope struct {
	Messages []json.RawMessage `json:"messages"`
	Meta     *struct {
		Showing struct {
			State     string `json:"state"`
			Unread    bool   `json:"unread"`
			Peek      bool   `json:"peek"`
			WorkState string `json:"work_state"`
		} `json:"showing"`
		Returned int `json:"returned"`
		Counts   map[string]struct {
			Unread int `json:"unread"`
			Total  int `json:"total"`
		} `json:"counts"`
		WorkStateCounts map[string]int `json:"work_state_counts"`
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

	// Work-state breakdown — only emit non-zero buckets (every agent
	// has a hundred zero-bucket states the operator doesn't care
	// about). The order mirrors the openapi.yaml WorkState enum so
	// the line reads in a predictable shape.
	workParts := []string{}
	for _, w := range []string{"inbox", "in_progress", "awaiting_reply", "done", "skipped", "blocked", "deferred"} {
		if n := env.Meta.WorkStateCounts[w]; n > 0 {
			workParts = append(workParts, fmt.Sprintf("%s %d", w, n))
		}
	}
	workSection := ""
	if len(workParts) > 0 {
		workSection = " · work: " + strings.Join(workParts, ", ")
	}

	tail := ""
	if !allFlag {
		tail = " · use --all for already-read"
	}

	return fmt.Sprintf("%s · totals: %s%s%s", prefix, totals, workSection, tail)
}

// `mmb inbox watch [--state ...]` → GET /events (SSE long-poll).
//
// Streams events forever, reconnecting with exponential backoff +
// jitter on disconnect (principle 8). Each event is emitted to stdout
// as one JSON line: `{"event":"<name>","data":<original>}` so agents
// can split on \n and parse with a streaming JSON decoder. Heartbeat
// comments from the server are NOT emitted to stdout — they only
// reset the reconnect timer.
//
// `--state` filters client-side: events whose payload doesn't match
// the requested trust state are dropped before stdout. The server
// emits everything; the CLI narrows.
func newInboxWatchCmd() *cobra.Command {
	var jsonMode bool
	var state string
	var maxReconnects int
	c := &cobra.Command{
		Use:   "watch",
		Short: "Stream inbound events as they arrive (auto-reconnect)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := enums.Validate("state", state, enums.InboxStates); err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}
			_ = jsonMode // accepted; output is always JSON-per-line
			return runEventStream(cmd, eventStreamOptions{
				stopOnFirst:   false,
				stateFilter:   state,
				maxReconnects: maxReconnects,
			})
		},
	}
	c.Flags().BoolVar(&jsonMode, "json", true, "emit raw JSON events per line (always on)")
	c.Flags().StringVar(&state, "state", "", "only emit events for this trust state (trusted|quarantined|rejected)")
	c.Flags().IntVar(&maxReconnects, "max-reconnects", 0, "stop after N reconnect attempts (0 = forever)")
	return c
}

// `mmb inbox wait [--timeout=5m] [--state=...]` — block once for the
// next matching event, then exit (principle 8: --wait-style).
//
// Emits exactly one JSON event to stdout on success; exits with a
// timeout-class error if the window elapses with no match. Use this
// inside agent loops where "watch forever" is the wrong shape — most
// agent flows want "block until something arrives, then act."
func newInboxWaitCmd() *cobra.Command {
	var state string
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "wait",
		Short: "Block once for the next inbound event, then exit",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := enums.Validate("state", state, enums.InboxStates); err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}
			return runEventStream(cmd, eventStreamOptions{
				stopOnFirst:     true,
				stateFilter:     state,
				overallDeadline: time.Now().Add(timeout),
				maxReconnects:   0,
			})
		},
	}
	c.Flags().StringVar(&state, "state", "", "only count events for this trust state (trusted|quarantined|rejected)")
	c.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "give up after this duration with no matching event")
	return c
}

// eventStreamOptions configures runEventStream's behavior. Both
// `inbox watch` (forever) and `inbox wait` (one-shot) are the same
// loop with different exit conditions:
//
//   - watch       → stopOnFirst=false, overallDeadline=zero
//   - wait        → stopOnFirst=true,  overallDeadline=now+timeout
type eventStreamOptions struct {
	stopOnFirst     bool
	stateFilter     string
	overallDeadline time.Time // zero means no deadline
	maxReconnects   int
}

// runEventStream is the shared SSE consumer for `inbox watch` and
// `inbox wait`. Reconnects with exponential backoff + jitter on
// disconnect; exits cleanly on EOF when stopOnFirst is true and a
// matching event has been emitted; honors overallDeadline for the
// `--timeout` shape.
func runEventStream(cmd *cobra.Command, opts eventStreamOptions) error {
	cli := newAPIClient()
	out := cmd.OutOrStdout()

	// Backoff window for reconnects. Capped at 30s — long enough that
	// a flapping server doesn't get hammered, short enough that an
	// agent waiting for an event doesn't sit idle for minutes.
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	reconnects := 0

	// lastEventID is the resume cursor: the id of the most recent inbox
	// event we've seen. Sent as Last-Event-ID on every (re)connect so the
	// server replays anything that landed during the gap. Persists across
	// reconnects within a single watch/wait invocation.
	lastEventID := ""

	for {
		if !opts.overallDeadline.IsZero() && time.Now().After(opts.overallDeadline) {
			return exitcode.Wrap(exitcode.Generic,
				fmt.Errorf("timed out after %s with no matching event", opts.overallDeadline.Sub(time.Now()).Abs()))
		}

		emitted, err := streamOnce(out, cli, opts, &lastEventID)
		if emitted && opts.stopOnFirst {
			return nil
		}

		// Stream ended (EOF or transport error). Decide whether to
		// reconnect or surface the error.
		if opts.maxReconnects > 0 && reconnects >= opts.maxReconnects {
			return err // may be nil on clean EOF
		}
		reconnects++

		// Sleep backoff + ±20% jitter before reconnecting. If a
		// deadline is set, never sleep past it.
		jitter := time.Duration(float64(backoff) * (0.8 + 0.4*rand.Float64()))
		if !opts.overallDeadline.IsZero() {
			remain := time.Until(opts.overallDeadline)
			if remain <= 0 {
				continue // top of loop will surface the timeout error
			}
			if jitter > remain {
				jitter = remain
			}
		}
		time.Sleep(jitter)

		// Exponential, capped.
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// streamOnce opens the /events stream and consumes events until EOF,
// transport error, or the idle watchdog fires. Returns
// (didEmitMatching, err). Heartbeat comments are silently consumed
// (proof of life — they reset the idle watchdog without polluting
// stdout). lastEventID is read to resume via Last-Event-ID and updated
// as inbox events (which carry an id) arrive.
//
// The stream uses StreamHTTPClient (no total timeout); liveness is the
// caller's job, enforced here by a context that we cancel if no event
// or heartbeat arrives within streamIdleTimeout. This replaces the old
// behavior where a 30s http.Client.Timeout killed the stream — and thus
// dropped any event landing in the reconnect gap — every 30 seconds.
func streamOnce(out io.Writer, cli *client.Client, opts eventStreamOptions, lastEventID *string) (bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var headers map[string]string
	if lastEventID != nil && *lastEventID != "" {
		headers = map[string]string{"Last-Event-ID": *lastEventID}
	}

	resp, err := cli.DoStream(ctx, "/events", headers)
	if err != nil {
		return false, fmt.Errorf("GET /events: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		// 401/403 are terminal — don't reconnect on auth failure.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return false, exitcode.Wrap(exitcode.Auth,
				fmt.Errorf("GET /events: HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode)))
		}
		return false, exitcode.Wrap(exitcode.FromHTTPStatus(resp.StatusCode),
			fmt.Errorf("GET /events: HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode)))
	}

	// Idle watchdog: cancel the request (unblocking the read) if the
	// stream goes silent past streamIdleTimeout. Reset on every event,
	// including heartbeat comments.
	watchdog := time.AfterFunc(streamIdleTimeout, cancel)
	defer watchdog.Stop()

	r := sse.New(resp.Body)
	emitted := false
	for {
		ev, err := r.Next()
		if err == io.EOF {
			return emitted, nil
		}
		if err != nil {
			return emitted, err
		}
		watchdog.Reset(streamIdleTimeout)

		// Track the resume cursor from any event carrying an id (inbox
		// events do; connected/heartbeat/outbound don't).
		if ev.ID != "" && lastEventID != nil {
			*lastEventID = ev.ID
		}

		if ev.IsComment {
			// Heartbeat comment — proof of life only, don't emit.
			continue
		}

		// Older servers emitted heartbeat as a NAMED event rather than a
		// comment. Swallow it so it never pollutes stdout or satisfies a
		// one-shot `wait` (it carries no trust state).
		if ev.Name == "heartbeat" {
			continue
		}

		// `connected` is the server's opening hello — useful diagnostic
		// but not user-meaningful for `wait` semantics. Suppress for
		// `wait` so it doesn't satisfy stopOnFirst on the hello alone.
		if ev.Name == "connected" && opts.stopOnFirst {
			continue
		}

		if !matchesStateFilter(ev, opts.stateFilter) {
			continue
		}

		// Emit one JSON line: {"event":"...","data":<original>}.
		// `data` is unparsed pass-through so agents see the server's
		// exact payload.
		envelope := map[string]any{"event": ev.Name}
		var parsed any
		if json.Unmarshal([]byte(ev.Data), &parsed) == nil {
			envelope["data"] = parsed
		} else {
			envelope["data"] = ev.Data
		}
		raw, _ := json.Marshal(envelope)
		if _, werr := fmt.Fprintln(out, string(raw)); werr != nil {
			return emitted, werr
		}
		emitted = true

		if opts.stopOnFirst {
			return emitted, nil
		}
	}
}

// matchesStateFilter returns true when the event's payload's state
// matches the requested filter (or no filter is set). Falls open on
// parse failure — better to over-emit than swallow events the agent
// might want to see.
func matchesStateFilter(ev sse.Event, filter string) bool {
	if filter == "" {
		return true
	}
	var payload struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
		return true
	}
	if payload.State == "" {
		return true
	}
	return payload.State == filter
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
		return exitcode.Wrap(exitcode.FromHTTPStatus(resp.StatusCode),
			fmt.Errorf("HTTP %d %s%s", resp.StatusCode, http.StatusText(resp.StatusCode), hint))
	}
	return nil
}
