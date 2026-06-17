package cmd

import (
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/enums"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// `mmb messages list --participant person@example.com` → GET /messages.
//
// Participant history is a read-only lookup for agent CRM/support workflows.
// It does not send --peek because the server never marks rows read for this
// endpoint. The backend matches visible From/To/CC participants only; BCC is
// intentionally excluded from the API contract.
func newMessagesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "messages",
		Short: "Query read-only message history by participant",
	}
	c.AddCommand(newMessagesListCmd())
	return c
}

func newMessagesListCmd() *cobra.Command {
	var participant, state, workState, cursor string
	var limit int

	c := &cobra.Command{
		Use:   "list",
		Short: "List read-only message history involving a participant address",
		Long: `List read-only message history for one participant address.

The server matches the normalized participant in From, To, or CC. BCC is
excluded because hidden recipients are not visible conversation participants.
This command emits the backend JSON unchanged on stdout and never marks
messages read.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			normalized, err := normalizeParticipantFlag(participant)
			if err != nil {
				return err
			}
			if err := enums.Validate("state", state, enums.InboxStates); err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}
			if err := enums.Validate("work-state", workState, enums.WorkStates); err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}

			q := url.Values{}
			q.Set("participant", normalized)
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
	c.Flags().StringVar(&participant, "participant", "", "required: participant email to match in From/To/CC (BCC excluded)")
	c.Flags().StringVar(&state, "state", "", "filter by trust state (trusted|quarantined|rejected; default server-side trusted)")
	c.Flags().StringVar(&workState, "work-state", "", "filter by agent-side work_state (e.g. inbox, blocked, done)")
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = server default)")
	c.Flags().StringVar(&cursor, "cursor", "", "opaque next_cursor from a previous /messages page")
	return c
}

func normalizeParticipantFlag(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", exitcode.Wrap(exitcode.Usage, fmt.Errorf("--participant is required"))
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Address != raw || strings.Count(addr.Address, "@") != 1 {
		return "", exitcode.Wrap(exitcode.Usage, fmt.Errorf("--participant must be a valid email address"))
	}
	return strings.ToLower(addr.Address), nil
}
