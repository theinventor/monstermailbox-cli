package cmd

import (
	"fmt"
	"net/http"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/spf13/cobra"
)

// `mmb expect --from <addr> --subject <pattern> [--ttl <duration>]`
// → POST /expectations.
//
// Expectations are pre-arranged trust grants — the agent declares
// ahead of time "I'm expecting a reply from CFO@stripe.com about
// the invoice"; matching inbound short-circuits the trust pipeline
// to trusted state without quarantine review.
func newExpectCmd() *cobra.Command {
	var from, subject, ttl string
	c := &cobra.Command{
		Use:   "expect",
		Short: "Pre-arrange a trust grant for an expected inbound message",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" {
				return fmt.Errorf("--from is required (the sender address you expect)")
			}
			body := map[string]any{"from": from}
			if subject != "" {
				body["subject"] = subject
			}
			if ttl != "" {
				body["ttl"] = ttl
			}

			cli := client.New()
			resp, err := cli.Do(http.MethodPost, "/expectations", body, nil)
			if err != nil {
				return fmt.Errorf("POST /expectations: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&from,    "from",    "", "expected sender (email or domain) — required")
	c.Flags().StringVar(&subject, "subject", "", "subject substring to match (optional)")
	c.Flags().StringVar(&ttl,     "ttl",     "", "expectation lifetime (e.g. 24h, 7d) — server-default if omitted")
	return c
}
