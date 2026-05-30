package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestTestEmailSendPostsTestEmails(t *testing.T) {
	stdout, cap, err := runCmd(t, []string{"test-email", "send"},
		http.StatusCreated, `{"ok":true,"test":true,"message_id":"123","event":"inbox.new","event_id":"msg_123_test_email","data_test":true}`)
	if err != nil {
		t.Fatalf("test-email send returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/test_emails" {
		t.Errorf("expected POST /test_emails; got %s %s", cap.method, cap.path)
	}
	if cap.authHeader != "Bearer mmb_testkey1234567890" {
		t.Errorf("expected Bearer auth; got %q", cap.authHeader)
	}
	if strings.TrimSpace(string(cap.body)) != "" {
		t.Errorf("test-email send MUST NOT send caller-controlled body fields; got %q", string(cap.body))
	}
	if !strings.Contains(stdout, `"message_id": "123"`) || !strings.Contains(stdout, `"data_test": true`) {
		t.Errorf("stdout should pass through server JSON; got %s", stdout)
	}
}

func TestTestEmailSendSendsIdempotencyKeyHeader(t *testing.T) {
	_, cap, err := runCmd(t, []string{"test-email", "send", "--idempotency-key", "setup-loop-1"},
		http.StatusCreated, `{"ok":true}`)
	if err != nil {
		t.Fatalf("test-email send returned error: %v", err)
	}
	if cap.idempotencyKey != "setup-loop-1" {
		t.Errorf("Idempotency-Key header MUST carry --idempotency-key; got %q", cap.idempotencyKey)
	}
}

func TestTestEmailSendDryRunSkipsHTTP(t *testing.T) {
	stdout, cap, err := runCmd(t, []string{"test-email", "send", "--dry-run", "--idempotency-key", "dry-1"},
		http.StatusCreated, `{"ok":true}`)
	if err != nil {
		t.Fatalf("test-email send --dry-run returned error: %v", err)
	}
	if cap.hits != 0 {
		t.Fatalf("--dry-run MUST NOT fire HTTP; got %d hits", cap.hits)
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("dry-run stdout MUST be JSON; got %q", stdout)
	}
	if env["dry_run"] != true || env["method"] != http.MethodPost || env["path"] != "/test_emails" {
		t.Errorf("bad dry-run envelope: %v", env)
	}
	headers := env["headers"].(map[string]any)
	if env["idempotency_key"] != "dry-1" || headers["Idempotency-Key"] != "dry-1" {
		t.Errorf("dry-run MUST expose idempotency header shape; got %v", env)
	}
}
