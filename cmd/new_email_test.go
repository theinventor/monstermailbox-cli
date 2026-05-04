package cmd

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestNewEmailPostsToSendWithoutInReplyTo(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"new-email", "--to", "alice@example.com", "--subject", "hi", "--body", "Test."},
		202, `{"outbound_id":"o_1","status":"queued"}`)
	if err != nil {
		t.Fatalf("new-email returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/send" {
		t.Errorf("expected POST /send; got %s %s", cap.method, cap.path)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.body, &body); err != nil {
		t.Fatalf("body MUST be JSON; got: %s", cap.body)
	}
	if body["to"] != "alice@example.com" {
		t.Errorf("body.to MUST carry --to; got: %v", body["to"])
	}
	if body["subject"] != "hi" {
		t.Errorf("body.subject MUST carry --subject; got: %v", body["subject"])
	}
	if body["body_text"] != "Test." {
		t.Errorf("body.body_text MUST carry --body; got: %v", body["body_text"])
	}
	if _, ok := body["in_reply_to_message_id"]; ok {
		t.Errorf("new-email MUST NOT set in_reply_to_message_id; got: %v", body["in_reply_to_message_id"])
	}
}

func TestNewEmailPassesCcAndBccArrays(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{
			"new-email",
			"--to", "alice@example.com",
			"--subject", "hi",
			"--body", "Test.",
			"--cc", "bob@example.com,carol@example.com",
			"--bcc", "dave@example.com",
		},
		202, `{"outbound_id":"o_1","status":"queued"}`)
	if err != nil {
		t.Fatalf("new-email returned error: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.body, &body); err != nil {
		t.Fatalf("body MUST be JSON; got: %s", cap.body)
	}
	cc, ok := body["cc"].([]any)
	if !ok {
		t.Fatalf("body.cc MUST be a JSON array; got: %T %v", body["cc"], body["cc"])
	}
	if len(cc) != 2 || cc[0] != "bob@example.com" || cc[1] != "carol@example.com" {
		t.Errorf("body.cc MUST carry comma-split --cc values; got: %v", cc)
	}
	bcc, ok := body["bcc"].([]any)
	if !ok {
		t.Fatalf("body.bcc MUST be a JSON array; got: %T %v", body["bcc"], body["bcc"])
	}
	if len(bcc) != 1 || bcc[0] != "dave@example.com" {
		t.Errorf("body.bcc MUST carry --bcc values; got: %v", bcc)
	}
}

func TestNewEmailRequiresAllThreeFields(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"new-email", "--to", "alice@example.com"},
		200, `{}`)
	if err == nil {
		t.Errorf("new-email missing --subject + --body MUST return an error; got nil")
	}
}

func TestNewEmailOmitsCcBccWhenUnset(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"new-email", "--to", "alice@example.com", "--subject", "hi", "--body", "Test."},
		202, `{"outbound_id":"o_1","status":"queued"}`)
	if err != nil {
		t.Fatalf("new-email returned error: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if _, ok := body["cc"]; ok {
		t.Errorf("body MUST NOT include cc when --cc is unset; got: %v", body["cc"])
	}
	if _, ok := body["bcc"]; ok {
		t.Errorf("body MUST NOT include bcc when --bcc is unset; got: %v", body["bcc"])
	}
}
