package cmd

import (
	"github.com/spf13/cobra"
)

// `mmb new-email --to <addr> --subject <s> --body <s>` → POST /send.
//
// Tertiary verb (after `reply-all` and
// `reply-not-all-with-custom-recipients`). Use this when you're
// starting a brand-new outbound thread with no reply context. If
// you're replying to an inbound, prefer one of the reply verbs so
// threading headers stitch correctly.
//
// Body forms (at least one required):
//   --body            inline plain text
//   --body-html       inline HTML
//   --body-file       plain text from file (HTML doesn't shell-escape well)
//   --body-html-file  HTML from file
//
// History: v0.7 briefly renamed this to `new-message`. Reverted in
// v0.8 — `new-email` is the canonical name; `new-message` stays as a
// hidden deprecated alias for one release.
func newNewEmailCmd() *cobra.Command {
	var to, subject string
	var cc, bcc []string
	var mf mutationFlags
	var bf bodyFlags
	c := &cobra.Command{
		Use:   "new-email",
		Short: "Send a brand-new outbound thread (no reply context)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runNewMessage(cmd, to, subject, cc, bcc, &bf, mf)
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
