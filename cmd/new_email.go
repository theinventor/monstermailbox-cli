package cmd

import (
	"fmt"
	"net/http"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
	"github.com/spf13/cobra"
)

// `mmb new-email --to <addr> --subject <s> --body <s>` → POST /send.
//
// Starts a brand-new outbound thread. No reply context — if you want to
// reply to an existing message, use `mmb reply-to-email` instead.
//
// Body forms (at least one required):
//   --body <s>            inline plain text
//   --body-html <s>       inline HTML
//   --body-file <path>    plain text from file (HTML doesn't shell-escape well)
//   --body-html-file <p>  HTML from file
//
// When only HTML is supplied, the server auto-derives the plain-text
// fallback for the multipart `text/plain` alternative — agents that
// want pretty plain text should ship their own `--body` too.
func newNewEmailCmd() *cobra.Command {
	var to, subject string
	var cc, bcc []string
	var mf mutationFlags
	var bf bodyFlags
	c := &cobra.Command{
		Use:   "new-email",
		Short: "Send a brand-new outbound thread (no reply context)",
		RunE: func(cmd *cobra.Command, _ []string) error {
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
		},
	}
	c.Flags().StringVar(&to, "to", "", "recipient email — required")
	c.Flags().StringVar(&subject, "subject", "", "subject — required")
	c.Flags().StringSliceVar(&cc, "cc", nil, "cc recipients (comma-separated or repeat the flag)")
	c.Flags().StringSliceVar(&bcc, "bcc", nil, "bcc recipients (comma-separated or repeat the flag)")
	bindBodyFlags(c, &bf)
	bindMutationFlags(c, &mf)
	return c
}
