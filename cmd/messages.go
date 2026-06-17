package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/enums"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// messages is the parent for participant-history lookups.
func newMessagesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "messages",
		Short: "Search sanitized message history",
	}
	c.AddCommand(newMessagesListCmd())
	return c
}

// `mmb messages list --participant person@example.com` → GET /messages.
//
// This is a read-only history lookup: the server does not mark messages read,
// so there is no --peek flag. Bcc-only matches are excluded server-side.
func newMessagesListCmd() *cobra.Command {
	var participant, state, workState, cursor string
	var limit int

	c := &cobra.Command{
		Use:   "list",
		Short: "List messages involving a participant address",
		Long: `List sanitized message history involving one participant address.

The server matches the normalized address against From, To, and Cc for the
authenticated agent only. Bcc-only matches are intentionally excluded. This
history lookup is read-only and never marks messages read.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			participant = strings.TrimSpace(participant)
			if participant == "" {
				return exitcode.Wrap(exitcode.Usage, fmt.Errorf("--participant is required"))
			}
			if err := enums.Validate("state", state, enums.InboxStates); err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}
			if err := enums.Validate("work-state", workState, enums.WorkStates); err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}

			q := url.Values{}
			q.Set("participant", participant)
			if state != "" {
				q.Set("state", state)
			}
			if workState != "" {
				q.Set("work_state", workState)
			}
			if limit > 0 {
				q.Set("limit", fmt.Sprintf("%d", limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}

			cli := newAPIClient()
			resp, err := cli.Do(http.MethodGet, "/messages", nil, q)
			if err != nil {
				return fmt.Errorf("GET /messages: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&participant, "participant", "", "required: participant email address to match against From, To, or Cc")
	c.Flags().StringVar(&state, "state", "", "filter by trust state (trusted|quarantined|rejected; default server-side: trusted)")
	c.Flags().StringVar(&workState, "work-state", "", "filter by agent-side work_state (e.g. inbox, done, blocked)")
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = server default)")
	c.Flags().StringVar(&cursor, "cursor", "", "opaque next_cursor from a previous response")
	return c
}
