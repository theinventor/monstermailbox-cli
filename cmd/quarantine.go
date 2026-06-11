package cmd

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

// `mmb quarantine list` → GET /inbox?state=quarantined.
//
// Convenience over `mmb inbox list --state=quarantined`; keeps the
// review queue easy to script from agent-side local tooling.
//
// `mmb quarantine escalate <id>` is a safe guidance surface: the
// release flow is owner-side (a human reviews + releases via the
// dashboard), so the CLI must not expose held body text, links, or
// attachments while pointing the agent at the supported path.
func newQuarantineCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "quarantine",
		Short: "Inspect quarantined messages and escalate for review",
	}
	c.AddCommand(newQuarantineListCmd())
	c.AddCommand(newQuarantineEscalateCmd())
	return c
}

func newQuarantineListCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "list",
		Short: "List quarantined messages awaiting review",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli := newAPIClient()
			q := url.Values{}
			q.Set("state", "quarantined")
			if limit > 0 {
				q.Set("limit", fmt.Sprintf("%d", limit))
			}
			resp, err := cli.Do(http.MethodGet, "/inbox", nil, q)
			if err != nil {
				return fmt.Errorf("GET /inbox?state=quarantined: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = server default)")
	return c
}

func newQuarantineEscalateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "escalate <id>",
		Short: "Show the safe human-review path for a quarantined message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("quarantine release is human-reviewed in the MonsterMailbox dashboard; ask the mailbox owner to review message %s there. The CLI keeps quarantined body text, links, and attachments hidden until the owner releases the message", args[0])
		},
	}
}
