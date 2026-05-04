package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/policy"
)

// newWhitelistCmd manages the LOCAL whitelist used in --local-only
// mode. In dashboard-synced mode the source of truth is the server,
// edited via the dashboard's Policy → Whitelist page; this command
// is a no-op (with a clear error) in that mode.
func newWhitelistCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "whitelist",
		Short: "Local-only whitelist commands (use the dashboard for synced mode)",
	}
	c.AddCommand(
		&cobra.Command{
			Use:   "add <pattern>",
			Short: "Add a sender to ~/.mmb-bridge/whitelist.json (local-only mode)",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				pattern := strings.TrimSpace(args[0])
				if pattern == "" {
					return fmt.Errorf("pattern is empty")
				}
				store, err := policy.LoadLocal()
				if err != nil {
					return err
				}
				snap, _, _ := store.Snapshot()
				for _, e := range snap.Whitelist {
					if e.Sender == pattern || e.SenderRegex == pattern {
						cmd.Printf("already present: %q\n", pattern)
						return nil
					}
				}
				snap.Whitelist = append(snap.Whitelist, api.WhitelistEntry{Sender: pattern})
				if err := policy.SaveLocal(snap.Whitelist); err != nil {
					return err
				}
				cmd.Printf("added %q\n", pattern)
				return nil
			},
		},
		&cobra.Command{
			Use:   "list",
			Short: "List entries in ~/.mmb-bridge/whitelist.json",
			RunE: func(cmd *cobra.Command, _ []string) error {
				store, err := policy.LoadLocal()
				if err != nil {
					return err
				}
				snap, _, _ := store.Snapshot()
				if len(snap.Whitelist) == 0 {
					cmd.Println("(empty)")
					return nil
				}
				for _, e := range snap.Whitelist {
					switch {
					case e.Sender != "":
						cmd.Println("sender       ", e.Sender)
					case e.SenderRegex != "":
						cmd.Println("sender_regex ", e.SenderRegex)
					}
				}
				return nil
			},
		},
	)
	return c
}
