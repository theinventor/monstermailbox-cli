package cmd

import (
	"github.com/spf13/cobra"
)

// `mmb reply-to-email` is the deprecated v0.6 spelling. It stays for
// one release as a hidden alias so scripts written before v0.7 keep
// working; remove in v0.8. The replacement is `mmb reply-all`, which
// uses the new server-side `reply_mode=all` to derive the full reply-
// all recipient set without forcing the CLI to know the agent's own
// address.
func newReplyToEmailAliasCmd() *cobra.Command {
	c := newReplyAllCmd()
	c.Use = "reply-to-email"
	c.Short = "(deprecated alias for `reply-all`)"
	c.Hidden = true
	c.Deprecated = "use `mmb reply-all` instead — same threading + quoting behavior, plus full reply-all recipients (the new default)"
	return c
}
