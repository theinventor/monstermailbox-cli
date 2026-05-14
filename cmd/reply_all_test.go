// End-to-end tests for `mmb reply-all <id>`, the primary outbound
// reply verb. reply-all makes two HTTP calls: GET /msg/<id> to fetch
// the original (for subject derivation + quoting), then POST /send
// with reply_mode=all so the SERVER computes the recipient set.
//
// Single-stub runCmd doesn't fit; this file uses the same
// route-aware helper that the old reply_to_email_test.go used.
package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/client"
)

// routedResponse + runReplyCmd are the route-aware stubs we use across
// the reply test files. Defined here (single source of truth);
// reply_custom_test.go consumes the same helpers.
type routedResponse struct {
	status int
	body   string
}

func runReplyCmd(t *testing.T, argv []string, routes map[string]routedResponse) (string, string, *captured, error) {
	t.Helper()

	postCap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		resp, ok := routes[key]
		if !ok {
			http.Error(w, "no stub for "+key, http.StatusNotImplemented)
			return
		}
		if r.Method == http.MethodPost {
			postCap.method = r.Method
			postCap.path = r.URL.Path
			postCap.rawQuery = r.URL.RawQuery
			postCap.authHeader = r.Header.Get("Authorization")
			postCap.contentType = r.Header.Get("Content-Type")
			postCap.body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = io.WriteString(w, resp.body)
	}))
	t.Cleanup(srv.Close)

	t.Setenv(client.EnvAPIURL, srv.URL)
	t.Setenv(client.EnvAPIKey, "mmb_testkey1234567890")

	root := NewRootCmd()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(errBuf)
	root.SetArgs(argv)

	err := root.Execute()
	return out.String(), errBuf.String(), postCap, err
}

// helper: a stub /msg response with sensible defaults; per-test
// overrides via opts.
func msgStub(opts ...func(map[string]any)) string {
	m := map[string]any{
		"id":          "123",
		"from":        map[string]any{"email": "sender@example.com", "display_name": ""},
		"to":          []string{"agent@monstermailbox.com", "carol@stripe.com"},
		"cc":          []string{"bob@stripe.com"},
		"subject":     "original topic",
		"body_text":   "",
		"body_html":   "",
		"received_at": "",
	}
	for _, o := range opts {
		o(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// ── reply-all ──────────────────────────────────────────────────────

func TestReplyAllSendsReplyModeAllAndOmitsTo(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub()},
		"POST /send":   {status: 202, body: `{"outbound_id":"o_1","status":"queued"}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-all", "123", "--body", "thanks"},
		routes)
	if err != nil {
		t.Fatalf("reply-all returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["reply_mode"] != "all" {
		t.Errorf("reply-all MUST send reply_mode=all; got: %v", body["reply_mode"])
	}
	if body["in_reply_to_message_id"] != "123" {
		t.Errorf("in_reply_to_message_id MUST be the positional arg; got: %v", body["in_reply_to_message_id"])
	}
	if _, hasTo := body["to"]; hasTo {
		t.Errorf("reply-all MUST NOT send `to` — server fills from parent.from_email; got: %v", body["to"])
	}
}

func TestReplyAllDerivesSubjectFromParent(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub()},
		"POST /send":   {status: 202, body: `{}`},
	}
	_, _, cap, _ := runReplyCmd(t, []string{"reply-all", "123", "--body", "x"}, routes)
	body := decodeBody(t, cap.body)
	if body["subject"] != "Re: original topic" {
		t.Errorf("subject MUST be 'Re: <original>'; got: %v", body["subject"])
	}
}

func TestReplyAllDoesNotDoubleStackRePrefix(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub(func(m map[string]any) {
			m["subject"] = "Re: existing thread"
		})},
		"POST /send": {status: 202, body: `{}`},
	}
	_, _, cap, _ := runReplyCmd(t, []string{"reply-all", "123", "--body", "x"}, routes)
	body := decodeBody(t, cap.body)
	if body["subject"] != "Re: existing thread" {
		t.Errorf("Re: MUST NOT stack; got: %v", body["subject"])
	}
}

func TestReplyAllSubjectOverrideWins(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub()},
		"POST /send":   {status: 202, body: `{}`},
	}
	_, _, cap, _ := runReplyCmd(t,
		[]string{"reply-all", "123", "--body", "x", "--subject-override", "Custom"},
		routes)
	body := decodeBody(t, cap.body)
	if body["subject"] != "Custom" {
		t.Errorf("--subject-override MUST win over derived 'Re:'; got: %v", body["subject"])
	}
}

func TestReplyAllQuotesOriginalByDefault(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub(func(m map[string]any) {
			m["body_text"] = "hello world"
		})},
		"POST /send": {status: 202, body: `{}`},
	}
	_, _, cap, _ := runReplyCmd(t, []string{"reply-all", "123", "--body", "thanks"}, routes)
	body := decodeBody(t, cap.body)
	out, _ := body["body_text"].(string)
	if !strings.Contains(out, "thanks") {
		t.Errorf("output MUST start with reply text; got: %q", out)
	}
	if !strings.Contains(out, "> hello world") {
		t.Errorf("output MUST quote original Gmail-style; got: %q", out)
	}
}

func TestReplyAllNoQuoteOmitsQuotedBlock(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub(func(m map[string]any) {
			m["body_text"] = "hello world"
		})},
		"POST /send": {status: 202, body: `{}`},
	}
	_, _, cap, _ := runReplyCmd(t, []string{"reply-all", "123", "--body", "thanks", "--no-quote"}, routes)
	body := decodeBody(t, cap.body)
	out, _ := body["body_text"].(string)
	if strings.Contains(out, "> hello world") {
		t.Errorf("--no-quote MUST suppress the quoted block; got: %q", out)
	}
}

func TestReplyAllPassesCcAndBccAdditively(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub()},
		"POST /send":   {status: 202, body: `{}`},
	}
	_, _, cap, _ := runReplyCmd(t,
		[]string{"reply-all", "123", "--body", "x", "--cc", "dave@stripe.com", "--bcc", "eve@stripe.com"},
		routes)
	body := decodeBody(t, cap.body)
	cc, _ := body["cc"].([]any)
	if len(cc) != 1 || cc[0] != "dave@stripe.com" {
		t.Errorf("CLI sends --cc verbatim; server is responsible for merging with parent's set. got: %v", cc)
	}
	bcc, _ := body["bcc"].([]any)
	if len(bcc) != 1 || bcc[0] != "eve@stripe.com" {
		t.Errorf("CLI sends --bcc verbatim; got: %v", bcc)
	}
}

func TestReplyAllHTMLQuotesWithBlockquote(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub(func(m map[string]any) {
			m["body_html"] = "<p>hello</p>"
		})},
		"POST /send": {status: 202, body: `{}`},
	}
	_, _, cap, _ := runReplyCmd(t,
		[]string{"reply-all", "123", "--body-html", "<p>thanks</p>"},
		routes)
	body := decodeBody(t, cap.body)
	out, _ := body["body_html"].(string)
	if !strings.Contains(out, "<p>thanks</p>") || !strings.Contains(out, "<blockquote") {
		t.Errorf("HTML body MUST contain reply + <blockquote>; got: %q", out)
	}
}

func TestReplyAllSendsSafeAttachment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reply-note.txt")
	if err := os.WriteFile(path, []byte("attached reply"), 0o600); err != nil {
		t.Fatal(err)
	}
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub()},
		"POST /send":   {status: 202, body: `{}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-all", "123", "--body", "thanks", "--attach", path},
		routes)
	if err != nil {
		t.Fatalf("reply-all with attachment returned error: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["reply_mode"] != "all" {
		t.Fatalf("reply-all MUST still send reply_mode=all; got: %v", body["reply_mode"])
	}
	attachments, _ := body["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("body.attachments MUST carry one attachment; got: %v", body["attachments"])
	}
	attachment, _ := attachments[0].(map[string]any)
	if attachment["filename"] != "reply-note.txt" {
		t.Errorf("attachment filename MUST be basename only; got: %v", attachment["filename"])
	}
	if attachment["content_base64"] == "" {
		t.Errorf("attachment content_base64 MUST be sent on live reply-all")
	}
}

func TestReplyAllRejectsArchiveOfArchivesAttachment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.zip.gz")
	if err := os.WriteFile(path, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub()},
	}
	_, _, _, err := runReplyCmd(t,
		[]string{"reply-all", "123", "--body", "thanks", "--attach", path},
		routes)
	if err == nil {
		t.Fatalf("archive-of-archives attachment MUST fail")
	}
	if !strings.Contains(err.Error(), "archive inside an archive") {
		t.Errorf("error MUST explain archive nesting; got: %v", err)
	}
}

func TestReplyAll404SurfacesNoSuchMessage(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/missing": {status: 404, body: `{"error":"not_found"}`},
	}
	_, _, _, err := runReplyCmd(t, []string{"reply-all", "missing", "--body", "x"}, routes)
	if err == nil {
		t.Fatalf("404 from /msg/<id> MUST surface as a non-zero exit; got nil")
	}
	if !strings.Contains(err.Error(), "no such message") {
		t.Errorf("error MUST be the friendly 'no such message in your mailbox'; got: %v", err)
	}
}

func TestReplyToEmailAliasStillDispatchesToReplyAll(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {status: 200, body: msgStub()},
		"POST /send":   {status: 202, body: `{}`},
	}
	// The alias takes the same positional id as reply-all.
	_, _, cap, err := runReplyCmd(t, []string{"reply-to-email", "123", "--body", "x"}, routes)
	if err != nil {
		t.Fatalf("deprecated alias MUST still work: %v", err)
	}
	body := decodeBody(t, cap.body)
	if body["reply_mode"] != "all" {
		t.Errorf("alias MUST behave like reply-all; got: %v", body["reply_mode"])
	}
}
