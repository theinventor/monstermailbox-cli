package cmd

import (
	"fmt"
	"net/http"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/spf13/cobra"
)

// `mmb register --address <local-part> --email <owner>` → POST /agents/register.
//
// Public, unauthenticated endpoint — the chicken-and-egg case where you
// don't have an API key yet because you're asking the server to mint one.
// The response includes the new API key ONCE (it's never re-fetchable);
// the CLI prints a clear note + an export line so the operator can
// immediately use it for subsequent calls.
//
// Side effect: server sends an HTML invite email to <owner-email> with a
// claim-token URL. The agent operates immediately; flows requiring human
// authority (quarantine release, blocked-outbound approval) block until
// the human claims the account.
//
// Status codes (per openapi.yaml):
//
//	201 — created
//	409 — address already taken
//	422 — validation failed
func newRegisterCmd() *cobra.Command {
	var address, email string
	c := &cobra.Command{
		Use:   "register",
		Short: "Register a new agent and trigger the human-owner invite flow",
		Long: `Creates an agent at <address>@monstermailbox.com and sends an HTML invite
to <owner-email>. Returns the agent's primary API key — SHOWN ONCE.
Save it immediately; there is no way to retrieve it again.

This command does NOT use MONSTERMAILBOX_API_KEY (the endpoint is public).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if address == "" || email == "" {
				return fmt.Errorf("--address and --email are both required")
			}
			payload := map[string]any{
				"desired_address": address,
				"owner_email":     email,
			}

			cli := client.New()
			// /agents/register is unauthenticated. Zero the API key so a
			// stale/wrong env var doesn't get sent on a public endpoint.
			cli.APIKey = ""
			resp, err := cli.Do(http.MethodPost, "/agents/register", payload, nil)
			if err != nil {
				return fmt.Errorf("POST /agents/register: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusCreated {
				// Wrap the JSON output with a SAVE-THIS-KEY banner.
				fmt.Fprintln(cmd.ErrOrStderr(), "✓ agent created — save the api_key NOW (will not be shown again)")
				fmt.Fprintln(cmd.ErrOrStderr(), "  to use immediately:")
				fmt.Fprintln(cmd.ErrOrStderr(), "    export MONSTERMAILBOX_API_KEY=<api_key from below>")
				fmt.Fprintln(cmd.ErrOrStderr(), "")
			}
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&address, "address", "", "desired local-part (e.g. 'my-agent' → my-agent@monstermailbox.com) — required")
	c.Flags().StringVar(&email, "email", "", "human owner's email — receives the claim invite — required")
	return c
}
