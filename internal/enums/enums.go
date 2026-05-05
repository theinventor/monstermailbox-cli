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

// DeliverSchemes is the set of --deliver targets supported across commands
// that produce artifacts. Currently a forward-looking list; principle 10
// (two-way I/O) is implemented in a later phase.
var DeliverSchemes = []string{"stdout", "file", "webhook"}

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
	"inbox_state":     InboxStates,
	"deliver_scheme":  DeliverSchemes,
}
