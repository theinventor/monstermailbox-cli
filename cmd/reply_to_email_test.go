package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/client"
)

// reply-to-email makes two HTTP calls in sequence: GET /msg/<id> to
// resolve the original sender + subject, then POST /send. A single
// stubbed response (the runCmd helper) won't fit, so this file uses
// a route-aware test server.

type routedResponse struct {
	status int
	body   string
}

// runReplyCmd wires a route-aware stub server and runs the CLI.
// Returns (stdout+stderr, captured POST request, command error).
// `routes` maps "<METHOD> <path>" to the response to serve.
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

func TestReplyToEmailDerivesRecipientAndSubject(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {
			status: 200,
			body: `{"id":"123","from":{"email":"sender@example.com"},"subject":"original topic"}`,
		},
		"POST /send": {
			status: 202,
			body:   `{"outbound_id":"o_1","status":"queued"}`,
		},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "123", "--body", "thanks"},
		routes)
	if err != nil {
		t.Fatalf("reply-to-email returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/send" {
		t.Errorf("expected POST /send; got %s %s", cap.method, cap.path)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.body, &body); err != nil {
		t.Fatalf("body MUST be JSON; got: %s", cap.body)
	}
	if body["to"] != "sender@example.com" {
		t.Errorf("body.to MUST be derived from the original from.email; got: %v", body["to"])
	}
	if body["subject"] != "Re: original topic" {
		t.Errorf("body.subject MUST prepend 'Re: '; got: %v", body["subject"])
	}
	bodyText, _ := body["body_text"].(string)
	if !strings.HasPrefix(bodyText, "thanks") {
		t.Errorf("body.body_text MUST start with --body; got: %v", body["body_text"])
	}
	if body["in_reply_to_message_id"] != "123" {
		t.Errorf("body.in_reply_to_message_id MUST be the --to-message-id value; got: %v", body["in_reply_to_message_id"])
	}
}

// Quote-the-original is the default — recipients see the body they
// were replying to, the way every other email client renders Reply.
// --no-quote opts out for clean bodies. Both pinned here.
func TestReplyToEmailQuotesOriginalByDefault(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {
			status: 200,
			body: `{"id":"123","from":{"email":"sender@example.com","display_name":"Pat"},"subject":"original","body_text":"hello there\nhow are you","received_at":"2026-05-03T20:00:00Z"}`,
		},
		"POST /send": {status: 202, body: `{"outbound_id":"o_1","status":"queued"}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "123", "--body", "thanks"},
		routes)
	if err != nil {
		t.Fatalf("reply-to-email: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	bodyText, _ := body["body_text"].(string)

	if !strings.HasPrefix(bodyText, "thanks") {
		t.Errorf("reply text must be at top: %q", bodyText)
	}
	if !strings.Contains(bodyText, "wrote:") {
		t.Errorf("must include 'wrote:' attribution line; got: %q", bodyText)
	}
	if !strings.Contains(bodyText, "Pat <sender@example.com>") {
		t.Errorf("attribution must name the sender as 'Display <email>'; got: %q", bodyText)
	}
	if !strings.Contains(bodyText, "> hello there") || !strings.Contains(bodyText, "> how are you") {
		t.Errorf("each original line must be quoted with '> '; got: %q", bodyText)
	}
}

func TestReplyToEmailNoQuoteOmitsQuotedBlock(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {
			status: 200,
			body: `{"id":"123","from":{"email":"sender@example.com"},"subject":"original","body_text":"hello"}`,
		},
		"POST /send": {status: 202, body: `{"outbound_id":"o_1","status":"queued"}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "123", "--body", "clean reply", "--no-quote"},
		routes)
	if err != nil {
		t.Fatalf("reply-to-email: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["body_text"] != "clean reply" {
		t.Errorf("--no-quote must send body verbatim; got: %v", body["body_text"])
	}
	if strings.Contains(body["body_text"].(string), ">") {
		t.Errorf("--no-quote must NOT include quoted lines; got: %v", body["body_text"])
	}
}

func TestReplyToEmailDoesNotDoubleStackRePrefix(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {
			status: 200,
			body: `{"id":"123","from":{"email":"sender@example.com"},"subject":"RE: already a reply"}`,
		},
		"POST /send": {status: 202, body: `{"outbound_id":"o_1","status":"queued"}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "123", "--body", "ok"},
		routes)
	if err != nil {
		t.Fatalf("reply-to-email returned error: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["subject"] != "RE: already a reply" {
		t.Errorf("subject MUST NOT double-stack 'Re:' on existing 'RE:'; got: %v", body["subject"])
	}
}

func TestReplyToEmailSubjectOverrideWins(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {
			status: 200,
			body: `{"id":"123","from":{"email":"sender@example.com"},"subject":"original"}`,
		},
		"POST /send": {status: 202, body: `{"outbound_id":"o_1","status":"queued"}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "123", "--body", "ok", "--subject-override", "Renamed thread"},
		routes)
	if err != nil {
		t.Fatalf("reply-to-email returned error: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["subject"] != "Renamed thread" {
		t.Errorf("--subject-override MUST replace the derived subject; got: %v", body["subject"])
	}
}

func TestReplyToEmailPassesCcAndBcc(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/123": {
			status: 200,
			body: `{"id":"123","from":{"email":"sender@example.com"},"subject":"x"}`,
		},
		"POST /send": {status: 202, body: `{"outbound_id":"o_1","status":"queued"}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{
			"reply-to-email",
			"--to-message-id", "123",
			"--body", "ok",
			"--cc", "bob@example.com",
			"--cc", "carol@example.com",
			"--bcc", "dave@example.com",
		},
		routes)
	if err != nil {
		t.Fatalf("reply-to-email returned error: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	cc, _ := body["cc"].([]any)
	if len(cc) != 2 || cc[0] != "bob@example.com" || cc[1] != "carol@example.com" {
		t.Errorf("body.cc MUST carry repeated --cc values; got: %v", cc)
	}
	bcc, _ := body["bcc"].([]any)
	if len(bcc) != 1 || bcc[0] != "dave@example.com" {
		t.Errorf("body.bcc MUST carry --bcc values; got: %v", bcc)
	}
}

func TestReplyToEmail404SurfacesNoSuchMessage(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/missing": {status: 404, body: `{"error":"not_found","message":"nope"}`},
		// POST /send intentionally omitted — we MUST NOT reach it.
	}
	_, _, _, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "missing", "--body", "ok"},
		routes)
	if err == nil {
		t.Fatalf("reply-to-email on a 404 message MUST return an error")
	}
	if !strings.Contains(err.Error(), "no such message") {
		t.Errorf("error MUST mention 'no such message'; got: %v", err)
	}
}

func TestReplyToEmail403SurfacesNoSuchMessage(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/forbidden": {status: 403, body: `{"error":"forbidden","message":"nope"}`},
	}
	_, _, _, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "forbidden", "--body", "ok"},
		routes)
	if err == nil {
		t.Fatalf("reply-to-email on a 403 MUST return an error")
	}
	if !strings.Contains(err.Error(), "no such message") {
		t.Errorf("error MUST mention 'no such message'; got: %v", err)
	}
}

func TestReplyToEmailRequiresMessageIdAndBody(t *testing.T) {
	routes := map[string]routedResponse{
		// stub the GET so we exercise the flag-validation path BEFORE
		// the network call short-circuits us
		"GET /msg/123": {status: 200, body: `{"id":"123","from":{"email":"x@y"}}`},
	}
	_, _, _, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "123"},
		routes)
	if err == nil {
		t.Errorf("reply-to-email without any body source MUST return an error")
	}

	_, _, _, err = runReplyCmd(t,
		[]string{"reply-to-email", "--body", "hi"},
		routes)
	if err == nil {
		t.Errorf("reply-to-email without --to-message-id MUST return an error")
	}
}

// HTML reply: the auto-quote uses <blockquote> instead of "> ".
// When the inbound has body_html, the quote includes it verbatim
// (server already sanitized).
func TestReplyToEmailHTMLQuotesWithBlockquote(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/9": {
			status: 200,
			body: `{"id":"9","from":{"email":"sender@example.com","display_name":"Sender"},
			        "subject":"topic","body_text":"hello","body_html":"<p>hello</p>",
			        "received_at":"2026-05-05T12:34:56Z"}`,
		},
		"POST /send": {status: 202, body: `{"outbound_id":"o_1","status":"enqueued"}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "9", "--body-html", "<p>thanks</p>"},
		routes)
	if err != nil {
		t.Fatalf("reply-to-email --body-html: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)

	html, _ := body["body_html"].(string)
	if !strings.Contains(html, "<p>thanks</p>") {
		t.Errorf("body_html MUST start with the user's HTML body; got %q", html)
	}
	if !strings.Contains(html, "<blockquote") {
		t.Errorf("body_html MUST quote with <blockquote>; got %q", html)
	}
	if !strings.Contains(html, "<p>hello</p>") {
		t.Errorf("blockquote MUST contain the original body_html; got %q", html)
	}
	// Plain-text shape: no body_text was supplied, server will derive.
	if _, ok := body["body_text"]; ok {
		t.Errorf("HTML-only reply MUST NOT include body_text; got %v", body["body_text"])
	}
}

// When the inbound message has only body_text (pre-rollout), the HTML
// reply still produces a quoted block — by escaping the text body
// into the <blockquote>. Round-trip safety: an inbound message with
// `<script>` text content must not become an injection vector in the
// outbound HTML.
func TestReplyToEmailHTMLEscapesPlainTextOriginalIntoBlockquote(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/9": {
			status: 200,
			body: `{"id":"9","from":{"email":"sender@example.com"},
			        "subject":"topic","body_text":"hi <script>alert(1)</script>",
			        "received_at":"2026-05-05T12:34:56Z"}`,
		},
		"POST /send": {status: 202, body: `{"outbound_id":"o_1","status":"enqueued"}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "9", "--body-html", "<p>thanks</p>"},
		routes)
	if err != nil {
		t.Fatalf("reply-to-email: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	html, _ := body["body_html"].(string)
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Errorf("plain-text-only original MUST be HTML-escaped into the blockquote; got %q", html)
	}
	if !strings.Contains(html, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("escaped form MUST be present; got %q", html)
	}
}

// Both bodies: both quote separately — HTML uses <blockquote>, text uses "> ".
// The two outbound payloads stay in sync (multipart/alternative rendering parity).
func TestReplyToEmailBothBodiesQuoteBoth(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/9": {
			status: 200,
			body: `{"id":"9","from":{"email":"sender@example.com"},
			        "subject":"topic","body_text":"hi","body_html":"<p>hi</p>",
			        "received_at":"2026-05-05T12:34:56Z"}`,
		},
		"POST /send": {status: 202, body: `{"outbound_id":"o_1","status":"enqueued"}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "9",
			"--body", "thanks", "--body-html", "<p>thanks</p>"},
		routes)
	if err != nil {
		t.Fatalf("reply-to-email: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)

	text, _ := body["body_text"].(string)
	if !strings.HasPrefix(text, "thanks") || !strings.Contains(text, "> hi") {
		t.Errorf("plain-text MUST start with --body and include '> ' quoted original; got %q", text)
	}
	html, _ := body["body_html"].(string)
	if !strings.Contains(html, "<p>thanks</p>") || !strings.Contains(html, "<blockquote") {
		t.Errorf("HTML MUST start with --body-html and include <blockquote>; got %q", html)
	}
}

// --no-quote MUST suppress quoting in BOTH bodies when both are present.
func TestReplyToEmailNoQuoteSuppressesBothFormats(t *testing.T) {
	routes := map[string]routedResponse{
		"GET /msg/9": {
			status: 200,
			body: `{"id":"9","from":{"email":"sender@example.com"},
			        "subject":"topic","body_text":"hi","body_html":"<p>hi</p>",
			        "received_at":"2026-05-05T12:34:56Z"}`,
		},
		"POST /send": {status: 202, body: `{"outbound_id":"o_1","status":"enqueued"}`},
	}
	_, _, cap, err := runReplyCmd(t,
		[]string{"reply-to-email", "--to-message-id", "9", "--no-quote",
			"--body", "thanks", "--body-html", "<p>thanks</p>"},
		routes)
	if err != nil {
		t.Fatalf("reply-to-email --no-quote: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)

	if body["body_text"] != "thanks" {
		t.Errorf("--no-quote MUST suppress text quote; got %v", body["body_text"])
	}
	if body["body_html"] != "<p>thanks</p>" {
		t.Errorf("--no-quote MUST suppress html quote; got %v", body["body_html"])
	}
}
