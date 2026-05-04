package cmd

import (
	"fmt"
	"net/http"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/spf13/cobra"
)

// `mmb msg show <id>` → GET /msg/{id}.
func newMsgCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "msg",
		Short: "Inspect a single message by id",
	}
	c.AddCommand(newMsgShowCmd())
	return c
}

func newMsgShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show the full sanitized message JSON for <id>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := client.New()
			resp, err := cli.Do(http.MethodGet, "/msg/"+args[0], nil, nil)
			if err != nil {
				return fmt.Errorf("GET /msg/%s: %w", args[0], err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
}
