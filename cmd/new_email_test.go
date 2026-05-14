// End-to-end tests for `mmb new-email`, the tertiary outbound verb
// (use only when you're starting a brand-new thread with no reply
// context).
package cmd

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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

func TestNewEmailSendsAttachmentPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(path, []byte("hello attachment"), 0o600); err != nil {
		t.Fatal(err)
	}
	bodyPath := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(bodyPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, cap, err := runCmd(t,
		[]string{"new-email", "--to", "alice@stripe.com", "--subject", "Hi", "--body-file", bodyPath, "--attach", path},
		202, `{}`)
	if err != nil {
		t.Fatalf("new-email with attachment returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["body_text"] != "hello" {
		t.Errorf("body-file MUST remain compatible with attachments; got: %v", body["body_text"])
	}
	attachments, _ := body["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("body.attachments MUST carry one attachment; got: %v", body["attachments"])
	}
	attachment, _ := attachments[0].(map[string]any)
	if attachment["filename"] != "hello.txt" {
		t.Errorf("attachment filename MUST be basename only; got: %v", attachment["filename"])
	}
	if attachment["content_type"] != "text/plain; charset=utf-8" {
		t.Errorf("attachment content_type MUST be detected; got: %v", attachment["content_type"])
	}
	if attachment["size"] != float64(len("hello attachment")) {
		t.Errorf("attachment size MUST be present; got: %v", attachment["size"])
	}
	want := base64.StdEncoding.EncodeToString([]byte("hello attachment"))
	if attachment["content_base64"] != want {
		t.Errorf("attachment content_base64 MUST carry encoded bytes; got: %v", attachment["content_base64"])
	}
}

func TestNewEmailDryRunRedactsAttachmentBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("secret local bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, cap, err := runCmd(t,
		[]string{"new-email", "--to", "x@y.com", "--subject", "x", "--body", "y", "--attach", path, "--dry-run"},
		200, `should-never-fire`)
	if err != nil {
		t.Fatalf("dry-run returned error: %v", err)
	}
	if cap.hits != 0 {
		t.Errorf("--dry-run MUST NOT fire HTTP; got %d hits", cap.hits)
	}
	if strings.Contains(stdout, "secret local bytes") || strings.Contains(stdout, "content_base64") || strings.Contains(stdout, path) {
		t.Fatalf("dry-run MUST show attachment metadata only, not bytes or local paths; got: %q", stdout)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("dry-run stdout MUST be JSON; got: %q", stdout)
	}
	body, _ := envelope["body"].(map[string]any)
	attachments, _ := body["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("dry-run body.attachments MUST carry metadata; got: %v", body["attachments"])
	}
	attachment, _ := attachments[0].(map[string]any)
	if attachment["filename"] != "notes.txt" || attachment["size"] != float64(len("secret local bytes")) {
		t.Errorf("dry-run attachment metadata mismatch; got: %v", attachment)
	}
}

func TestNewEmailRejectsBlockedAttachmentExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.exe")
	if err := os.WriteFile(path, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := runCmd(t,
		[]string{"new-email", "--to", "x@y.com", "--subject", "x", "--body", "y", "--attach", path},
		200, `{}`)
	if err == nil {
		t.Fatalf("blocked attachment extension MUST fail")
	}
	if !strings.Contains(err.Error(), "blocked file extension") {
		t.Errorf("error MUST explain blocked extension; got: %v", err)
	}
}

func TestNewEmailRejectsOversizeAttachment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxAttachmentBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, _, err = runCmd(t,
		[]string{"new-email", "--to", "x@y.com", "--subject", "x", "--body", "y", "--attach", path},
		200, `{}`)
	if err == nil {
		t.Fatalf("oversize attachment MUST fail")
	}
	if !strings.Contains(err.Error(), "exceeds size limit") {
		t.Errorf("error MUST explain size limit; got: %v", err)
	}
}

func TestNewEmailRejectsUnsafeAttachmentFilename(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"new-email", "--to", "x@y.com", "--subject", "x", "--body", "y", "--attach", "bad\nname.txt"},
		200, `{}`)
	if err == nil {
		t.Fatalf("unsafe attachment filename MUST fail")
	}
	if !strings.Contains(err.Error(), "control characters") {
		t.Errorf("error MUST explain unsafe filename; got: %v", err)
	}
}
