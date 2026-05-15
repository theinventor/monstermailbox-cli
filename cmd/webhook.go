package cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"
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
		Long: `Manage this agent's webhooks.

When you don't know which events to pick, pick the preset that matches
your workflow:

  --event-preset trusted-inbox
      inbox.new only. Trusted/readable mail is ready; quarantined
      mail will not notify this webhook.

  --event-preset quarantine-aware-inbox
      inbox.new + inbox.quarantined + inbox.released. Use this when
      your integration needs to know held mail exists or when a human
      releases it. Quarantine payloads stay redacted/safe; they do not
      grant the agent access to held content.

You can also pass explicit --event values. For example, --event inbox.new
is the trusted/readable "go check the inbox" signal. It does not fire for
quarantined mail. Subscribe to additional events only when you have a
concrete reason (e.g. outbound.bounced if you need to react to
undeliverable addresses; outbound.sent for delivery confirmation UIs).

--all-events delivers EVERY event for every state change, including
events you almost certainly won't act on. Use it only if you're
building an audit/observability pipeline.

Run 'mmb webhook events' for the full catalog with per-event
recommendations.`,
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
			cli := newAPIClient()
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
			cli := newAPIClient()
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
	Name         string
	URL          string
	Events       []string
	EventPreset  string
	AllEvents    bool
	Active       bool
	Headers      []string
	AuthBearer   string
	ClearHeaders bool
	activeSet    bool
}

func bindWebhookContentFlags(c *cobra.Command, f *webhookMutationFlags, isUpdate bool) {
	c.Flags().StringVar(&f.Name, "name", "", "human label for the webhook")
	c.Flags().StringVar(&f.URL, "url", "", "https URL the events POST to")
	c.Flags().StringSliceVar(&f.Events, "event", nil, "event to subscribe to (repeatable). inbox.new means trusted/readable mail only; add inbox.quarantined or use --event-preset quarantine-aware-inbox for held-mail notifications.")
	c.Flags().StringVar(&f.EventPreset, "event-preset", "", "named event set: trusted-inbox, quarantine-aware-inbox, full-inbound-lifecycle")
	c.Flags().BoolVar(&f.AllEvents, "all-events", false, "subscribe to all agent-audience events. Only use this for audit/observability/firehose pipelines, not as the default quarantine workaround.")
	c.Flags().StringArrayVar(&f.Headers, "header", nil, "delivery header to send with each webhook POST, as 'Name: value' (repeatable)")
	c.Flags().StringVar(&f.AuthBearer, "auth-bearer", "", "set delivery Authorization: Bearer <token>")
	if isUpdate {
		c.Flags().BoolVar(&f.Active, "active", true, "set active=true (use --active=false to pause)")
		c.Flags().BoolVar(&f.ClearHeaders, "clear-headers", false, "remove all configured delivery auth headers")
		c.PreRunE = func(cmd *cobra.Command, _ []string) error {
			f.activeSet = cmd.Flags().Changed("active")
			return nil
		}
	}
}

var webhookEventPresets = map[string][]string{
	"trusted-inbox": {
		"inbox.new",
	},
	"quarantine-aware-inbox": {
		"inbox.new",
		"inbox.quarantined",
		"inbox.released",
	},
	"full-inbound-lifecycle": {
		"inbox.arriving",
		"inbox.new",
		"inbox.quarantined",
		"inbox.released",
		"inbox.rejected",
	},
}

func webhookEventPresetNames() []string {
	names := make([]string, 0, len(webhookEventPresets))
	for name := range webhookEventPresets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func expandWebhookEventPreset(name string) ([]string, error) {
	if name == "" {
		return nil, nil
	}
	events, ok := webhookEventPresets[name]
	if !ok {
		return nil, fmt.Errorf("--event-preset must be one of: %s (got: %q)", strings.Join(webhookEventPresetNames(), ", "), name)
	}
	out := make([]string, len(events))
	copy(out, events)
	return out, nil
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

func webhookHeadersFromFlags(wf webhookMutationFlags) (map[string]string, error) {
	headers := map[string]string{}
	for _, raw := range wf.Headers {
		name, value, ok := strings.Cut(raw, ":")
		if !ok {
			return nil, fmt.Errorf("--header must be in 'Name: value' form")
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name == "" {
			return nil, fmt.Errorf("--header name is required")
		}
		if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("--header cannot contain newlines")
		}
		headers[name] = value
	}
	if wf.AuthBearer != "" {
		if strings.ContainsAny(wf.AuthBearer, "\r\n") {
			return nil, fmt.Errorf("--auth-bearer cannot contain newlines")
		}
		headers["Authorization"] = "Bearer " + wf.AuthBearer
	}
	return headers, nil
}

func newWebhookCreateCmd() *cobra.Command {
	var mf mutationFlags
	var wf webhookMutationFlags
	c := &cobra.Command{
		Use:   "create",
		Short: "Create a webhook. Plaintext signing secret is returned ONCE in the response.",
		Long: `Create a webhook.

Simple trusted-inbox shape — notify only when inbound mail is readable:

  mmb webhook create \
    --name "my-receiver" \
    --url  "https://your-receiver.example.com/mmb" \
    --event-preset trusted-inbox

Quarantine-aware inbox shape — also notify when mail is held for
human review, and when a human releases it:

  mmb webhook create \
    --name "my-receiver" \
    --url  "https://your-receiver.example.com/mmb" \
    --event-preset quarantine-aware-inbox

The quarantine notification is intentionally safe/redacted; it tells
the integration that held mail exists, not that the agent can read the
held content before human release.

The response includes the plaintext signing secret EXACTLY ONCE.
Copy it now — the server bcrypts it; you cannot retrieve it later.

Receivers verify each request by:
  expected = "sha256=" + hmac_sha256(secret, "<X-MMB-Timestamp>.<body>")
  hmac.compare_digest(expected, "<X-MMB-Signature>")
…and rejecting requests where |now - timestamp| > 300s.

For other event types, run 'mmb webhook events' first. Use --all-events
only for audit/observability/firehose receivers.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if wf.Name == "" {
				return fmt.Errorf("--name is required")
			}
			if wf.URL == "" {
				return fmt.Errorf("--url is required")
			}
			events, err := normalizeWebhookEvents(wf)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				return fmt.Errorf("specify at least one --event, pass --event-preset, or pass --all-events")
			}
			if err := validateWebhookEvents(events); err != nil {
				return err
			}
			headers, err := webhookHeadersFromFlags(wf)
			if err != nil {
				return err
			}

			body := map[string]any{
				"name":   wf.Name,
				"url":    wf.URL,
				"events": events,
			}
			if len(headers) > 0 {
				body["headers"] = headers
			}
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPost, "/webhooks", body, mf))
			}
			cli := newAPIClient()
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
			if wf.AllEvents || wf.EventPreset != "" || len(wf.Events) > 0 {
				events, err := normalizeWebhookEvents(wf)
				if err != nil {
					return err
				}
				if err := validateWebhookEvents(events); err != nil {
					return err
				}
				body["events"] = events
			}
			if wf.activeSet {
				body["active"] = wf.Active
			}
			if wf.ClearHeaders && (len(wf.Headers) > 0 || wf.AuthBearer != "") {
				return fmt.Errorf("--clear-headers cannot be combined with --header or --auth-bearer")
			}
			if wf.ClearHeaders {
				body["headers"] = map[string]string{}
			} else if len(wf.Headers) > 0 || wf.AuthBearer != "" {
				headers, err := webhookHeadersFromFlags(wf)
				if err != nil {
					return err
				}
				body["headers"] = headers
			}
			if len(body) == 0 {
				return fmt.Errorf("nothing to update — set at least one of --name / --url / --event / --event-preset / --all-events / --active / --header / --auth-bearer / --clear-headers")
			}
			path := "/webhooks/" + args[0]
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPatch, path, body, mf))
			}
			cli := newAPIClient()
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
			cli := newAPIClient()
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

func newWebhookDeliveriesCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "deliveries <id>",
		Short: "List recent delivery attempts for a webhook",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := newAPIClient()
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
		Short: "Catalog of agent-audience webhook events (with descriptions + recommendations)",
		Long: `List every event type you can subscribe to, with a description and
"recommended_for" label.

When in doubt, subscribe to ` + "`inbox.new`" + ` only — that's the
"trusted/readable inbound mail is ready" signal. It does not notify
on quarantined mail. Use the quarantine-aware inbox preset when your
receiver needs held-mail notifications.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli := newAPIClient()
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

func normalizeWebhookEvents(wf webhookMutationFlags) ([]string, error) {
	eventSources := 0
	if wf.AllEvents {
		eventSources++
	}
	if wf.EventPreset != "" {
		eventSources++
	}
	if len(wf.Events) > 0 {
		eventSources++
	}
	if eventSources > 1 {
		return nil, fmt.Errorf("--event, --event-preset, and --all-events are mutually exclusive")
	}
	if wf.AllEvents {
		return []string{enums.WebhookWildcard}, nil
	}
	if wf.EventPreset != "" {
		return expandWebhookEventPreset(wf.EventPreset)
	}
	out := make([]string, 0, len(wf.Events))
	for _, e := range wf.Events {
		e = strings.TrimSpace(e)
		if e != "" {
			out = append(out, e)
		}
	}
	return out, nil
}
