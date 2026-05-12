package cmd

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

// `mmb whitelist create <pattern>` → POST /whitelist.
//
// Pattern is a sender email or domain (`@example.com`). Matching
// inbound from that pattern gets trusted-by-default without
// quarantine. The dashboard's whitelist UI is the same surface;
// the CLI is the agent-side entry point so a Go bot can manage
// its own whitelist without going through the human dashboard.
//
// Verb choice: principle 6 — `create` is the canonical resource-
// creation verb (matching the get/list/create/update/delete set).
// `whitelist add` is preserved as a hidden alias for one release.
func newWhitelistCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "whitelist",
		Short: "Manage the agent's sender whitelist",
	}
	c.AddCommand(newWhitelistCreateCmd())
	c.AddCommand(newWhitelistAddAliasCmd())
	return c
}

func newWhitelistCreateCmd() *cobra.Command {
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "create <pattern>",
		Short: "Create a whitelist entry for a sender pattern (email or domain)",
		Args:  cobra.ExactArgs(1),
		RunE:  whitelistCreateRunE(&mf),
	}
	bindMutationFlags(c, &mf)
	return c
}

// newWhitelistAddAliasCmd is the deprecated v0.2 spelling. Each alias
// gets its own mutationFlags binding so cobra can populate them
// independently of the canonical command's bindings.
func newWhitelistAddAliasCmd() *cobra.Command {
	var mf mutationFlags
	c := &cobra.Command{
		Use:        "add <pattern>",
		Short:      "(deprecated alias for `whitelist create`)",
		Args:       cobra.ExactArgs(1),
		Hidden:     true,
		Deprecated: "use `mmb whitelist create` instead",
		RunE:       whitelistCreateRunE(&mf),
	}
	bindMutationFlags(c, &mf)
	return c
}

func whitelistCreateRunE(mf *mutationFlags) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		body := map[string]any{"pattern": args[0]}

		if mf.DryRun {
			return printJSON(cmd.OutOrStdout(),
				newDryRunEnvelope(http.MethodPost, "/whitelist", body, *mf))
		}

		cli := newAPIClient()
		resp, err := cli.DoWithHeaders(http.MethodPost, "/whitelist", body, nil, mf.Headers())
		if err != nil {
			return fmt.Errorf("POST /whitelist: %w", err)
		}
		defer resp.Body.Close()
		return passthroughJSON(cmd.OutOrStdout(), resp)
	}
}
