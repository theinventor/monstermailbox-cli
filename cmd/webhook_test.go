// End-to-end tests for `mmb webhook` CRUD + test-fire + deliveries.
// Each test pins request shape (method, path, body) against the
// openapi.yaml /webhooks contract — same shape the Rails-side request
// specs pin from the server end.
package cmd

import (
	"net/http"
	"strings"
	"testing"
)

// ── list ───────────────────────────────────────────────────────────

func TestWebhookListGetsWebhooksEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"webhook", "list"}, 200, `{"webhooks":[]}`)
	if err != nil {
		t.Fatalf("webhook list returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/webhooks" {
		t.Errorf("expected GET /webhooks; got %s %s", cap.method, cap.path)
	}
}

func TestWebhookListPropagatesLimit(t *testing.T) {
	_, cap, _ := runCmd(t, []string{"webhook", "list", "--limit", "10"}, 200, `{"webhooks":[]}`)
	if !strings.Contains(cap.rawQuery, "limit=10") {
		t.Errorf("--limit MUST land in query string; got: %q", cap.rawQuery)
	}
}

// ── get ────────────────────────────────────────────────────────────

func TestWebhookGetHitsWebhookIdEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"webhook", "get", "42"}, 200, `{"id":"42"}`)
	if err != nil {
		t.Fatalf("webhook get returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/webhooks/42" {
		t.Errorf("expected GET /webhooks/42; got %s %s", cap.method, cap.path)
	}
}

// ── create ─────────────────────────────────────────────────────────

func TestWebhookCreatePostsFullPayload(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"webhook", "create",
			"--name", "prod-receiver",
			"--url", "https://example.com/hook",
			"--event", "message.trusted",
			"--event", "outbound.scanned"},
		201, `{"id":"42","secret":"whsec_aaaaa"}`)
	if err != nil {
		t.Fatalf("webhook create returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/webhooks" {
		t.Errorf("expected POST /webhooks; got %s %s", cap.method, cap.path)
	}
	body := decodeBody(t, cap.body)
	if body["name"] != "prod-receiver" {
		t.Errorf("body.name MUST be --name; got: %v", body["name"])
	}
	if body["url"] != "https://example.com/hook" {
		t.Errorf("body.url MUST be --url; got: %v", body["url"])
	}
	evs, _ := body["events"].([]any)
	if len(evs) != 2 {
		t.Errorf("body.events MUST have both --event values; got: %v", evs)
	}
}

func TestWebhookCreateAllEventsCollapsesToWildcard(t *testing.T) {
	_, cap, _ := runCmd(t,
		[]string{"webhook", "create",
			"--name", "all",
			"--url", "https://example.com/h",
			"--all-events"},
		201, `{"id":"1"}`)
	body := decodeBody(t, cap.body)
	evs, _ := body["events"].([]any)
	if len(evs) != 1 || evs[0] != "*" {
		t.Errorf("--all-events MUST collapse to [\"*\"]; got: %v", evs)
	}
}

func TestWebhookCreateRejectsUnknownEvent(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"webhook", "create",
			"--name", "x", "--url", "https://example.com/h",
			"--event", "agent.frozen"},
		201, `{}`)
	if err == nil {
		t.Fatalf("unknown agent event MUST error before hitting the wire")
	}
	if !strings.Contains(err.Error(), "must be one of") {
		t.Errorf("error MUST enumerate the valid set; got: %v", err)
	}
}

func TestWebhookCreateRejectsMixingWildcardAndExplicit(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"webhook", "create",
			"--name", "x", "--url", "https://example.com/h",
			"--event", "*", "--event", "message.trusted"},
		201, `{}`)
	if err == nil {
		t.Fatalf("mixing '*' with explicit names MUST error")
	}
}

func TestWebhookCreateRequiresNameAndURL(t *testing.T) {
	_, _, err := runCmd(t, []string{"webhook", "create", "--event", "message.trusted"}, 201, `{}`)
	if err == nil {
		t.Fatalf("create without --name/--url MUST error")
	}
}

func TestWebhookCreateRequiresEvents(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"webhook", "create", "--name", "x", "--url", "https://example.com/h"},
		201, `{}`)
	if err == nil {
		t.Fatalf("create without --event or --all-events MUST error")
	}
}

func TestWebhookCreateDryRunSkipsHTTP(t *testing.T) {
	stdout, cap, err := runCmd(t,
		[]string{"webhook", "create",
			"--name", "x", "--url", "https://example.com/h",
			"--event", "message.trusted",
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

func TestWebhookUpdatePatchesWebhookIdEndpoint(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"webhook", "update", "42", "--url", "https://new.example.com/h"},
		200, `{"id":"42"}`)
	if err != nil {
		t.Fatalf("update returned error: %v", err)
	}
	if cap.method != http.MethodPatch || cap.path != "/webhooks/42" {
		t.Errorf("expected PATCH /webhooks/42; got %s %s", cap.method, cap.path)
	}
	body := decodeBody(t, cap.body)
	if body["url"] != "https://new.example.com/h" {
		t.Errorf("body.url MUST carry --url; got: %v", body["url"])
	}
	// Don't send fields the caller didn't set
	if _, ok := body["name"]; ok {
		t.Errorf("update MUST NOT include unspecified --name")
	}
}

func TestWebhookUpdateForwardsActiveFalseExplicitly(t *testing.T) {
	_, cap, _ := runCmd(t,
		[]string{"webhook", "update", "42", "--active=false"},
		200, `{"id":"42"}`)
	body := decodeBody(t, cap.body)
	v, ok := body["active"]
	if !ok || v != false {
		t.Errorf("--active=false MUST forward active:false; got: %v (present=%v)", v, ok)
	}
}

func TestWebhookUpdateRequiresAtLeastOneField(t *testing.T) {
	_, _, err := runCmd(t, []string{"webhook", "update", "42"}, 200, `{}`)
	if err == nil {
		t.Fatalf("update with no fields MUST error")
	}
}

// ── delete ─────────────────────────────────────────────────────────

func TestWebhookDeleteRequiresForce(t *testing.T) {
	_, _, err := runCmd(t, []string{"webhook", "delete", "42"}, 204, ``)
	if err == nil {
		t.Fatalf("delete without --force MUST error")
	}
}

func TestWebhookDeleteHitsWebhookIdEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"webhook", "delete", "42", "--force"}, 204, ``)
	if err != nil {
		t.Fatalf("delete returned error: %v", err)
	}
	if cap.method != http.MethodDelete || cap.path != "/webhooks/42" {
		t.Errorf("expected DELETE /webhooks/42; got %s %s", cap.method, cap.path)
	}
}

// ── test fire ──────────────────────────────────────────────────────

func TestWebhookTestPostsTestDeliveriesNested(t *testing.T) {
	_, cap, err := runCmd(t, []string{"webhook", "test", "42"}, 202, `{"ok":true}`)
	if err != nil {
		t.Fatalf("webhook test returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/webhooks/42/test_deliveries" {
		t.Errorf("expected POST /webhooks/42/test_deliveries; got %s %s", cap.method, cap.path)
	}
}

// ── deliveries log ─────────────────────────────────────────────────

func TestWebhookDeliveriesListsNestedResource(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"webhook", "deliveries", "42", "--limit", "10"},
		200, `{"deliveries":[]}`)
	if err != nil {
		t.Fatalf("webhook deliveries returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/webhooks/42/deliveries" {
		t.Errorf("expected GET /webhooks/42/deliveries; got %s %s", cap.method, cap.path)
	}
	if !strings.Contains(cap.rawQuery, "limit=10") {
		t.Errorf("--limit MUST land in query string; got: %q", cap.rawQuery)
	}
}

// ── events catalog ─────────────────────────────────────────────────

func TestWebhookEventsHitsCatalog(t *testing.T) {
	_, cap, err := runCmd(t, []string{"webhook", "events"}, 200, `{"events":[]}`)
	if err != nil {
		t.Fatalf("webhook events returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/webhook_events" {
		t.Errorf("expected GET /webhook_events; got %s %s", cap.method, cap.path)
	}
}
