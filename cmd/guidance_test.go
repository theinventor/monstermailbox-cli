// End-to-end tests for `mmb guidance` CRUD. Each test pins request
// shape (method, path, body) against the openapi.yaml /guidance
// contract — the same shape the Rails-side request specs pin from the
// server end. Together they hold both ends of the wire.
package cmd

import (
	"net/http"
	"strings"
	"testing"
)

// ── list ───────────────────────────────────────────────────────────

func TestGuidanceListGetsGuidanceEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"guidance", "list"}, 200, `{"guidance":[]}`)
	if err != nil {
		t.Fatalf("guidance list returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/guidance" {
		t.Errorf("expected GET /guidance; got %s %s", cap.method, cap.path)
	}
}

func TestGuidanceListPropagatesLimit(t *testing.T) {
	_, cap, _ := runCmd(t, []string{"guidance", "list", "--limit", "10"}, 200, `{"guidance":[]}`)
	if !strings.Contains(cap.rawQuery, "limit=10") {
		t.Errorf("--limit MUST land in query string; got: %q", cap.rawQuery)
	}
}

// ── get ────────────────────────────────────────────────────────────

func TestGuidanceGetHitsGuidanceIdEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"guidance", "get", "g_1"}, 200, `{"id":"g_1"}`)
	if err != nil {
		t.Fatalf("guidance get returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/guidance/g_1" {
		t.Errorf("expected GET /guidance/g_1; got %s %s", cap.method, cap.path)
	}
}

func TestGuidanceGetRequiresId(t *testing.T) {
	_, _, err := runCmd(t, []string{"guidance", "get"}, 200, `{}`)
	if err == nil {
		t.Fatalf("guidance get without id MUST error")
	}
}

// ── create ─────────────────────────────────────────────────────────

func TestGuidanceCreateSendsFullPayload(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"guidance", "create",
			"--name", "ci-failures",
			"--instructions", "Find the failing test, propose a fix.",
			"--from-email", "notifications@github.com",
			"--subject-regex", "^Run failed:",
			"--body-contains", "deploy,main"},
		201, `{"id":"g_1"}`)
	if err != nil {
		t.Fatalf("guidance create returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/guidance" {
		t.Errorf("expected POST /guidance; got %s %s", cap.method, cap.path)
	}
	body := decodeBody(t, cap.body)
	if body["name"] != "ci-failures" {
		t.Errorf("body.name MUST be --name; got: %v", body["name"])
	}
	if body["from_email"] != "notifications@github.com" {
		t.Errorf("body.from_email MUST be --from-email; got: %v", body["from_email"])
	}
	if body["subject_regex"] != "^Run failed:" {
		t.Errorf("body.subject_regex MUST be --subject-regex; got: %v", body["subject_regex"])
	}
	bc, _ := body["body_contains"].([]any)
	if len(bc) != 2 {
		t.Errorf("body.body_contains MUST be a 2-element array (deploy, main); got: %v", bc)
	}
}

func TestGuidanceCreateRequiresNameAndInstructions(t *testing.T) {
	_, _, err := runCmd(t, []string{"guidance", "create", "--from-domain", "stripe.com"}, 200, `{}`)
	if err == nil {
		t.Fatalf("create without --name/--instructions MUST error")
	}
}

func TestGuidanceCreateRequiresAtLeastOneMatcher(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"guidance", "create", "--name", "x", "--instructions", "y"},
		200, `{}`)
	if err == nil {
		t.Fatalf("create with no matcher MUST error (the server would 422 anyway; CLI catches early)")
	}
	if !strings.Contains(err.Error(), "matcher") {
		t.Errorf("error MUST teach the rule; got: %v", err)
	}
}

func TestGuidanceCreateDryRunSkipsHTTP(t *testing.T) {
	stdout, cap, err := runCmd(t,
		[]string{"guidance", "create",
			"--name", "x", "--instructions", "y", "--from-domain", "stripe.com",
			"--dry-run"},
		201, `should-never-fire`)
	if err != nil {
		t.Fatalf("dry-run returned error: %v", err)
	}
	if cap.hits != 0 {
		t.Errorf("--dry-run MUST NOT fire HTTP; got %d hits", cap.hits)
	}
	if !strings.Contains(stdout, `"dry_run": true`) {
		t.Errorf("dry-run envelope MUST emit dry_run: true; got: %q", stdout)
	}
}

// ── update ─────────────────────────────────────────────────────────

func TestGuidanceUpdatePatchesGuidanceIdEndpoint(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"guidance", "update", "g_1", "--instructions", "new instructions"},
		200, `{"id":"g_1"}`)
	if err != nil {
		t.Fatalf("guidance update returned error: %v", err)
	}
	if cap.method != http.MethodPatch || cap.path != "/guidance/g_1" {
		t.Errorf("expected PATCH /guidance/g_1; got %s %s", cap.method, cap.path)
	}
	body := decodeBody(t, cap.body)
	if body["instructions"] != "new instructions" {
		t.Errorf("body.instructions MUST carry --instructions; got: %v", body["instructions"])
	}
	// Update should ONLY send fields the caller specified — no
	// accidental reset of unset fields.
	if _, hasName := body["name"]; hasName {
		t.Errorf("update MUST NOT send unspecified fields; got name: %v", body["name"])
	}
}

func TestGuidanceUpdateRequiresAtLeastOneFieldFlag(t *testing.T) {
	_, _, err := runCmd(t, []string{"guidance", "update", "g_1"}, 200, `{}`)
	if err == nil {
		t.Fatalf("update with nothing-to-update MUST error")
	}
}

func TestGuidanceUpdateForwardsEnabledFalseExplicitly(t *testing.T) {
	_, cap, _ := runCmd(t,
		[]string{"guidance", "update", "g_1", "--enabled=false"},
		200, `{}`)
	body := decodeBody(t, cap.body)
	if body["enabled"] != false {
		t.Errorf("body.enabled MUST be false when explicitly --enabled=false; got: %v", body["enabled"])
	}
}

// ── delete ─────────────────────────────────────────────────────────

func TestGuidanceDeleteHitsGuidanceIdEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"guidance", "delete", "g_1"}, 204, ``)
	if err != nil {
		t.Fatalf("guidance delete returned error: %v", err)
	}
	if cap.method != http.MethodDelete || cap.path != "/guidance/g_1" {
		t.Errorf("expected DELETE /guidance/g_1; got %s %s", cap.method, cap.path)
	}
}

func TestGuidanceDeleteEmitsAcknowledgmentOn204(t *testing.T) {
	stdout, _, err := runCmd(t, []string{"guidance", "delete", "g_1"}, 204, ``)
	if err != nil {
		t.Fatalf("delete returned error: %v", err)
	}
	if !strings.Contains(stdout, `"deleted": true`) {
		t.Errorf("204 No Content MUST yield an explicit deleted:true ack on stdout; got: %q", stdout)
	}
}
