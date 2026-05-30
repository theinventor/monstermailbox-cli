package cmd

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func newTestEmailCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "test-email",
		Short: "Create safe test inbox messages",
		Long: `Create safe synthetic inbox messages that exercise the real
MonsterMailbox inbox workflow. These commands never send external SMTP or
Postmark outbound email.`,
	}
	c.AddCommand(newTestEmailSendCmd())
	return c
}

func newTestEmailSendCmd() *cobra.Command {
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "send",
		Short: "Create a fetchable test Message and emit inbox.new",
		Long: `Create a real trusted Message in this agent's inbox and ask the
server to emit the normal inbox.new webhook event with data.test=true.

The server chooses the sender, subject, and body so callers cannot spoof
arbitrary mail. The JSON response includes message_id, event_id, whether a
webhook delivery is expected, and next-step hints such as mmb msg get <id>
--peek.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			const path = "/test_emails"
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPost, path, nil, mf))
			}
			cli := newAPIClient()
			resp, err := cli.DoWithHeaders(http.MethodPost, path, nil, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("POST %s: %w", path, err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	bindMutationFlags(c, &mf)
	return c
}
