package cmd

import (
	"fmt"
	"net/http"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
	"github.com/spf13/cobra"
)

// `mmb send` is the legacy entry point. Kept as a thin alias that warns
// to stderr and dispatches to the new commands based on whether
// --in-reply-to is set. Two minor releases from now this drops.
//
// Outbound from an agent is policy-gated server-side: rate limits,
// recipient allowlists, content guards. The CLI just submits and
// reports the verdict (sent | awaiting_human_approval | rejected).
func newSendCmd() *cobra.Command {
	var to, subject, body, inReplyTo string
	var cc, bcc []string
	c := &cobra.Command{
		Use:   "send",
		Short: "DEPRECATED: use `mmb new-email`, `mmb reply-all`, or `mmb reply-not-all-with-custom-recipients`",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"warning: `mmb send` is deprecated; use `mmb new-email` for fresh threads or `mmb reply-all` to reply (the new default)")

			if to == "" || subject == "" || body == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("--to, --subject, and --body are all required"))
			}
			payload := map[string]any{
				"to":        to,
				"subject":   subject,
				"body_text": body,
			}
			if inReplyTo != "" {
				payload["in_reply_to_message_id"] = inReplyTo
			}
			if len(cc) > 0 {
				payload["cc"] = cc
			}
			if len(bcc) > 0 {
				payload["bcc"] = bcc
			}

			cli := client.New()
			resp, err := cli.Do(http.MethodPost, "/send", payload, nil)
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
	c.Flags().StringVar(&inReplyTo, "in-reply-to", "", "id of the inbound Message this is a reply to (optional)")
	c.Flags().StringSliceVar(&cc, "cc", nil, "cc recipients (comma-separated or repeat the flag)")
	c.Flags().StringSliceVar(&bcc, "bcc", nil, "bcc recipients (comma-separated or repeat the flag)")
	return c
}
