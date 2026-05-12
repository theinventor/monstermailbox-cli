package cmd

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/enums"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
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
//
// `mmb msg update`, `claim`, `done`, `skip`, `block`, `defer`,
// `awaiting-reply`, and `reopen` all hit PATCH /msg/{id}/work_state.
// `update` is the canonical Trevin-vocabulary form; the others are
// sugar wrappers each agent can type without remembering the work_state
// enum spelling. See newMsgUpdateWorkStateCmd for the shared body
// shape and newMsgWorkStateSugarCmd for how each sugar wrapper maps.
func newMsgCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "msg",
		Short: "Inspect a single message by id, or transition its work_state",
	}
	c.AddCommand(newMsgGetCmd())
	c.AddCommand(newMsgShowAliasCmd())
	c.AddCommand(newMsgUpdateWorkStateCmd())
	c.AddCommand(newMsgWorkStateSugarCmd("claim", "in_progress", "inbox",
		"Atomically claim an inbox message — first caller wins, second gets 409"))
	c.AddCommand(newMsgWorkStateSugarCmd("done", "done", "",
		"Mark the message done (work complete; safe terminal state)"))
	c.AddCommand(newMsgWorkStateSugarCmd("skip", "skipped", "",
		"Skip the message — requires --reason / --note explaining why"))
	c.AddCommand(newMsgWorkStateSugarCmd("block", "blocked", "",
		"Mark the message blocked on a human or external dependency — requires --note"))
	c.AddCommand(newMsgWorkStateSugarCmd("defer", "deferred", "",
		"Defer the message; agent will return to it later"))
	c.AddCommand(newMsgWorkStateSugarCmd("awaiting-reply", "awaiting_reply", "",
		"Mark the message as awaiting a sender response (you replied; ball is in their court)"))
	c.AddCommand(newMsgWorkStateSugarCmd("reopen", "inbox", "",
		"Reopen a terminal/long-tail message back to the inbox queue"))
	return c
}

func newMsgGetCmd() *cobra.Command {
	var peek bool
	c := &cobra.Command{
		Use:   "get <id>",
		Short: "Get the full sanitized message JSON for <id>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := newAPIClient()
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
			cli := newAPIClient()
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

// `mmb msg update <id> --work-state <state>` is the canonical
// PATCH /msg/{id}/work_state shape, per principle 6 (cross-CLI
// vocabulary: `update`). The sugar wrappers (claim/done/skip/...) all
// dispatch into this same body construction; this command is what
// agent-context surfaces as the "real" verb.
//
// Body fields:
//
//	state                  required; one of enums.WorkStates
//	expected_current_state optional; when set, server only commits if
//	                       the row's current state matches — gives
//	                       caller a race-free "I think it's X, change
//	                       it to Y" handshake.
//	note                   optional; free-text. For state=skipped this
//	                       lands as the skip reason; for state=blocked
//	                       as the blocker note.
//	claimed_by             optional; loop/worker id stamped onto the
//	                       row when state=in_progress.
//
// The endpoint is a mutation, so --idempotency-key + --dry-run are
// available via bindMutationFlags (principle 4).
func newMsgUpdateWorkStateCmd() *cobra.Command {
	var workState, expectedCurrent, note, claimedBy string
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "update <id>",
		Short: "Transition a message's agent-side work_state (PATCH /msg/{id}/work_state)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doWorkStateUpdate(cmd, args[0], workState, expectedCurrent, note, claimedBy, mf)
		},
	}
	c.Flags().StringVar(&workState, "work-state", "",
		fmt.Sprintf("required: target work_state (one of: %s)", joinEnum(enums.WorkStates)))
	c.Flags().StringVar(&expectedCurrent, "expected-current-state", "",
		fmt.Sprintf("optional: only commit if current state matches (one of: %s); 409 on mismatch", joinEnum(enums.WorkStates)))
	c.Flags().StringVar(&note, "note", "", "optional: free-text note (skip reason / block note / completion summary)")
	c.Flags().StringVar(&claimedBy, "claimed-by", "", "optional: loop/worker id stamped on the row when --work-state=in_progress")
	bindMutationFlags(c, &mf)
	return c
}

// newMsgWorkStateSugarCmd builds a thin convenience wrapper around
// the canonical update verb. The sugar commands exist because
// `mmb msg done <id> --note "replied OK"` reads better in an agent
// loop than the `update --work-state done --note ...` form, and
// `mmb msg claim <id>` is a critical race-free verb worth its own
// surface.
//
// `defaultExpected` is the only-when-non-empty
// `expected_current_state` value the wrapper enforces (currently only
// `claim` uses it — claim from anywhere other than :inbox is wrong by
// definition). Other wrappers leave it empty so the server's
// transition table is the single source of truth.
func newMsgWorkStateSugarCmd(verb, targetState, defaultExpected, summary string) *cobra.Command {
	var note, claimedBy, expectedCurrent string
	var reason string // skip-only alias
	var mf mutationFlags
	c := &cobra.Command{
		Use:   verb + " <id>",
		Short: summary,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// `skip` lets the caller pass either --reason or --note;
			// reason is the more natural verb here. We collapse the
			// two onto a single body field so the server-side handler
			// stays simple.
			effectiveNote := note
			if verb == "skip" && reason != "" {
				effectiveNote = reason
			}

			expected := expectedCurrent
			if expected == "" {
				expected = defaultExpected
			}

			return doWorkStateUpdate(cmd, args[0], targetState, expected, effectiveNote, claimedBy, mf)
		},
	}
	c.Flags().StringVar(&note, "note", "",
		"optional: free-text note recorded with the transition")
	if verb == "skip" {
		c.Flags().StringVar(&reason, "reason", "",
			"why you're skipping; takes precedence over --note for skip")
	}
	c.Flags().StringVar(&claimedBy, "claimed-by", "",
		"optional: loop/worker id (only meaningful when claiming or in_progress)")
	c.Flags().StringVar(&expectedCurrent, "expected-current-state", "",
		fmt.Sprintf("optional: only commit if current state matches (one of: %s)", joinEnum(enums.WorkStates)))
	bindMutationFlags(c, &mf)
	return c
}

// doWorkStateUpdate is the shared body for `update` + every sugar
// wrapper. Validates the enum values up front (principle 3 — error
// names the valid set), constructs the request body, and either
// dry-runs or fires the PATCH.
func doWorkStateUpdate(cmd *cobra.Command, id, workState, expected, note, claimedBy string, mf mutationFlags) error {
	if workState == "" {
		return exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("--work-state is required (one of: %s)", joinEnum(enums.WorkStates)))
	}
	if err := enums.Validate("work-state", workState, enums.WorkStates); err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}
	if err := enums.Validate("expected-current-state", expected, enums.WorkStates); err != nil {
		return exitcode.Wrap(exitcode.Usage, err)
	}

	body := map[string]any{"state": workState}
	if expected != "" {
		body["expected_current_state"] = expected
	}
	if note != "" {
		body["note"] = note
	}
	if claimedBy != "" {
		body["claimed_by"] = claimedBy
	}

	path := "/msg/" + id + "/work_state"

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
}

// joinEnum returns the comma-separated rendering used in --help text
// and Usage error messages. Pulled out so every flag's description
// uses the same spacing.
func joinEnum(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}
