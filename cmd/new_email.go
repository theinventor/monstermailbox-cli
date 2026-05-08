package cmd

import (
	"github.com/spf13/cobra"
)

// `mmb new-email` is the deprecated v0.6 spelling. Hidden alias for
// `mmb new-message`. Remove in v0.8.
func newNewEmailAliasCmd() *cobra.Command {
	var to, subject string
	var cc, bcc []string
	var mf mutationFlags
	var bf bodyFlags
	c := &cobra.Command{
		Use:        "new-email",
		Short:      "(deprecated alias for `new-message`)",
		Hidden:     true,
		Deprecated: "use `mmb new-message` instead",
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
