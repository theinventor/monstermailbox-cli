// End-to-end tests for `mmb new-email`, the tertiary outbound verb
// (use only when you're starting a brand-new thread with no reply
// context). Plus smoke tests that the deprecated `new-message` alias
// from v0.7 still dispatches to the same code path.
//
// File name preserved as new_message_test.go because that's where
// these tests landed in v0.7; rename in the v0.9 cleanup along with
// runNewMessage.
package cmd

import (
	"net/http"
	"strings"
	"testing"
)

func TestNewEmailSendsBasicPayloadToSlashSend(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"new-email", "--to", "alice@stripe.com", "--subject", "Hi", "--body", "hello"},
		202, `{"outbound_id":"o_1","status":"queued"}`)
	if err != nil {
		t.Fatalf("new-email returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/send" {
		t.Errorf("expected POST /send; got %s %s", cap.method, cap.path)
	}
	body := decodeBody(t, cap.body)
	if body["to"] != "alice@stripe.com" {
		t.Errorf("body.to MUST be the --to flag; got: %v", body["to"])
	}
	if body["subject"] != "Hi" {
		t.Errorf("body.subject MUST be the --subject flag; got: %v", body["subject"])
	}
	if body["body_text"] != "hello" {
		t.Errorf("body.body_text MUST be the --body flag; got: %v", body["body_text"])
	}
	// new-email MUST NOT carry reply_mode or in_reply_to — that's a
	// reply concern, not a fresh-message concern.
	if _, ok := body["reply_mode"]; ok {
		t.Errorf("new-email MUST NOT send reply_mode; got: %v", body["reply_mode"])
	}
}

func TestNewEmailRequiresToAndSubject(t *testing.T) {
	_, _, err := runCmd(t, []string{"new-email", "--body", "x"}, 200, `{}`)
	if err == nil {
		t.Fatalf("new-email without --to/--subject MUST error")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error MUST teach what's missing; got: %v", err)
	}
}

func TestNewEmailDryRunSkipsHTTP(t *testing.T) {
	stdout, cap, err := runCmd(t,
		[]string{"new-email", "--to", "x@y.com", "--subject", "x", "--body", "y", "--dry-run"},
		200, `should-never-fire`)
	if err != nil {
		t.Fatalf("dry-run returned error: %v", err)
	}
	if cap.hits != 0 {
		t.Errorf("--dry-run MUST NOT fire HTTP; got %d hits", cap.hits)
	}
	if !strings.Contains(stdout, `"dry_run": true`) {
		t.Errorf("dry-run envelope MUST emit `dry_run: true`; got: %q", stdout)
	}
}

func TestNewEmailSendsCcAndBcc(t *testing.T) {
	_, cap, _ := runCmd(t,
		[]string{"new-email", "--to", "alice@stripe.com", "--subject", "x", "--body", "y",
			"--cc", "bob@stripe.com,carol@stripe.com",
			"--bcc", "dave@stripe.com"},
		202, `{}`)
	body := decodeBody(t, cap.body)
	cc, _ := body["cc"].([]any)
	if len(cc) != 2 {
		t.Errorf("body.cc MUST carry both --cc entries; got: %v", cc)
	}
	bcc, _ := body["bcc"].([]any)
	if len(bcc) != 1 || bcc[0] != "dave@stripe.com" {
		t.Errorf("body.bcc MUST carry --bcc; got: %v", bcc)
	}
}

func TestNewMessageAliasStillDispatchesToSend(t *testing.T) {
	// v0.7 briefly named the canonical verb `new-message`. The alias
	// stays one release so anyone who started using it keeps working.
	_, cap, err := runCmd(t,
		[]string{"new-message", "--to", "alice@stripe.com", "--subject", "x", "--body", "y"},
		202, `{}`)
	if err != nil {
		t.Fatalf("deprecated alias MUST still send: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/send" {
		t.Errorf("alias MUST POST /send like new-email; got %s %s", cap.method, cap.path)
	}
}
