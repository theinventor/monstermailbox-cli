package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/client"
)

func TestContactProductFeedbackPostsToProductEndpoint(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"contact", "product-feedback", "make quarantine release easier"},
		201, `{"received":true,"message":"feedback received"}`)
	if err != nil {
		t.Fatalf("returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/agent_product_feedback" {
		t.Errorf("expected POST /agent_product_feedback; got %s %s", cap.method, cap.path)
	}
	body := decodeBody(t, cap.body)
	if body["text"] != "make quarantine release easier" {
		t.Errorf("body.text MUST carry product feedback; got: %v", body["text"])
	}
}

func TestContactSupportPostsToSupportIntakePath(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"contact", "support", "--subject", "webhook retries", "what happened to delivery abc?"},
		201, `{"received":true,"message":"support request received"}`)
	if err != nil {
		t.Fatalf("returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != supportIntakePath {
		t.Errorf("expected POST %s; got %s %s", supportIntakePath, cap.method, cap.path)
	}
	body := decodeBody(t, cap.body)
	if _, has := body["to"]; has {
		t.Errorf("support contact MUST NOT expose support routing in the CLI payload; got to=%v", body["to"])
	}
	if body["subject"] != "webhook retries" {
		t.Errorf("subject mismatch; got: %v", body["subject"])
	}
	if body["text"] != "what happened to delivery abc?" {
		t.Errorf("text mismatch; got: %v", body["text"])
	}
}

func TestContactSupportTextFlagFormSends(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"contact", "support", "--subject", "account", "--text", "cannot claim my agent"},
		202, `{"status":"queued"}`)
	if err != nil {
		t.Fatalf("returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["text"] != "cannot claim my agent" {
		t.Errorf("--text MUST land on text; got: %v", body["text"])
	}
}

func TestContactSupportStdinFormSends(t *testing.T) {
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.hits++
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.body, _ = readAll(r)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"received":true,"message":"support request received"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(client.EnvAPIURL, srv.URL)
	t.Setenv(client.EnvAPIKey, "mmb_test")

	root := NewRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetIn(strings.NewReader("piped support question\n"))
	root.SetArgs([]string{"contact", "support", "--subject", "inbox", "-"})

	if err := root.Execute(); err != nil {
		t.Fatalf("returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["text"] != "piped support question" {
		t.Errorf("stdin MUST be trimmed and sent as text; got: %v", body["text"])
	}
}

func TestContactSupportDryRunSkipsHTTP(t *testing.T) {
	stdout, cap, err := runCmd(t,
		[]string{"contact", "support", "--subject", "webhook retries", "--text", "question", "--dry-run", "--idempotency-key", "contact-1"},
		201, `should-never-fire`)
	if err != nil {
		t.Fatalf("dry-run returned error: %v", err)
	}
	if cap.hits != 0 {
		t.Errorf("--dry-run MUST NOT fire HTTP; got %d hits", cap.hits)
	}
	var env dryRunEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("dry-run stdout MUST be JSON; got %q err=%v", stdout, err)
	}
	if !env.DryRun || env.Method != http.MethodPost || env.Path != supportIntakePath {
		t.Errorf("bad dry-run envelope: %+v", env)
	}
	if env.IdempotencyKey != "contact-1" || env.Headers["Idempotency-Key"] != "contact-1" {
		t.Errorf("dry-run MUST expose idempotency header shape; got %+v", env)
	}
}

func TestContactSupportRejectsMissingAndAmbiguousInput(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"contact", "support", "--text", "question"},
		200, `{}`)
	if err == nil || !strings.Contains(err.Error(), "--subject is required") {
		t.Fatalf("missing subject MUST teach the required flag; got %v", err)
	}

	_, _, err = runCmd(t,
		[]string{"contact", "support", "--subject", "x"},
		200, `{}`)
	if err == nil || !strings.Contains(err.Error(), "support message text is required") {
		t.Fatalf("missing body MUST teach input forms; got %v", err)
	}

	_, _, err = runCmd(t,
		[]string{"contact", "support", "--subject", "x", "positional", "--text", "flag"},
		200, `{}`)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("ambiguous input MUST error; got %v", err)
	}
}

func TestContactHelpExplainsThreeFeedbackPaths(t *testing.T) {
	root := NewRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"contact", "--help"})

	if err := root.Execute(); err != nil {
		t.Fatalf("help returned error: %v", err)
	}
	help := out.String()
	for _, want := range []string{"contact support", "contact product-feedback", "mmb feedback"} {
		if !strings.Contains(help, want) {
			t.Errorf("contact help MUST mention %q; got %q", want, help)
		}
	}
}
