package cmd

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/spf13/cobra"
)

// `mmb msg get <id>` → GET /msg/{id}.
//
// Fetching marks the message read by default. `--peek` opts out and
// leaves `read_at` untouched — useful for inspection while preserving
// unread queue semantics.
//
// Verb choice: principle 6 (cross-CLI vocabulary) — `get` is the
// dominant convention for "fetch one by id" across CLI ecosystems.
// `mmb msg show` is preserved as a hidden alias for one release so
// scripts written against the old name keep working; remove in v0.4.
func newMsgCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "msg",
		Short: "Inspect a single message by id",
	}
	c.AddCommand(newMsgGetCmd())
	c.AddCommand(newMsgShowAliasCmd())
	return c
}

func newMsgGetCmd() *cobra.Command {
	var peek bool
	c := &cobra.Command{
		Use:   "get <id>",
		Short: "Get the full sanitized message JSON for <id>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := client.New()
			q := url.Values{}
			if peek {
				q.Set("peek", "true")
			}
			resp, err := cli.Do(http.MethodGet, "/msg/"+args[0], nil, q)
			if err != nil {
				return fmt.Errorf("GET /msg/%s: %w", args[0], err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().BoolVar(&peek, "peek", false, "do NOT mark the message as read")
	return c
}

// newMsgShowAliasCmd is the deprecated v0.2 spelling. Hidden from
// --help so agents discover only the canonical `msg get`.
func newMsgShowAliasCmd() *cobra.Command {
	var peek bool
	c := &cobra.Command{
		Use:        "show <id>",
		Short:      "(deprecated alias for `msg get`)",
		Args:       cobra.ExactArgs(1),
		Hidden:     true,
		Deprecated: "use `mmb msg get` instead",
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := client.New()
			q := url.Values{}
			if peek {
				q.Set("peek", "true")
			}
			resp, err := cli.Do(http.MethodGet, "/msg/"+args[0], nil, q)
			if err != nil {
				return fmt.Errorf("GET /msg/%s: %w", args[0], err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().BoolVar(&peek, "peek", false, "do NOT mark the message as read")
	return c
}
