// Package cmd hangs the cobra command tree for `mmb-bridge`.
//
// Subcommands:
//
//   init           — redeem an enrollment token, write config.json,
//                    register Pub/Sub watch on the user's GCP topic.
//   start          — run the daemon (foreground or detached).
//   status         — pid, uptime, last forward, policy version.
//   stop           — SIGTERM the running daemon, drop the pid file.
//   logs           — tail ~/.mmb-bridge/bridge.log (with -f to follow).
//   whitelist      — local-only whitelist read/write (used in --local-only mode).
//   rotate-key     — call POST /bridge/rotate, persist the new key.
package cmd

import (
	"github.com/spf13/cobra"
)

// Root returns the populated `mmb-bridge` cobra root command.
//
// Exposed so test code can drive the tree directly without forking
// the binary.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "mmb-bridge",
		Short: "Local Gmail → monstermailbox bridge daemon",
		Long: `mmb-bridge runs on your laptop, watches your personal Gmail
via gogcli, applies a whitelist, and forwards matched messages
to your monstermailbox tenant.

The bridge daemon NEVER sees your agent's primary API key — only
a bridge-scoped key issued at 'mmb-bridge init'.`,
		SilenceUsage: true,
	}
	root.AddCommand(
		newInitCmd(),
		newStartCmd(),
		newStopCmd(),
		newStatusCmd(),
		newLogsCmd(),
		newWhitelistCmd(),
		newRotateKeyCmd(),
	)
	return root
}
