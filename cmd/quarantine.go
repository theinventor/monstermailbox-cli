package cmd

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

// `mmb quarantine list` → GET /inbox?state=quarantined.
//
// Convenience over `mmb inbox list --state=quarantined`; matches
// the spec § "Bridge UX" command shape so muscle memory is the
// same regardless of which surface (CLI vs dashboard) the operator
// is on.
//
// `mmb quarantine escalate <id>` is a v0 stub: the escalation flow
// is owner-side (a human reviews + releases via the dashboard),
// the agent CAN flag a message for accelerated review by writing
// to the message's audit metadata. The endpoint isn't in v0
// OpenAPI yet, so the stub returns "not implemented" cleanly
// rather than masking the missing surface.
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
		Short: "Flag a quarantined message for accelerated human review (v1)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("quarantine escalate is not yet implemented in v0 — release/reject must be done from the dashboard for now (message id: %s)", args[0])
		},
	}
}
