package cmd

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func newStaffCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "staff",
		Short: "Staff-only operator tools (requires a staff API key)",
		Long: `Staff-only operator tools.

These commands call /admin/api/* and require a staff bearer token, not an
agent API key. Store or pass the staff token the same way as other mmb tokens,
for example MONSTERMAILBOX_API_KEY=mmb_staff_...`,
	}
	c.AddCommand(newStaffWebhookDeliveriesCmd())
	return c
}

func newStaffWebhookDeliveriesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "webhook-deliveries",
		Short: "Inspect failed webhook deliveries across tenants",
		Long: `Inspect failed and gave-up outbound webhook deliveries across tenants.

The staff API returns safe metadata only: request payload bodies, receiver
response bodies, signing secrets, and custom delivery header values are not
printed by this CLI.`,
	}
	c.AddCommand(newStaffWebhookDeliveriesListCmd())
	c.AddCommand(newStaffWebhookDeliveriesGetCmd())
	c.AddCommand(newStaffWebhookDeliveriesRedriveCmd())
	return c
}

func newStaffWebhookDeliveriesListCmd() *cobra.Command {
	var ownerEmail, agentAddress, webhookID, event, status, cursor string
	var limit int
	var includeTests bool

	c := &cobra.Command{
		Use:   "list",
		Short: "List failed/gave_up webhook deliveries",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli := newAPIClient()
			q := url.Values{}
			setQuery(q, "owner_email", ownerEmail)
			setQuery(q, "agent_address", agentAddress)
			setQuery(q, "webhook_id", webhookID)
			setQuery(q, "event", event)
			setQuery(q, "status", status)
			setQuery(q, "cursor", cursor)
			if limit > 0 {
				q.Set("limit", fmt.Sprintf("%d", limit))
			}
			if includeTests {
				q.Set("include_tests", "1")
			}
			resp, err := cli.Do(http.MethodGet, "/admin/api/webhook_deliveries", nil, q)
			if err != nil {
				return fmt.Errorf("GET /admin/api/webhook_deliveries: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&ownerEmail, "owner-email", "", "filter by tenant owner email")
	c.Flags().StringVar(&agentAddress, "agent-address", "", "filter by agent address")
	c.Flags().StringVar(&webhookID, "webhook-id", "", "filter by webhook id")
	c.Flags().StringVar(&event, "event", "", "filter by event name")
	c.Flags().StringVar(&status, "status", "", "failed or gave_up")
	c.Flags().StringVar(&cursor, "cursor", "", "pagination cursor from prior response")
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = server default)")
	c.Flags().BoolVar(&includeTests, "include-tests", false, "include synthetic test delivery failures")
	return c
}

func newStaffWebhookDeliveriesGetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "get <delivery-id>",
		Short: "Inspect one webhook delivery's safe metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := newAPIClient()
			path := "/admin/api/webhook_deliveries/" + args[0]
			resp, err := cli.Do(http.MethodGet, path, nil, nil)
			if err != nil {
				return fmt.Errorf("GET %s: %w", path, err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	return c
}

func newStaffWebhookDeliveriesRedriveCmd() *cobra.Command {
	var confirm string
	var allowDuplicate bool
	var mf mutationFlags

	c := &cobra.Command{
		Use:   "redrive <delivery-id>",
		Short: "Redrive one failed real webhook delivery",
		Long: `Redrive one failed real webhook delivery.

The server refuses synthetic test deliveries, missing payload snapshots, inactive
webhooks, and duplicate redrives unless --allow-duplicate is explicit. The CLI
also requires --confirm <delivery-id> and --idempotency-key so retries do not
enqueue duplicate webhook posts.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if confirm != id {
				return fmt.Errorf("--confirm %s is required to redrive delivery %s", id, id)
			}
			if mf.IdempotencyKey == "" {
				return fmt.Errorf("--idempotency-key is required for redrive")
			}

			body := map[string]any{"confirm": "redrive"}
			if allowDuplicate {
				body["allow_duplicate"] = true
			}

			path := "/admin/api/webhook_deliveries/" + id + "/redrive"
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(), newDryRunEnvelope(http.MethodPost, path, body, mf))
			}

			cli := newAPIClient()
			resp, err := cli.DoWithHeaders(http.MethodPost, path, body, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("POST %s: %w", path, err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&confirm, "confirm", "", "must equal <delivery-id> to acknowledge a real replay")
	c.Flags().BoolVar(&allowDuplicate, "allow-duplicate", false, "permit another redrive when this delivery was already redriven")
	bindMutationFlags(c, &mf)
	return c
}

func setQuery(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}
