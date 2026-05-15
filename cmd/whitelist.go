package cmd

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

// `mmb whitelist create <sender>` → POST /whitelist.
//
// Sender is an exact sender email or domain (`@example.com`). Regex
// matching is explicit via --sender-regex so broad trust rules are
// visible at the call site. The dashboard's whitelist UI is the same
// surface; the CLI is the agent-side entry point so a Go bot can
// manage its own whitelist without going through the human dashboard.
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
	var sender string
	var senderRegex string
	var subjectRegex string
	c := &cobra.Command{
		Use:   "create [<sender>]",
		Short: "Create a whitelist entry for an exact sender or explicit sender regex",
		Args:  cobra.MaximumNArgs(1),
		RunE:  whitelistCreateRunE(&mf, &sender, &senderRegex, &subjectRegex),
	}
	c.Flags().StringVar(&sender, "sender", "",
		"exact sender email or domain to trust (same as positional <sender>)")
	c.Flags().StringVar(&senderRegex, "sender-regex", "",
		"regular expression for matching trusted senders")
	c.Flags().StringVar(&subjectRegex, "subject-regex", "",
		"optional regular expression that narrows the trusted subject")
	bindMutationFlags(c, &mf)
	return c
}

// newWhitelistAddAliasCmd is the deprecated v0.2 spelling. Each alias
// gets its own mutationFlags binding so cobra can populate them
// independently of the canonical command's bindings.
func newWhitelistAddAliasCmd() *cobra.Command {
	var mf mutationFlags
	var sender string
	var senderRegex string
	var subjectRegex string
	c := &cobra.Command{
		Use:        "add [<sender>]",
		Short:      "(deprecated alias for `whitelist create`)",
		Args:       cobra.MaximumNArgs(1),
		Hidden:     true,
		Deprecated: "use `mmb whitelist create` instead",
		RunE:       whitelistCreateRunE(&mf, &sender, &senderRegex, &subjectRegex),
	}
	c.Flags().StringVar(&sender, "sender", "",
		"exact sender email or domain to trust (same as positional <sender>)")
	c.Flags().StringVar(&senderRegex, "sender-regex", "",
		"regular expression for matching trusted senders")
	c.Flags().StringVar(&subjectRegex, "subject-regex", "",
		"optional regular expression that narrows the trusted subject")
	bindMutationFlags(c, &mf)
	return c
}

func whitelistCreateRunE(
	mf *mutationFlags,
	sender *string,
	senderRegex *string,
	subjectRegex *string,
) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		body, err := whitelistCreateBody(args, *sender, *senderRegex, *subjectRegex)
		if err != nil {
			return err
		}

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

func whitelistCreateBody(args []string, sender, senderRegex, subjectRegex string) (map[string]any, error) {
	positionalSender := ""
	if len(args) > 0 {
		positionalSender = args[0]
	}

	senderSources := 0
	if positionalSender != "" {
		senderSources++
	}
	if sender != "" {
		senderSources++
	}
	if senderRegex != "" {
		senderSources++
	}

	switch {
	case senderSources == 0:
		return nil, errors.New("whitelist create requires an exact sender (<sender> or --sender) or --sender-regex")
	case senderSources > 1:
		return nil, errors.New("whitelist create accepts only one sender source: positional <sender>, --sender, or --sender-regex")
	}

	body := map[string]any{}
	if positionalSender != "" {
		body["sender"] = positionalSender
	} else if sender != "" {
		body["sender"] = sender
	} else {
		body["sender_regex"] = senderRegex
	}
	if subjectRegex != "" {
		body["subject_regex"] = subjectRegex
	}
	return body, nil
}
