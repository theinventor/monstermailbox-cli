// End-to-end tests for `mmb agent-product-feedback`. Pins the wire
// shape (POST /agent_product_feedback with {text}) against the
// openapi.yaml contract, plus the input-form precedence rules
// (positional / --text / stdin via "-") and rejection of mixed forms.
package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/client"
)

func TestAgentProductFeedbackPostsToTheRightEndpoint(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"agent-product-feedback", "the policy editor is great"},
		201, `{"received":true,"message":"feedback received"}`)
	if err != nil {
		t.Fatalf("returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/agent_product_feedback" {
		t.Errorf("expected POST /agent_product_feedback; got %s %s", cap.method, cap.path)
	}
	body := decodeBody(t, cap.body)
	if body["text"] != "the policy editor is great" {
		t.Errorf("body.text MUST carry positional arg; got: %v", body["text"])
	}
}

func TestAgentProductFeedbackTextFlagFormSends(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"agent-product-feedback", "--text", "love it"},
		201, `{"received":true,"message":"feedback received"}`)
	if err != nil {
		t.Fatalf("returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["text"] != "love it" {
		t.Errorf("--text MUST land on body.text; got: %v", body["text"])
	}
}

func TestAgentProductFeedbackStdinFormSends(t *testing.T) {
	// Custom harness because runCmd doesn't wire stdin.
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.hits++
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.body, _ = readAll(r)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"received":true,"message":"feedback received"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(client.EnvAPIURL, srv.URL)
	t.Setenv(client.EnvAPIKey, "mmb_test")

	root := NewRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetIn(strings.NewReader("piped text from stdin\n"))
	root.SetArgs([]string{"agent-product-feedback", "-"})

	if err := root.Execute(); err != nil {
		t.Fatalf("returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["text"] != "piped text from stdin" {
		t.Errorf("stdin MUST be trimmed and sent as body.text; got: %v", body["text"])
	}
}

func TestAgentProductFeedbackRejectsMixedInputForms(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"agent-product-feedback", "pos arg", "--text", "flag arg"},
		200, `{}`)
	if err == nil {
		t.Fatalf("mixed positional + --text MUST error so the agent doesn't wonder which won")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("error MUST teach the rule; got: %v", err)
	}
}

func TestAgentProductFeedbackRequiresInput(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"agent-product-feedback"},
		200, `{}`)
	if err == nil {
		t.Fatalf("no input MUST error")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error MUST teach what's missing; got: %v", err)
	}
}

func TestAgentProductFeedbackDryRunSkipsHTTP(t *testing.T) {
	stdout, cap, err := runCmd(t,
		[]string{"agent-product-feedback", "x", "--dry-run"},
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
	if !strings.Contains(stdout, `/agent_product_feedback`) {
		t.Errorf("dry-run envelope MUST name the path; got: %q", stdout)
	}
}

func TestAgentProductFeedbackSends422AsAVisibleError(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"agent-product-feedback", "x"},
		422, `{"error":"validation_failed","message":"text exceeds 4096 bytes"}`)
	if err == nil {
		t.Fatalf("422 MUST surface as a non-zero exit")
	}
}

// readAll is a tiny shim so the stdin test can read the request body
// without pulling in io.ReadAll's import every time. Mirrors the
// signature runCmd uses internally.
func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}
