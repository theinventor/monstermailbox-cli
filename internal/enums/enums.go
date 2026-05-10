// Package enums is the single source of truth for enum-shaped flag values.
//
// Two consumers share these lists:
//   - per-command flag validation (so the rejection message names the valid set,
//     per principle 3 — errors that teach and enumerate)
//   - `mmb agent-context` introspection (so an agent can discover the valid set
//     from machine-readable JSON without parsing --help text)
//
// Adding a new enum-shaped flag means: define the list here, call Validate in
// the command's RunE, and add the list to the InContext map so agent-context
// surfaces it.
package enums

import (
	"fmt"
	"strings"
)

// InboxStates is the set of trust states GET /inbox accepts via ?state=.
// Mirrors the server's openapi.yaml inboxState schema.
var InboxStates = []string{"trusted", "quarantined", "rejected"}

// WorkStates is the agent-side work-tracking axis. Orthogonal to
// the trust state (`InboxStates`) and to read/unread. Mirrors
// openapi.yaml § WorkState.
//
// The agent's loop should poll `?work_state=inbox` for its real work
// queue (rather than `?unread=true`, which is a human-inbox concept)
// and commit a terminal transition every time it touches a row.
var WorkStates = []string{"inbox", "in_progress", "awaiting_reply", "done", "skipped", "blocked", "deferred"}

// DeliverSchemes is the set of --deliver targets supported across commands
// that produce artifacts. Currently a forward-looking list; principle 10
// (two-way I/O) is implemented in a later phase.
var DeliverSchemes = []string{"stdout", "file", "webhook"}

// WebhookWildcard is the special "subscribe to all events in this audience"
// value the server accepts in place of an explicit list of event names.
const WebhookWildcard = "*"

// WebhookAgentEvents is the set of agent-audience events `mmb webhook
// create --event ...` accepts. Owner-audience events (operator alerts)
// are managed only via the dashboard and are NOT in this list.
//
// Authoritative copy is server-side at GET /webhook_events (surfaced
// via `mmb webhook events`); this list lets the CLI reject typos with
// the valid set named, without a round-trip.
var WebhookAgentEvents = []string{
	"inbox.arriving",
	"inbox.new",
	"inbox.quarantined",
	"inbox.rejected",
	"inbox.released",
	"outbound.queued",
	"outbound.scanned",
	"outbound.approved",
	"outbound.sent",
	"outbound.bounced",
}

// Validate returns nil if val is in valid, otherwise an error of the form
//
//	--<flag> must be one of: a, b, c (got: "x")
//
// Empty val is treated as "not set" — caller decides whether the flag is
// required. Validate's job is range-check, not presence-check.
func Validate(flag, val string, valid []string) error {
	if val == "" {
		return nil
	}
	for _, v := range valid {
		if v == val {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of: %s (got: %q)", flag, strings.Join(valid, ", "), val)
}

// InContext is the map of enum-name → values that `mmb agent-context` emits
// under the "enums" key. Keep keys in lower_snake_case for JSON friendliness.
var InContext = map[string][]string{
	"inbox_state":         InboxStates,
	"work_state":          WorkStates,
	"deliver_scheme":      DeliverSchemes,
	"webhook_agent_event": WebhookAgentEvents,
}
