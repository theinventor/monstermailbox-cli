package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/config"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// EnvFeedbackEndpoint is the env var that, when set, causes
// `mmb feedback "..."` to ALSO POST the entry to that URL after
// writing the local JSONL log. Surfaced in agent-context's
// endpoints.feedback_upstream so agents can detect whether their
// reports reach maintainers.
const EnvFeedbackEndpoint = "MONSTERMAILBOX_FEEDBACK_ENDPOINT"

// feedbackEntry is one row in the local feedback.jsonl ledger and
// the body shape POSTed to the upstream endpoint when configured.
type feedbackEntry struct {
	Timestamp  string `json:"timestamp"` // RFC3339, UTC
	CLIVersion string `json:"cli_version"`
	Profile    string `json:"profile,omitempty"`
	Text       string `json:"text"`
}

// `mmb feedback` — principle 10 (two-way I/O). Agents constantly hit
// friction (rejected flags, race conditions, errors that don't
// enumerate); without a channel back to maintainers it never gets
// reported, and the CLI quietly stays painful. This command writes
// locally by default and, when MONSTERMAILBOX_FEEDBACK_ENDPOINT is
// set, also POSTs upstream.
//
// Subcommands:
//
//	mmb feedback "<text>"   — record one entry
//	mmb feedback list       — show the local log (newest first)
//	mmb feedback path       — print the JSONL path (for tooling)
func newFeedbackCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "feedback [text...]",
		Short: "Record local feedback for the CLI maintainers (optionally POSTs upstream)",
		Long: `Record short feedback notes locally and (when configured) post them
upstream to the maintainers. Three forms:

  mmb feedback "the --tier flag rejects 'enterprise'..."
  mmb feedback record "..."        # explicit subcommand form
  mmb feedback list                # show recent entries (newest first)
  mmb feedback path                # print the JSONL ledger path

Set MONSTERMAILBOX_FEEDBACK_ENDPOINT to also POST entries upstream.`,
		// Bare form: `mmb feedback "<text>"` runs as if the user had typed
		// `mmb feedback record "<text>"`. If they type a known subcommand
		// (list, path, record, help) we let cobra route as usual.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			text := strings.TrimSpace(strings.Join(args, " "))
			if text == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("feedback text MUST be non-empty"))
			}
			return recordFeedback(cmd.OutOrStdout(), text)
		},
	}
	c.AddCommand(newFeedbackRecordCmd())
	c.AddCommand(newFeedbackListCmd())
	c.AddCommand(newFeedbackPathCmd())
	return c
}

// newFeedbackRecordCmd is `mmb feedback "<text>"`. The positional arg
// IS the feedback text. Empty text exits Usage rather than recording
// a useless empty row.
func newFeedbackRecordCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "record <text>",
		Short: "Record one feedback entry (alias: bare `mmb feedback <text>`)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.TrimSpace(strings.Join(args, " "))
			if text == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("feedback text MUST be non-empty"))
			}
			return recordFeedback(cmd.OutOrStdout(), text)
		},
	}
}

func newFeedbackListCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "list",
		Short: "List recorded feedback entries (newest first)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			entries, err := readFeedback()
			if err != nil {
				return err
			}
			if limit > 0 && len(entries) > limit {
				entries = entries[len(entries)-limit:]
			}
			// Reverse so newest is first.
			for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
				entries[i], entries[j] = entries[j], entries[i]
			}
			return printJSON(cmd.OutOrStdout(), map[string]any{
				"path":    feedbackPath(),
				"count":   len(entries),
				"entries": entries,
			})
		},
	}
	c.Flags().IntVar(&limit, "limit", 0, "max entries to return (0 = all)")
	return c
}

func newFeedbackPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the local feedback ledger path",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), feedbackPath())
			return nil
		},
	}
}

// recordFeedback appends one entry to the local JSONL log and, if
// EnvFeedbackEndpoint is set, POSTs it upstream. Reports both
// outcomes in the JSON response so the agent can see exactly what
// happened (local-only vs. local+upstream + status).
func recordFeedback(out io.Writer, text string) error {
	entry := feedbackEntry{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		CLIVersion: Version,
		Profile:    activeProfileName(),
		Text:       text,
	}

	if err := appendFeedback(entry); err != nil {
		return fmt.Errorf("append feedback: %w", err)
	}

	resp := map[string]any{
		"recorded":     true,
		"path":         feedbackPath(),
		"upstream":     nil,
		"upstream_url": nil,
	}

	if upstream := os.Getenv(EnvFeedbackEndpoint); upstream != "" {
		status, err := postFeedback(upstream, entry)
		resp["upstream_url"] = upstream
		if err != nil {
			// Local write succeeded; upstream failure is reported but does
			// not fail the command — the entry is captured either way.
			resp["upstream"] = map[string]any{
				"posted": false,
				"error":  err.Error(),
			}
		} else {
			resp["upstream"] = map[string]any{
				"posted": true,
				"status": status,
			}
		}
	}

	return printJSON(out, resp)
}

// readFeedback loads every entry from the local JSONL log. Missing
// file is NOT an error — it just means no feedback has been recorded
// yet, so we return an empty slice.
func readFeedback() ([]feedbackEntry, error) {
	path := feedbackPath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []feedbackEntry{}, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var entries []feedbackEntry
	scanner := bufio.NewScanner(f)
	// Allow long single-line entries (default 64KB cap can be too small
	// for paste-in repros). 1MB ceiling per line is fine for prose.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e feedbackEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// Skip malformed lines rather than aborting the whole list —
			// older entries from a future schema shouldn't break the CLI.
			continue
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return entries, nil
}

func appendFeedback(e feedbackEntry) error {
	path := feedbackPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(e)
}

// postFeedback POSTs an entry to the configured upstream endpoint.
// Returns the HTTP status (or 0 on transport failure) and an error
// for non-2xx + transport errors.
func postFeedback(url string, e feedbackEntry) (int, error) {
	body, err := json.Marshal(e)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", client.UserAgentPrefix+"/"+Version)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// feedbackPath is the JSONL log location. Honors $MMB_FEEDBACK_PATH
// (test override) and falls back to a sibling of the credentials
// config so both files live under one config directory.
func feedbackPath() string {
	if explicit := os.Getenv("MMB_FEEDBACK_PATH"); explicit != "" {
		return explicit
	}
	// Same directory as config.json so a single chmod covers both.
	return filepath.Join(filepath.Dir(config.Path()), "feedback.jsonl")
}

// activeProfileName returns the name of the profile the CLI would
// resolve right now, or "" if env-var auth or no auth is in use.
// Used to tag feedback entries so a multi-profile user can see
// which agent identity reported what.
func activeProfileName() string {
	c := newAPIClient()
	if strings.HasPrefix(c.Source, "profile:") {
		return strings.TrimPrefix(c.Source, "profile:")
	}
	return ""
}
