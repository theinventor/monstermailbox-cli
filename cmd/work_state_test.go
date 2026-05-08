// End-to-end tests for the work_state command surface:
//
//   - canonical:      mmb msg update <id> --work-state <state> [...]
//   - sugar wrappers: mmb msg claim/done/skip/block/defer/awaiting-reply/reopen
//   - inbox filter:   mmb inbox list --work-state <state>
//
// Each test spins up an httptest.NewServer (via runCmd from cmd_test.go),
// runs the CLI, and asserts the outgoing request method + path + body
// shape match the openapi.yaml contract — the same shape contract the
// Rails-side request specs already pin from the server end. Together,
// they hold both ends of the wire.
package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// ── Canonical: mmb msg update <id> --work-state ────────────────────

func TestMsgUpdatePatchesWorkStateEndpointWithStateBody(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"msg", "update", "msg_1", "--work-state", "done", "--note", "replied OK"},
		200, `{"id":"msg_1","work_state":"done"}`)
	if err != nil {
		t.Fatalf("msg update returned error: %v", err)
	}
	if cap.method != http.MethodPatch || cap.path != "/msg/msg_1/work_state" {
		t.Errorf("expected PATCH /msg/msg_1/work_state; got %s %s", cap.method, cap.path)
	}
	if cap.contentType != "application/json" {
		t.Errorf("expected JSON Content-Type; got: %q", cap.contentType)
	}

	body := decodeBody(t, cap.body)
	if body["state"] != "done" {
		t.Errorf("body.state MUST be 'done'; got: %v", body["state"])
	}
	if body["note"] != "replied OK" {
		t.Errorf("body.note MUST carry --note; got: %v", body["note"])
	}
}

func TestMsgUpdateRequiresWorkStateFlag(t *testing.T) {
	_, _, err := runCmd(t, []string{"msg", "update", "msg_1"}, 200, `{}`)
	if err == nil {
		t.Fatalf("msg update without --work-state MUST error; got nil")
	}
	// Principle 3: the error must teach by naming the valid set so the
	// agent can self-correct without reading --help.
	if !strings.Contains(err.Error(), "inbox") || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error MUST enumerate valid work_states; got: %v", err)
	}
}

func TestMsgUpdateRejectsInvalidWorkStateAndNamesValidSet(t *testing.T) {
	_, _, err := runCmd(t, []string{"msg", "update", "msg_1", "--work-state", "secret"}, 200, `{}`)
	if err == nil {
		t.Fatalf("msg update with invalid --work-state MUST error; got nil")
	}
	for _, w := range []string{"inbox", "in_progress", "done", "blocked"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error MUST enumerate %q; got: %v", w, err)
		}
	}
}

func TestMsgUpdateForwardsExpectedCurrentStateAndClaimedBy(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{
			"msg", "update", "msg_1",
			"--work-state", "in_progress",
			"--expected-current-state", "inbox",
			"--claimed-by", "loop-7",
			"--note", "starting",
		},
		200, `{"id":"msg_1"}`)
	if err != nil {
		t.Fatalf("msg update returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["expected_current_state"] != "inbox" {
		t.Errorf("body.expected_current_state MUST be carried; got: %v", body["expected_current_state"])
	}
	if body["claimed_by"] != "loop-7" {
		t.Errorf("body.claimed_by MUST be carried; got: %v", body["claimed_by"])
	}
}

func TestMsgUpdateDryRunShortCircuitsBeforeHTTPCall(t *testing.T) {
	stdout, cap, err := runCmd(t,
		[]string{"msg", "update", "msg_1", "--work-state", "done", "--dry-run"},
		200, `should-never-fire`)
	if err != nil {
		t.Fatalf("msg update --dry-run returned error: %v", err)
	}
	if cap.hits != 0 {
		t.Errorf("--dry-run MUST NOT fire an HTTP request; got %d hits", cap.hits)
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(stdout), &env); jerr != nil {
		t.Fatalf("dry-run stdout MUST be JSON; got: %q", stdout)
	}
	if env["dry_run"] != true {
		t.Errorf("dry_run flag MUST be true; got: %v", env["dry_run"])
	}
	if env["method"] != "PATCH" {
		t.Errorf("method MUST be PATCH; got: %v", env["method"])
	}
	if env["path"] != "/msg/msg_1/work_state" {
		t.Errorf("path MUST be /msg/msg_1/work_state; got: %v", env["path"])
	}
}

func TestMsgUpdateSendsIdempotencyKeyHeaderWhenProvided(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{
			"msg", "update", "msg_1",
			"--work-state", "done",
			"--idempotency-key", "ik-12345",
		},
		200, `{}`)
	if err != nil {
		t.Fatalf("msg update returned error: %v", err)
	}
	if cap.idempotencyKey != "ik-12345" {
		t.Errorf("Idempotency-Key header MUST carry --idempotency-key; got: %q", cap.idempotencyKey)
	}
}

// ── Sugar: mmb msg claim ───────────────────────────────────────────

func TestMsgClaimPatchesWorkStateWithExpectedInboxAndStateInProgress(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"msg", "claim", "msg_1", "--claimed-by", "loop-1"},
		200, `{"id":"msg_1","work_state":"in_progress"}`)
	if err != nil {
		t.Fatalf("msg claim returned error: %v", err)
	}
	if cap.method != http.MethodPatch || cap.path != "/msg/msg_1/work_state" {
		t.Errorf("expected PATCH /msg/msg_1/work_state; got %s %s", cap.method, cap.path)
	}
	body := decodeBody(t, cap.body)
	if body["state"] != "in_progress" {
		t.Errorf("claim MUST send state=in_progress; got: %v", body["state"])
	}
	if body["expected_current_state"] != "inbox" {
		t.Errorf("claim MUST send expected_current_state=inbox for the race-free path; got: %v", body["expected_current_state"])
	}
	if body["claimed_by"] != "loop-1" {
		t.Errorf("claim MUST forward --claimed-by; got: %v", body["claimed_by"])
	}
}

func TestMsgClaimSurfaces409StateConflict(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"msg", "claim", "msg_1"},
		409, `{"error":"state_conflict","current":"in_progress"}`)
	if err == nil {
		t.Fatalf("409 from server MUST yield a non-zero exit; got nil error")
	}
}

// ── Sugar: mmb msg done ────────────────────────────────────────────

func TestMsgDonePatchesWithStateDone(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"msg", "done", "msg_1", "--note", "replied"},
		200, `{}`)
	if err != nil {
		t.Fatalf("msg done returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["state"] != "done" {
		t.Errorf("done MUST send state=done; got: %v", body["state"])
	}
	if body["note"] != "replied" {
		t.Errorf("done MUST forward --note; got: %v", body["note"])
	}
	// Sugar verbs other than `claim` MUST NOT default-set
	// expected_current_state — server's transition table is the single
	// source of truth for whether the move is legal.
	if _, present := body["expected_current_state"]; present {
		t.Errorf("done MUST NOT auto-attach expected_current_state; got: %v", body["expected_current_state"])
	}
}

// ── Sugar: mmb msg skip ────────────────────────────────────────────

func TestMsgSkipPatchesWithStateSkippedAndUsesReasonAsNote(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"msg", "skip", "msg_1", "--reason", "off-topic refund question"},
		200, `{}`)
	if err != nil {
		t.Fatalf("msg skip returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["state"] != "skipped" {
		t.Errorf("skip MUST send state=skipped; got: %v", body["state"])
	}
	if body["note"] != "off-topic refund question" {
		t.Errorf("skip MUST collapse --reason onto body.note; got: %v", body["note"])
	}
}

func TestMsgSkipReasonTakesPrecedenceOverNote(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"msg", "skip", "msg_1", "--note", "fallback", "--reason", "winner"},
		200, `{}`)
	if err != nil {
		t.Fatalf("msg skip returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["note"] != "winner" {
		t.Errorf("--reason MUST win over --note when both supplied; got: %v", body["note"])
	}
}

// ── Sugar: mmb msg block ───────────────────────────────────────────

func TestMsgBlockPatchesWithStateBlocked(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"msg", "block", "msg_1", "--note", "needs owner to confirm wire"},
		200, `{}`)
	if err != nil {
		t.Fatalf("msg block returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["state"] != "blocked" {
		t.Errorf("block MUST send state=blocked; got: %v", body["state"])
	}
	if body["note"] != "needs owner to confirm wire" {
		t.Errorf("block MUST forward --note; got: %v", body["note"])
	}
}

// ── Sugar: mmb msg defer ───────────────────────────────────────────

func TestMsgDeferPatchesWithStateDeferred(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"msg", "defer", "msg_1", "--note", "circle back tomorrow"},
		200, `{}`)
	if err != nil {
		t.Fatalf("msg defer returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["state"] != "deferred" {
		t.Errorf("defer MUST send state=deferred; got: %v", body["state"])
	}
}

// ── Sugar: mmb msg awaiting-reply ──────────────────────────────────

func TestMsgAwaitingReplyPatchesWithStateAwaitingReply(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"msg", "awaiting-reply", "msg_1", "--note", "asked Alice"},
		200, `{}`)
	if err != nil {
		t.Fatalf("msg awaiting-reply returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["state"] != "awaiting_reply" {
		t.Errorf("awaiting-reply MUST send state=awaiting_reply (underscore in JSON, hyphen in CLI); got: %v", body["state"])
	}
}

// ── Sugar: mmb msg reopen ──────────────────────────────────────────

func TestMsgReopenPatchesWithStateInbox(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"msg", "reopen", "msg_1", "--note", "actually mine"},
		200, `{}`)
	if err != nil {
		t.Fatalf("msg reopen returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["state"] != "inbox" {
		t.Errorf("reopen MUST send state=inbox; got: %v", body["state"])
	}
}

// ── inbox list --work-state passthrough ────────────────────────────

func TestInboxListWorkStateFlagPropagatesToQueryString(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"inbox", "list", "--work-state", "blocked"},
		200, `{"messages":[],"meta":{"showing":{"state":"trusted","unread":true,"work_state":"blocked"},"returned":0,"counts":{},"work_state_counts":{}}}`)
	if err != nil {
		t.Fatalf("inbox list --work-state returned error: %v", err)
	}
	if !strings.Contains(cap.rawQuery, "work_state=blocked") {
		t.Errorf("--work-state MUST land in query string as work_state=...; got: %q", cap.rawQuery)
	}
}

func TestInboxListRejectsInvalidWorkState(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"inbox", "list", "--work-state", "secret"},
		200, `{}`)
	if err == nil {
		t.Fatalf("inbox list with invalid --work-state MUST error; got nil")
	}
	for _, w := range []string{"inbox", "blocked"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error MUST enumerate %q; got: %v", w, err)
		}
	}
}

func TestInboxListHintRendersWorkStateBreakdownToStderr(t *testing.T) {
	body := `{"messages":[{"id":"msg_1"}],"meta":{"showing":{"state":"trusted","unread":true,"peek":false},"returned":1,"counts":{"trusted":{"unread":1,"total":3},"quarantined":{"unread":0,"total":0},"rejected":{"unread":0,"total":0}},"work_state_counts":{"inbox":1,"in_progress":1,"blocked":2,"done":0}}}`
	stdout, stderr, _, err := runCmdSplit(t,
		[]string{"inbox", "list"},
		200, body)
	if err != nil {
		t.Fatalf("inbox list returned error: %v", err)
	}
	if !strings.Contains(stdout, `"messages"`) {
		t.Errorf("stdout MUST carry the JSON body so |jq still works; got: %q", stdout)
	}
	if !strings.Contains(stderr, "inbox 1") {
		t.Errorf("stderr hint MUST mention the inbox bucket; got: %q", stderr)
	}
	if !strings.Contains(stderr, "blocked 2") {
		t.Errorf("stderr hint MUST mention non-zero blocked count; got: %q", stderr)
	}
	if strings.Contains(stderr, "done 0") {
		t.Errorf("stderr hint MUST suppress zero buckets to keep noise down; got: %q", stderr)
	}
}

// ── agent-context introspection ────────────────────────────────────

func TestAgentContextEnumeratesWorkStates(t *testing.T) {
	stdout, _, err := runCmd(t,
		[]string{"agent-context"},
		200, `{}`)
	if err != nil {
		t.Fatalf("agent-context returned error: %v", err)
	}
	var ctx struct {
		Enums map[string][]string `json:"enums"`
	}
	if jerr := json.Unmarshal([]byte(stdout), &ctx); jerr != nil {
		t.Fatalf("agent-context stdout MUST be JSON; got: %q (err: %v)", stdout, jerr)
	}
	got, ok := ctx.Enums["work_state"]
	if !ok {
		keys := make([]string, 0, len(ctx.Enums))
		for k := range ctx.Enums {
			keys = append(keys, k)
		}
		t.Fatalf("agent-context.enums MUST include work_state for principle-7 introspection; got keys: %v", keys)
	}
	want := []string{"inbox", "in_progress", "awaiting_reply", "done", "skipped", "blocked", "deferred"}
	if len(got) != len(want) {
		t.Errorf("work_state enum MUST have %d entries; got %d (%v)", len(want), len(got), got)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("work_state enum MUST include %q; got: %v", w, got)
		}
	}
}

// decodeBody parses the captured request body as JSON, calling
// t.Fatalf on parse failure with the raw bytes so a malformed body
// fails loudly rather than yielding an empty map and silent assertion
// passes.
func decodeBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	if len(raw) == 0 {
		t.Fatalf("request body was empty; expected JSON")
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body MUST be valid JSON; got: %s (err: %v)", raw, err)
	}
	return body
}

