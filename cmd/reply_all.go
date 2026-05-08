package cmd

import (
	"fmt"
	"net/http"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
	"github.com/spf13/cobra"
)

// `mmb reply-all <id>` — primary reply verb.
//
// Sends a reply that includes everyone on the original thread (sender,
// other primary recipients, plus the original cc list) MINUS the
// agent's own address. The recipient set is computed SERVER-side via
// `reply_mode=all` — the CLI just supplies the parent message id and
// the server fills in the recipients.
//
// The CLI still fetches the original locally for two purposes:
//   - subject derivation (Re: <orig>) so `--subject-override` is
//     optional but available
//   - quoted-original block in the body, Gmail-style
//
// `--cc` and `--bcc` flags ADD to the server-derived list rather than
// replace it. If you want explicit-only recipients with no auto-fill,
// use `mmb reply-not-all-with-custom-recipients <id> --to ...`.
func newReplyAllCmd() *cobra.Command {
	var subjectOverride string
	var cc, bcc []string
	var noQuote bool
	var mf mutationFlags
	var bf bodyFlags
	c := &cobra.Command{
		Use:   "reply-all <message-id>",
		Short: "Reply to everyone on the original thread (the safe default)",
		Long: `Reply to everyone on the original thread.

Recipients are computed server-side from the parent message's
participant set, minus the agent's own address. --cc / --bcc ADD to
that list rather than replace it. If you need explicit recipient
control, use 'reply-not-all-with-custom-recipients' instead.

Body forms (at least one required): --body / --body-html / --body-file
/ --body-html-file. The original message is quoted Gmail-style unless
--no-quote is passed. Subject is auto-prefixed "Re: " unless the
original already starts with "Re:".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			messageID := args[0]
			text, htmlBody, err := bf.resolve()
			if err != nil {
				return err
			}

			cli := client.New()
			orig, err := fetchOriginalMessage(cli, messageID)
			if err != nil {
				return err
			}

			subject := subjectOverride
			if subject == "" {
				subject = derivedReplySubject(orig.Subject)
			}

			outText := text
			outHTML := htmlBody
			if !noQuote {
				if outText != "" {
					outText = outText + "\n\n" + quoteOriginal(orig)
				}
				if outHTML != "" {
					outHTML = outHTML + "\n" + quoteOriginalHTML(orig)
				}
			}

			payload := map[string]any{
				// `to` is intentionally omitted — when reply_mode=all is
				// set, the server fills it from the parent's from_email.
				// Passing it here would override the server's choice and
				// defeat the point.
				"subject":                subject,
				"in_reply_to_message_id": messageID,
				"reply_mode":             "all",
			}
			if outText != "" {
				payload["body_text"] = outText
			}
			if outHTML != "" {
				payload["body_html"] = outHTML
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

			resp, err := cli.DoWithHeaders(http.MethodPost, "/send", payload, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("POST /send: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&subjectOverride, "subject-override", "", "replace the derived 'Re: <orig>' subject (rare)")
	c.Flags().StringSliceVar(&cc, "cc", nil, "ADDITIONAL cc recipients (server still adds the parent's to+cc set)")
	c.Flags().StringSliceVar(&bcc, "bcc", nil, "bcc recipients")
	c.Flags().BoolVar(&noQuote, "no-quote", false, "send body alone without quoting the original message")
	bindBodyFlags(c, &bf)
	bindMutationFlags(c, &mf)
	_ = exitcode.Generic // silence import when not used directly
	return c
}
