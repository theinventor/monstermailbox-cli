// Package cmd holds the cobra command tree for the mmb CLI.
//
// R8.1 — every command is a thin shim over `internal/client.Client.Do`
// so the OpenAPI contract is the single source of truth. Tests in
// this package spin up an httptest.NewServer and assert that each
// command's outgoing request (method + path + auth header + body
// shape) matches the contract.
package cmd

import (
	"runtime/debug"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/client"
)

// Version is set at link-time via -ldflags "-X github.com/theinventor/monstermailbox-cli/cmd.Version=v0.x.y"
// (that's how goreleaser stamps release builds). For users who install via
// `go install github.com/theinventor/monstermailbox-cli@latest`, ldflags
// aren't applied, so init() falls back to the module version Go embeds in
// runtime/debug.BuildInfo — that's a real semver tag like "v0.2.0", which
// lets `mmb update` work instead of skipping with "dev build".
//
// Source checkouts (`go run ./` or `go build` without ldflags) report
// "(devel)" in BuildInfo, which we treat as the dev sentinel so local
// development still skips the updater.
var Version = "dev"

var rootProfile string

func init() {
	if Version != "dev" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	v := info.Main.Version
	if v == "" || v == "(devel)" {
		return
	}
	Version = v
}

// NewRootCmd builds the cobra command tree. Exposed as a constructor
// (rather than a package-level var) so tests can build a fresh tree
// per test, with their own io.Writer for stdout/stderr capture.
func NewRootCmd() *cobra.Command {
	rootProfile = ""
	root := &cobra.Command{
		Use:           "mmb",
		Short:         "monstermailbox CLI — operate an agent mailbox from the terminal",
		Long:          "mmb is the official CLI for monstermailbox. Configure with MONSTERMAILBOX_API_KEY (and optionally MONSTERMAILBOX_API_URL).",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&rootProfile, "profile", "", "saved auth profile to use for this invocation")

	root.AddCommand(newAgentContextCmd())
	root.AddCommand(newSkillCmd())
	root.AddCommand(newAuthCmd())
	root.AddCommand(newWhoamiCmd())
	root.AddCommand(newRegisterCmd())
	root.AddCommand(newUpdateCmd())
	root.AddCommand(newInboxCmd())
	root.AddCommand(newMsgCmd())
	root.AddCommand(newTestEmailCmd())
	root.AddCommand(newExpectCmd())
	root.AddCommand(newWhitelistCmd())
	root.AddCommand(newSendCmd())
	// Outbound verbs in priority order:
	//   reply-all                                 (primary; safe default)
	//   reply-not-all-with-custom-recipients      (secondary; deliberately awkward)
	//   new-email                                 (tertiary; brand-new thread)
	root.AddCommand(newReplyAllCmd())
	root.AddCommand(newReplyCustomRecipientsCmd())
	root.AddCommand(newNewEmailCmd())
	// Hidden deprecated alias: `reply-to-email` is the pre-v0.7
	// spelling of reply-all. Removed in a future cleanup.
	root.AddCommand(newReplyToEmailAliasCmd())
	root.AddCommand(newGuidanceCmd())
	root.AddCommand(newWebhookCmd())
	root.AddCommand(newStaffCmd())
	root.AddCommand(newContactCmd())
	root.AddCommand(newAgentProductFeedbackCmd())
	root.AddCommand(newQuarantineCmd())
	root.AddCommand(newFeedbackCmd())

	return root
}

func newAPIClient() *client.Client {
	return client.NewWithProfile(rootProfile)
}
