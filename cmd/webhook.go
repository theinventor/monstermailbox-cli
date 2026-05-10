package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/enums"
)

// `mmb webhook` — per-agent outbound webhooks.
//
// Webhooks deliver agent events (mail received, mail sent, outbound
// scan outcome, etc.) to a URL you control. The plaintext signing
// secret is returned ONCE on create — receivers verify
// `X-MMB-Signature: sha256=<hex>` against
// `HMAC-SHA256(secret, "<X-MMB-Timestamp>.<body>")`, then de-dup
// retries by `event_id`.
//
// Operates against the agent the calling API key authenticates as.
// There is no `--agent` flag — the key already says which agent.
//
// Owner-track webhooks (operator alerts) are NOT manageable here;
// they live exclusively in the dashboard at
// /dashboard/account/webhooks for the human owner.
//
// Verbs follow principle 6: list / get / create / update / delete.
// Test fire is a nested resource `test-fire <id>` (the server-side
// route is POST /webhooks/:id/test_deliveries). Recent deliveries
// surface via `deliveries <id>`.
func newWebhookCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "webhook",
		Short: "Manage this agent's webhooks (mail-flow events delivered to a URL you control)",
	}
	c.AddCommand(newWebhookListCmd())
	c.AddCommand(newWebhookGetCmd())
	c.AddCommand(newWebhookCreateCmd())
	c.AddCommand(newWebhookUpdateCmd())
	c.AddCommand(newWebhookDeleteCmd())
	c.AddCommand(newWebhookTestCmd())
	c.AddCommand(newWebhookDeliveriesCmd())
	c.AddCommand(newWebhookEventsCmd())
	return c
}

func newWebhookListCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "list",
		Short: "List this agent's webhooks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli := client.New()
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", fmt.Sprintf("%d", limit))
			}
			resp, err := cli.Do(http.MethodGet, "/webhooks", nil, q)
			if err != nil {
				return fmt.Errorf("GET /webhooks: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = server default)")
	return c
}

func newWebhookGetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "get <id>",
		Short: "Get one webhook by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := client.New()
			resp, err := cli.Do(http.MethodGet, "/webhooks/"+args[0], nil, nil)
			if err != nil {
				return fmt.Errorf("GET /webhooks/%s: %w", args[0], err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	return c
}

// webhookMutationFlags carries the create/update flag schema in one
// place so the two commands can't drift.
type webhookMutationFlags struct {
	Name      string
	URL       string
	Events    []string
	AllEvents bool
	Active    bool
	activeSet bool
}

func bindWebhookContentFlags(c *cobra.Command, f *webhookMutationFlags, isUpdate bool) {
	c.Flags().StringVar(&f.Name, "name", "", "human label for the webhook")
	c.Flags().StringVar(&f.URL, "url", "", "https URL the events POST to")
	c.Flags().StringSliceVar(&f.Events, "event", nil, "event to subscribe to (repeatable). Use --all-events to subscribe to every agent event including future additions.")
	c.Flags().BoolVar(&f.AllEvents, "all-events", false, "subscribe to all agent-audience events, present and future")
	if isUpdate {
		c.Flags().BoolVar(&f.Active, "active", true, "set active=true (use --active=false to pause)")
		c.PreRunE = func(cmd *cobra.Command, _ []string) error {
			f.activeSet = cmd.Flags().Changed("active")
			return nil
		}
	}
}

func validateWebhookEvents(events []string) error {
	if len(events) == 0 {
		return nil
	}
	if len(events) == 1 && events[0] == enums.WebhookWildcard {
		return nil
	}
	for _, e := range events {
		if e == enums.WebhookWildcard {
			return fmt.Errorf("--event cannot mix %q with explicit event names", enums.WebhookWildcard)
		}
		if err := enums.Validate("event", e, enums.WebhookAgentEvents); err != nil {
			return err
		}
	}
	return nil
}

func newWebhookCreateCmd() *cobra.Command {
	var mf mutationFlags
	var wf webhookMutationFlags
	c := &cobra.Command{
		Use:   "create",
		Short: "Create a webhook. Plaintext signing secret is returned ONCE in the response.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if wf.Name == "" {
				return fmt.Errorf("--name is required")
			}
			if wf.URL == "" {
				return fmt.Errorf("--url is required")
			}
			events := normalizeWebhookEvents(wf.Events, wf.AllEvents)
			if len(events) == 0 {
				return fmt.Errorf("specify at least one --event, or pass --all-events")
			}
			if err := validateWebhookEvents(events); err != nil {
				return err
			}

			body := map[string]any{
				"name":   wf.Name,
				"url":    wf.URL,
				"events": events,
			}
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPost, "/webhooks", body, mf))
			}
			cli := client.New()
			resp, err := cli.DoWithHeaders(http.MethodPost, "/webhooks", body, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("POST /webhooks: %w", err)
			}
			defer resp.Body.Close()
			if err := passthroughJSON(cmd.OutOrStdout(), resp); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(),
				"hint: copy the `secret` field above. It will not be retrievable again.")
			return nil
		},
	}
	bindMutationFlags(c, &mf)
	bindWebhookContentFlags(c, &wf, false)
	return c
}

func newWebhookUpdateCmd() *cobra.Command {
	var mf mutationFlags
	var wf webhookMutationFlags
	c := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a webhook (name / url / events / active)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if wf.Name != "" {
				body["name"] = wf.Name
			}
			if wf.URL != "" {
				body["url"] = wf.URL
			}
			if wf.AllEvents || len(wf.Events) > 0 {
				events := normalizeWebhookEvents(wf.Events, wf.AllEvents)
				if err := validateWebhookEvents(events); err != nil {
					return err
				}
				body["events"] = events
			}
			if wf.activeSet {
				body["active"] = wf.Active
			}
			if len(body) == 0 {
				return fmt.Errorf("nothing to update — set at least one of --name / --url / --event / --all-events / --active")
			}
			path := "/webhooks/" + args[0]
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPatch, path, body, mf))
			}
			cli := client.New()
			resp, err := cli.DoWithHeaders(http.MethodPatch, path, body, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("PATCH %s: %w", path, err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	bindMutationFlags(c, &mf)
	bindWebhookContentFlags(c, &wf, true)
	return c
}

func newWebhookDeleteCmd() *cobra.Command {
	var mf mutationFlags
	var force bool
	c := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a webhook (permanent — requires --force)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return fmt.Errorf("--force is required to delete a webhook")
			}
			path := "/webhooks/" + args[0]
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodDelete, path, nil, mf))
			}
			cli := client.New()
			resp, err := cli.DoWithHeaders(http.MethodDelete, path, nil, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("DELETE %s: %w", path, err)
			}
			defer resp.Body.Close()
			fmt.Fprintln(cmd.OutOrStdout(), `{"deleted":true}`)
			return nil
		},
	}
	bindMutationFlags(c, &mf)
	c.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return c
}

func newWebhookTestCmd() *cobra.Command {
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "test <id>",
		Short: "Fire a synthetic test event through this webhook",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/webhooks/" + args[0] + "/test_deliveries"
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPost, path, nil, mf))
			}
			cli := client.New()
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

func newWebhookDeliveriesCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "deliveries <id>",
		Short: "List recent delivery attempts for a webhook",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := client.New()
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", fmt.Sprintf("%d", limit))
			}
			path := "/webhooks/" + args[0] + "/deliveries"
			resp, err := cli.Do(http.MethodGet, path, nil, q)
			if err != nil {
				return fmt.Errorf("GET %s: %w", path, err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = server default; capped at 100)")
	return c
}

func newWebhookEventsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "events",
		Short: "Catalog of agent-audience webhook events available to subscribe to",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli := client.New()
			resp, err := cli.Do(http.MethodGet, "/webhook_events", nil, nil)
			if err != nil {
				return fmt.Errorf("GET /webhook_events: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	return c
}

func normalizeWebhookEvents(events []string, allEvents bool) []string {
	if allEvents {
		return []string{enums.WebhookWildcard}
	}
	out := make([]string, 0, len(events))
	for _, e := range events {
		e = strings.TrimSpace(e)
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

