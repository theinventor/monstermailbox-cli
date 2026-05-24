package cmd

import (
	"net/http"
	"strings"
	"testing"
)

func TestStaffWebhookDeliveriesListHitsStaffEndpoint(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"staff", "webhook-deliveries", "list",
			"--owner-email", "owner@example.com",
			"--agent-address", "agent@monstermailbox.com",
			"--status", "gave_up",
			"--include-tests",
			"--limit", "10"},
		200, `{"webhook_deliveries":[]}`)
	if err != nil {
		t.Fatalf("staff webhook deliveries list returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/admin/api/webhook_deliveries" {
		t.Errorf("expected GET /admin/api/webhook_deliveries; got %s %s", cap.method, cap.path)
	}
	for _, want := range []string{
		"owner_email=owner%40example.com",
		"agent_address=agent%40monstermailbox.com",
		"status=gave_up",
		"include_tests=1",
		"limit=10",
	} {
		if !strings.Contains(cap.rawQuery, want) {
			t.Errorf("query missing %s in %q", want, cap.rawQuery)
		}
	}
}

func TestStaffWebhookDeliveriesGetHitsStaffEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"staff", "webhook-deliveries", "get", "42"}, 200, `{"id":"42"}`)
	if err != nil {
		t.Fatalf("staff webhook deliveries get returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/admin/api/webhook_deliveries/42" {
		t.Errorf("expected GET /admin/api/webhook_deliveries/42; got %s %s", cap.method, cap.path)
	}
}

func TestStaffWebhookDeliveriesRedriveRequiresConfirmation(t *testing.T) {
	_, _, _, err := runCmdSplit(t,
		[]string{"staff", "webhook-deliveries", "redrive", "42", "--idempotency-key", "rk-1", "--confirm", "wrong"},
		202, `{"ok":true}`)
	if err == nil {
		t.Fatalf("redrive without exact confirmation must fail")
	}
	if !strings.Contains(err.Error(), "--confirm 42 is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStaffWebhookDeliveriesRedriveRequiresIdempotencyKey(t *testing.T) {
	_, _, _, err := runCmdSplit(t,
		[]string{"staff", "webhook-deliveries", "redrive", "42", "--confirm", "42"},
		202, `{"ok":true}`)
	if err == nil {
		t.Fatalf("redrive without idempotency key must fail")
	}
	if !strings.Contains(err.Error(), "--idempotency-key is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStaffWebhookDeliveriesRedrivePostsGuardedBody(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"staff", "webhook-deliveries", "redrive", "42",
			"--confirm", "42",
			"--idempotency-key", "rk-1",
			"--allow-duplicate"},
		202, `{"ok":true}`)
	if err != nil {
		t.Fatalf("staff webhook deliveries redrive returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/admin/api/webhook_deliveries/42/redrive" {
		t.Errorf("expected POST /admin/api/webhook_deliveries/42/redrive; got %s %s", cap.method, cap.path)
	}
	if cap.idempotencyKey != "rk-1" {
		t.Errorf("redrive MUST send Idempotency-Key; got %q", cap.idempotencyKey)
	}
	body := decodeBody(t, cap.body)
	if body["confirm"] != "redrive" {
		t.Errorf("body.confirm MUST be server confirmation token; got %v", body["confirm"])
	}
	if body["allow_duplicate"] != true {
		t.Errorf("body.allow_duplicate MUST be true when --allow-duplicate is set; got %v", body["allow_duplicate"])
	}
}
