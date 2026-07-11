package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/theinventor/monstermailbox-cli/internal/client"
)

// disableConflictingInboxWebhooks pauses any ACTIVE webhook subscribed to
// inbox.new (or all-events). Once the SSE plugin is installed it owns inbound
// delivery; a leftover inbox.new webhook (from a pre-plugin setup) then
// double-delivers every email — once via the webhook, once via the plugin —
// which surfaces as duplicate replies seconds apart. Webhooks subscribed only to
// other events (e.g. outbound.bounced to a monitor) are never touched.
// Best-effort: any failure is reported as a hint and never fails the install.
func disableConflictingInboxWebhooks(w io.Writer, cli *client.Client) {
	resp, err := cli.Do(http.MethodGet, "/webhooks", nil, nil)
	if err != nil {
		fmt.Fprintf(w, "⚠ couldn't check for a conflicting inbox webhook (%v)\n"+
			"    if you set one up before installing the plugin, disable it: mmb webhook update <id> --active=false\n", err)
		return
	}
	var payload struct {
		Webhooks []map[string]any `json:"webhooks"`
	}
	decErr := json.NewDecoder(resp.Body).Decode(&payload)
	resp.Body.Close()
	if decErr != nil {
		return
	}
	for _, wh := range payload.Webhooks {
		// Reuse the agent-setup predicates: active + subscribed to inbox.new
		// (webhookHasInboxNew also treats "*"/all-events as a match).
		if !webhookActive(wh) || !webhookHasInboxNew(wh) {
			continue
		}
		id := webhookIDString(wh)
		name, _ := wh["name"].(string)
		pr, perr := cli.DoWithHeaders(http.MethodPatch, "/webhooks/"+id,
			map[string]any{"active": false}, nil, nil)
		if perr != nil {
			fmt.Fprintf(w, "⚠ found a redundant inbox.new webhook (id %s, %q) but couldn't disable it: %v\n"+
				"    disable it manually so inbound isn't delivered twice: mmb webhook update %s --active=false\n",
				id, name, perr, id)
			continue
		}
		pr.Body.Close()
		fmt.Fprintf(w, "✓ disabled redundant inbox.new webhook (id %s, %q) — the plugin now delivers inbound;\n"+
			"    running both delivered every email twice. Re-enable with: mmb webhook update %s --active=true\n",
			id, name, id)
	}
}

// webhookIDString extracts the id from a decoded webhook, tolerating both the
// string and JSON-number encodings.
func webhookIDString(wh map[string]any) string {
	switch v := wh["id"].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%d", int64(v))
	default:
		return fmt.Sprintf("%v", wh["id"])
	}
}
