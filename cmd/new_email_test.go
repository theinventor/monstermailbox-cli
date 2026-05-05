package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func TestNewEmailRequiresToAndSubject(t *testing.T) {
	// --to + --subject are still mandatory. Body can come from any of
	// four sources, so we only check the two non-body required flags
	// here.
	_, _, err := runCmd(t,
		[]string{"new-email", "--to", "alice@example.com", "--body", "x"},
		200, `{}`)
	if err == nil {
		t.Errorf("new-email missing --subject MUST return an error; got nil")
	}
	_, _, err = runCmd(t,
		[]string{"new-email", "--subject", "hi", "--body", "x"},
		200, `{}`)
	if err == nil {
		t.Errorf("new-email missing --to MUST return an error; got nil")
	}
}

// At least one of --body / --body-html / --body-file / --body-html-file
// must produce content. The error message must enumerate the four
// options so the agent knows the full menu.
func TestNewEmailRejectsMissingBodyAndNamesValidSet(t *testing.T) {
	_, stderr, _, err := runCmdSplit(t,
		[]string{"new-email", "--to", "x@y.com", "--subject", "hi"},
		200, `{}`)
	if err == nil {
		t.Fatalf("new-email with no body source MUST error")
	}
	for _, want := range []string{"--body", "--body-html", "--body-file", "--body-html-file"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("error MUST enumerate %q; got %q", want, stderr)
		}
	}
}

// --body-html maps onto the JSON body_html field. Server-side validation
// (PR theinventor/monstermailbox-ai#52) accepts either body alone.
func TestNewEmailHTMLOnlySendsBodyHTML(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"new-email", "--to", "x@y.com", "--subject", "hi", "--body-html", "<p>hi</p>"},
		202, `{"outbound_id":"o_1","status":"enqueued"}`)
	if err != nil {
		t.Fatalf("new-email --body-html: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["body_html"] != "<p>hi</p>" {
		t.Errorf("body.body_html MUST carry --body-html; got %v", body["body_html"])
	}
	if _, ok := body["body_text"]; ok {
		t.Errorf("HTML-only send MUST NOT include body_text (server auto-derives); got %v", body["body_text"])
	}
}

// Both --body and --body-html together produce the multipart-alternative
// shape the recipient's mail client renders best.
func TestNewEmailBothBodiesShipBoth(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"new-email", "--to", "x@y.com", "--subject", "hi",
			"--body", "plain", "--body-html", "<b>fancy</b>"},
		202, `{"outbound_id":"o_1","status":"enqueued"}`)
	if err != nil {
		t.Fatalf("new-email both bodies: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["body_text"] != "plain" {
		t.Errorf("body_text missing; got %v", body["body_text"])
	}
	if body["body_html"] != "<b>fancy</b>" {
		t.Errorf("body_html missing; got %v", body["body_html"])
	}
}

// --body-file reads from disk so HTML / long bodies don't have to
// shell-escape. --body-html-file works the same way.
func TestNewEmailBodyFileVariantsReadFromDisk(t *testing.T) {
	dir := t.TempDir()
	textPath := filepath.Join(dir, "body.txt")
	htmlPath := filepath.Join(dir, "body.html")
	if err := os.WriteFile(textPath, []byte("from a file"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(htmlPath, []byte("<p>from a file</p>"), 0644); err != nil {
		t.Fatal(err)
	}

	_, cap, err := runCmd(t,
		[]string{"new-email", "--to", "x@y.com", "--subject", "hi",
			"--body-file", textPath, "--body-html-file", htmlPath},
		202, `{"outbound_id":"o_1","status":"enqueued"}`)
	if err != nil {
		t.Fatalf("new-email file variants: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["body_text"] != "from a file" {
		t.Errorf("--body-file did not read; got %v", body["body_text"])
	}
	if body["body_html"] != "<p>from a file</p>" {
		t.Errorf("--body-html-file did not read; got %v", body["body_html"])
	}
}

// Pairing inline + file flag for the SAME body is a usage error — the
// CLI shouldn't silently pick one. Principle 3: the rejection message
// MUST tell the user the rule.
func TestNewEmailInlineAndFileSameBodyConflict(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "body.txt")
	_ = os.WriteFile(p, []byte("from-file"), 0644)

	_, stderr, _, err := runCmdSplit(t,
		[]string{"new-email", "--to", "x@y.com", "--subject", "hi",
			"--body", "inline", "--body-file", p},
		200, `{}`)
	if err == nil {
		t.Fatalf("--body + --body-file MUST conflict")
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("error MUST say 'mutually exclusive'; got %q", stderr)
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
