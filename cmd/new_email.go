package cmd

import (
	"fmt"
	"net/http"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
	"github.com/spf13/cobra"
)

// `mmb new-email --to <addr> --subject <s> --body <s> [--cc] [--bcc]` → POST /send.
//
// Starts a brand-new outbound thread. No reply context — if you want to
// reply to an existing message, use `mmb reply-to-email` instead.
func newNewEmailCmd() *cobra.Command {
	var to, subject, body string
	var cc, bcc []string
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "new-email",
		Short: "Send a brand-new outbound thread (no reply context)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if to == "" || subject == "" || body == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("--to, --subject, and --body are all required"))
			}
			payload := map[string]any{
				"to":        to,
				"subject":   subject,
				"body_text": body,
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
		},
	}
	c.Flags().StringVar(&to, "to", "", "recipient email — required")
	c.Flags().StringVar(&subject, "subject", "", "subject — required")
	c.Flags().StringVar(&body, "body", "", "plain-text body — required")
	c.Flags().StringSliceVar(&cc, "cc", nil, "cc recipients (comma-separated or repeat the flag)")
	c.Flags().StringSliceVar(&bcc, "bcc", nil, "bcc recipients (comma-separated or repeat the flag)")
	bindMutationFlags(c, &mf)
	return c
}
