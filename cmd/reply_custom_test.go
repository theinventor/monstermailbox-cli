// End-to-end tests for `mmb reply-not-all-with-custom-recipients`,
// the deliberately-awkward secondary reply verb. The verb's whole
// reason for existing is to nudge agents toward reply-all unless they
// REALLY mean to narrow the recipient set.
package cmd

import (
	"strings"
	"testing"
)

func TestReplyCustomRequiresToFlag(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub()},
	}
	_, _, _, err := runReplyCmd(t,
		[]string{"reply-not-all-with-custom-recipients", "123", "--body", "x"},
		routes)
	if err == nil {
		t.Fatalf("--to is required on the custom-recipients verb; got nil")
	}
	if !strings.Contains(err.Error(), "--to is required") {
		t.Errorf("error MUST teach the requirement; got: %v", err)
	}
}

func TestReplyCustomSendsReplyModeSenderOnlyAndExplicitTo(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub()},
		"POST /send":   {status: 202, body: `{}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-not-all-with-custom-recipients", "123", "--to", "alice@stripe.com", "--body", "x"},
		routes)
	if err != nil {
		t.Fatalf("returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["reply_mode"] != "sender_only" {
		t.Errorf("reply_mode MUST be sender_only on the narrow verb; got: %v", body["reply_mode"])
	}
	if body["to"] != "alice@stripe.com" {
		t.Errorf("to MUST be the explicit --to flag; got: %v", body["to"])
	}
	if body["in_reply_to_message_id"] != "123" {
		t.Errorf("threading still required; got: %v", body["in_reply_to_message_id"])
	}
}

func TestReplyCustomCcDoesNotInheritFromParent(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub()},
		"POST /send":   {status: 202, body: `{}`},
	}
	_, _, cap, _ := runReplyCmd(t,
		[]string{"reply-not-all-with-custom-recipients", "123",
			"--to", "alice@stripe.com",
			"--cc", "dave@stripe.com",
			"--body", "x"},
		routes)
	body := decodeBody(t, cap.body)
	cc, _ := body["cc"].([]any)
	// reply_mode=sender_only means server doesn't fill cc; whatever the
	// caller passes is whatever ships. Only one cc here.
	if len(cc) != 1 || cc[0] != "dave@stripe.com" {
		t.Errorf("cc MUST be exactly the caller's --cc (no inheritance from parent); got: %v", cc)
	}
}

func TestReplyCustomQuotesOriginalByDefault(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub(func(m map[string]any) {
			m["body_text"] = "hello"
		})},
		"POST /send": {status: 202, body: `{}`},
	}
	_, _, cap, _ := runReplyCmd(t,
		[]string{"reply-not-all-with-custom-recipients", "123",
			"--to", "alice@stripe.com", "--body", "thanks"},
		routes)
	body := decodeBody(t, cap.body)
	out, _ := body["body_text"].(string)
	if !strings.Contains(out, "> hello") {
		t.Errorf("custom-recipients verb still quotes the original by default; got: %q", out)
	}
}
