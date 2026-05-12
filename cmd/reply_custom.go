package cmd

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// `mmb reply-not-all-with-custom-recipients <id> --to ...` — secondary
// reply verb. Deliberately awkwardly named so an agent typing it out
// pauses to ask "do I really want to NOT reply-all?" The default
// (reply-all) is almost always correct; this verb exists for the
// cases where you really DO want to narrow the audience (e.g. private
// reply to one participant on a list thread).
//
// Threading + subject derivation + quoting are the same as reply-all.
// The differences:
//   - `--to` is REQUIRED (no auto-derivation; the whole point is
//     explicit control)
//   - reply_mode=sender_only is sent so the server doesn't fill cc
//     from the parent
//   - --cc and --bcc are caller-only (NOT additions to a derived set)
//
// If you want reply-all with adjustments, use `mmb reply-all` and
// pass --cc / --bcc to extend.
func newReplyCustomRecipientsCmd() *cobra.Command {
	var toAddress, subjectOverride string
	var cc, bcc []string
	var noQuote bool
	var mf mutationFlags
	var bf bodyFlags
	c := &cobra.Command{
		Use:   "reply-not-all-with-custom-recipients <message-id>",
		Short: "Reply with explicit recipients only (use only when you're CONFIDENT reply-all is wrong)",
		Long: `Reply with explicit recipients only.

Use this ONLY when you're confident reply-all is wrong (e.g. private
reply to one person on a list). The verb is intentionally long to nudge
toward 'reply-all' for the common case.

--to is required. --cc / --bcc are caller-only — they are NOT added to
a server-derived recipient set. Subject + threading + quoting work the
same as reply-all.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			messageID := args[0]
			if toAddress == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("--to is required for reply-not-all-with-custom-recipients (use 'reply-all' if you don't have an explicit recipient list)"))
			}
			text, htmlBody, err := bf.resolve()
			if err != nil {
				return err
			}

			cli := newAPIClient()
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
				"to":                     toAddress,
				"subject":                subject,
				"in_reply_to_message_id": messageID,
				"reply_mode":             "sender_only",
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
	c.Flags().StringVar(&toAddress, "to", "", "primary recipient — REQUIRED (no auto-derivation)")
	c.Flags().StringVar(&subjectOverride, "subject-override", "", "replace the derived 'Re: <orig>' subject")
	c.Flags().StringSliceVar(&cc, "cc", nil, "cc recipients (caller-only; nothing is auto-added)")
	c.Flags().StringSliceVar(&bcc, "bcc", nil, "bcc recipients")
	c.Flags().BoolVar(&noQuote, "no-quote", false, "send body alone without quoting the original")
	bindBodyFlags(c, &bf)
	bindMutationFlags(c, &mf)
	return c
}
