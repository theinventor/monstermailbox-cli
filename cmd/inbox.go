package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

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
func newInboxListCmd() *cobra.Command {
	var state, since string
	var limit int
	c := &cobra.Command{
		Use:   "list",
		Short: "List recent inbound messages (default: trusted)",
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

			resp, err := cli.Do(http.MethodGet, "/inbox", nil, q)
			if err != nil {
				return fmt.Errorf("GET /inbox: %w", err)
			}
			defer resp.Body.Close()

			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&state, "state", "", "filter by state (trusted|quarantined|rejected)")
	c.Flags().StringVar(&since, "since", "", "ISO8601 lower bound on received_at")
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = server default)")
	return c
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
func passthroughJSON(w io.Writer, resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var pretty any
	if err := json.Unmarshal(body, &pretty); err != nil {
		_, werr := w.Write(body)
		return werr
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(pretty)
}
