package cmd

import (
	"fmt"
	"net/http"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/spf13/cobra"
)

// `mmb whitelist add <pattern>` → POST /whitelist.
//
// Pattern is a sender email or domain (`@example.com`). Matching
// inbound from that pattern gets trusted-by-default without
// quarantine. The dashboard's whitelist UI is the same surface;
// the CLI is the agent-side entry point so a Go bot can manage
// its own whitelist without going through the human dashboard.
func newWhitelistCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "whitelist",
		Short: "Manage the agent's sender whitelist",
	}
	c.AddCommand(newWhitelistAddCmd())
	return c
}

func newWhitelistAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <pattern>",
		Short: "Add a sender pattern (email or domain) to the whitelist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"pattern": args[0]}

			cli := client.New()
			resp, err := cli.Do(http.MethodPost, "/whitelist", body, nil)
			if err != nil {
				return fmt.Errorf("POST /whitelist: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
}
