package cmd

import (
	"fmt"
	"net/http"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
	"github.com/spf13/cobra"
)

// `mmb new-message` is the v0.7 spelling that briefly replaced
// `new-email`. Reverted in v0.8 — kept here as a hidden deprecated
// alias so anyone who started using v0.7's name keeps working
// through one more release. Removed in v0.9 along with
// reply-to-email.
func newNewMessageAliasCmd() *cobra.Command {
	var to, subject string
	var cc, bcc []string
	var mf mutationFlags
	var bf bodyFlags
	c := &cobra.Command{
		Use:        "new-message",
		Short:      "(deprecated alias for `new-email`)",
		Hidden:     true,
		Deprecated: "use `mmb new-email` instead",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runNewMessage(cmd, to, subject, cc, bcc, &bf, mf)
		},
	}
	c.Flags().StringVar(&to, "to", "", "recipient email — required")
	c.Flags().StringVar(&subject, "subject", "", "subject — required")
	c.Flags().StringSliceVar(&cc, "cc", nil, "cc recipients")
	c.Flags().StringSliceVar(&bcc, "bcc", nil, "bcc recipients")
	bindBodyFlags(c, &bf)
	bindMutationFlags(c, &mf)
	return c
}

// runNewMessage is the shared body for `new-email` and the
// `new-message` deprecated alias. Pulled out so both register the
// same validation + payload shape. The function name keeps its v0.7
// spelling for code-archaeology continuity; rename in v0.9 cleanup.
func runNewMessage(cmd *cobra.Command, to, subject string, cc, bcc []string, bf *bodyFlags, mf mutationFlags) error {
	if to == "" || subject == "" {
		return exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("--to and --subject are both required"))
	}
	text, html, err := bf.resolve()
	if err != nil {
		return err
	}
	payload := map[string]any{
		"to":      to,
		"subject": subject,
	}
	if text != "" {
		payload["body_text"] = text
	}
	if html != "" {
		payload["body_html"] = html
	}
	if len(cc) > 0 {
		payload["cc"] = cc
	}
	if len(bcc) > 0 {
		payload["bcc"] = bcc
	}

	if mf.DryRun {
		return printJSON(cmd.OutOrStdout(),
			newDryRunEnvelope(http.MethodPost, "/send", payload, mf))
	}

	cli := client.New()
	resp, err := cli.DoWithHeaders(http.MethodPost, "/send", payload, nil, mf.Headers())
	if err != nil {
		return fmt.Errorf("POST /send: %w", err)
	}
	defer resp.Body.Close()
	return passthroughJSON(cmd.OutOrStdout(), resp)
}
