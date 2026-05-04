// Package cmd holds the cobra command tree for the mmb CLI.
//
// R8.1 — every command is a thin shim over `internal/client.Client.Do`
// so the OpenAPI contract is the single source of truth. Tests in
// this package spin up an httptest.NewServer and assert that each
// command's outgoing request (method + path + auth header + body
// shape) matches the contract.
package cmd

import (
	"github.com/spf13/cobra"
)

// Version is set at link-time via -ldflags "-X github.com/theinventor/monstermailbox-cli/cmd.Version=v0.x.y"
var Version = "dev"

// NewRootCmd builds the cobra command tree. Exposed as a constructor
// (rather than a package-level var) so tests can build a fresh tree
// per test, with their own io.Writer for stdout/stderr capture.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "mmb",
		Short:         "monstermailbox CLI — operate an agent mailbox from the terminal",
		Long:          "mmb is the official CLI for monstermailbox. Configure with MONSTERMAILBOX_API_KEY (and optionally MONSTERMAILBOX_API_URL).",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newAuthCmd())
	root.AddCommand(newWhoamiCmd())
	root.AddCommand(newRegisterCmd())
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newInboxCmd())
	root.AddCommand(newMsgCmd())
	root.AddCommand(newExpectCmd())
	root.AddCommand(newWhitelistCmd())
	root.AddCommand(newSendCmd())
	root.AddCommand(newNewEmailCmd())
	root.AddCommand(newReplyToEmailCmd())
	root.AddCommand(newQuarantineCmd())

	return root
}
