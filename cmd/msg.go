package cmd

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/spf13/cobra"
)

// `mmb msg show <id>` → GET /msg/{id}.
//
// Fetching marks the message read by default. `--peek` opts out and
// leaves `read_at` untouched — useful for inspection while preserving
// unread queue semantics.
func newMsgCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "msg",
		Short: "Inspect a single message by id",
	}
	c.AddCommand(newMsgShowCmd())
	return c
}

func newMsgShowCmd() *cobra.Command {
	var peek bool
	c := &cobra.Command{
		Use:   "show <id>",
		Short: "Show the full sanitized message JSON for <id>",
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
