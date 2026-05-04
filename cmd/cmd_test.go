// R8.1 — per-command HTTP-request shape tests. Each test spins up
// an httptest.NewServer, points the CLI at it via
// MONSTERMAILBOX_API_URL, runs the command, and asserts the
// outgoing request method + path + auth header + body shape match
// the OpenAPI contract.
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

// captured holds what the test server saw for the most recent
// request, so the test can assert on it after the command returns.
type captured struct {
	method      string
	path        string
	rawQuery    string
	authHeader  string
	contentType string
	body        []byte
}

// runCmd wires the test server into the CLI via env vars, runs the
// argv, and returns (stdout, captured-request, command-error).
func runCmd(t *testing.T, argv []string, respStatus int, respBody string) (string, *captured, error) {
	t.Helper()

	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.rawQuery = r.URL.RawQuery
		cap.authHeader = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		cap.body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(respStatus)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)

	t.Setenv(client.EnvAPIURL, srv.URL)
	t.Setenv(client.EnvAPIKey, "mmb_testkey1234567890")

	root := NewRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs(argv)

	err := root.Execute()
	return out.String(), cap, err
}

// ── whoami ─────────────────────────────────────────────────────

func TestWhoamiHitsVersionEndpoint(t *testing.T) {
	stdout, cap, err := runCmd(t, []string{"whoami"}, 200, `{"version":"v0.1.0"}`)
	if err != nil {
		t.Fatalf("whoami returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/version" {
		t.Errorf("expected GET /version; got %s %s", cap.method, cap.path)
	}
	// Output should NOT contain the full API key (only the masked
	// fingerprint); mmb_testkey1234567890 → mmb_test…7890.
	if strings.Contains(stdout, "mmb_testkey1234567890") {
		t.Errorf("whoami stdout MUST mask the API key; got: %s", stdout)
	}
	if !strings.Contains(stdout, "mmb_test") || !strings.Contains(stdout, "7890") {
		t.Errorf("whoami stdout SHOULD include the masked fingerprint; got: %s", stdout)
	}
}

// ── inbox list ─────────────────────────────────────────────────

func TestInboxListGetsInboxWithAuth(t *testing.T) {
	_, cap, err := runCmd(t, []string{"inbox", "list"}, 200, `{"messages":[]}`)
	if err != nil {
		t.Fatalf("inbox list returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/inbox" {
		t.Errorf("expected GET /inbox; got %s %s", cap.method, cap.path)
	}
	if cap.authHeader != "Bearer mmb_testkey1234567890" {
		t.Errorf("expected Bearer auth; got: %q", cap.authHeader)
	}
}

func TestInboxListPropagatesStateAndLimitFlags(t *testing.T) {
	_, cap, err := runCmd(t, []string{"inbox", "list", "--state", "quarantined", "--limit", "25"}, 200, `{"messages":[]}`)
	if err != nil {
		t.Fatalf("inbox list returned error: %v", err)
	}
	if !strings.Contains(cap.rawQuery, "state=quarantined") {
		t.Errorf("expected query to carry state=quarantined; got: %q", cap.rawQuery)
	}
	if !strings.Contains(cap.rawQuery, "limit=25") {
		t.Errorf("expected query to carry limit=25; got: %q", cap.rawQuery)
	}
}

// ── inbox watch ────────────────────────────────────────────────

func TestInboxWatchGetsEventsEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"inbox", "watch", "--json"}, 200, `data: {"event":"hello"}`)
	if err != nil {
		t.Fatalf("inbox watch returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/events" {
		t.Errorf("expected GET /events; got %s %s", cap.method, cap.path)
	}
}

// ── msg show ───────────────────────────────────────────────────

func TestMsgShowGetsMsgIdEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"msg", "show", "abc123"}, 200, `{"id":"abc123"}`)
	if err != nil {
		t.Fatalf("msg show returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/msg/abc123" {
		t.Errorf("expected GET /msg/abc123; got %s %s", cap.method, cap.path)
	}
}

func TestMsgShowRequiresIdArg(t *testing.T) {
	_, _, err := runCmd(t, []string{"msg", "show"}, 200, `{}`)
	if err == nil {
		t.Errorf("msg show without an id MUST return an error; got nil")
	}
}

// ── expect ─────────────────────────────────────────────────────

func TestExpectPostsToExpectationsEndpointWithFromBody(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"expect", "--from", "ceo@stripe.com", "--subject", "wire", "--ttl", "24h"},
		201, `{"id":"e_1"}`)
	if err != nil {
		t.Fatalf("expect returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/expectations" {
		t.Errorf("expected POST /expectations; got %s %s", cap.method, cap.path)
	}
	if cap.contentType != "application/json" {
		t.Errorf("expected JSON Content-Type; got: %q", cap.contentType)
	}
	var body map[string]any
	if err := json.Unmarshal(cap.body, &body); err != nil {
		t.Fatalf("body MUST be JSON; got: %s", cap.body)
	}
	if body["from"] != "ceo@stripe.com" {
		t.Errorf("body.from MUST be ceo@stripe.com; got: %v", body["from"])
	}
	if body["subject"] != "wire" {
		t.Errorf("body.subject MUST carry the --subject flag; got: %v", body["subject"])
	}
	if body["ttl"] != "24h" {
		t.Errorf("body.ttl MUST carry the --ttl flag; got: %v", body["ttl"])
	}
}

func TestExpectRequiresFromFlag(t *testing.T) {
	_, _, err := runCmd(t, []string{"expect", "--subject", "x"}, 200, `{}`)
	if err == nil {
		t.Errorf("expect without --from MUST return an error; got nil")
	}
}

// ── whitelist add ──────────────────────────────────────────────

func TestWhitelistAddPostsToWhitelistEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"whitelist", "add", "@example.com"}, 201, `{"id":"w_1"}`)
	if err != nil {
		t.Fatalf("whitelist add returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/whitelist" {
		t.Errorf("expected POST /whitelist; got %s %s", cap.method, cap.path)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["pattern"] != "@example.com" {
		t.Errorf("body.pattern MUST carry the positional arg; got: %v", body["pattern"])
	}
}

// ── send ───────────────────────────────────────────────────────

func TestSendPostsToSendEndpointWithRequiredFields(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"send", "--to", "alice@example.com", "--subject", "hi", "--body", "Test."},
		202, `{"state":"awaiting_human_approval"}`)
	if err != nil {
		t.Fatalf("send returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/send" {
		t.Errorf("expected POST /send; got %s %s", cap.method, cap.path)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["to"] != "alice@example.com" {
		t.Errorf("body.to MUST carry --to; got: %v", body["to"])
	}
	if body["subject"] != "hi" {
		t.Errorf("body.subject MUST carry --subject; got: %v", body["subject"])
	}
	if body["body_text"] != "Test." {
		t.Errorf("body.body_text MUST carry --body; got: %v", body["body_text"])
	}
}

func TestSendRequiresAllThreeFields(t *testing.T) {
	_, _, err := runCmd(t,
		[]string{"send", "--to", "alice@example.com"},
		200, `{}`)
	if err == nil {
		t.Errorf("send missing --subject + --body MUST return an error; got nil")
	}
}

func TestSendPrintsDeprecationWarning(t *testing.T) {
	combined, _, err := runCmd(t,
		[]string{"send", "--to", "alice@example.com", "--subject", "hi", "--body", "Test."},
		202, `{"state":"queued"}`)
	if err != nil {
		t.Fatalf("send returned error: %v", err)
	}
	if !strings.Contains(combined, "deprecated") {
		t.Errorf("send MUST warn that it is deprecated; got: %s", combined)
	}
	if !strings.Contains(combined, "new-email") || !strings.Contains(combined, "reply-to-email") {
		t.Errorf("deprecation warning MUST point at new-email + reply-to-email; got: %s", combined)
	}
}

// ── quarantine list ────────────────────────────────────────────

func TestQuarantineListHitsInboxWithStateQuarantined(t *testing.T) {
	_, cap, err := runCmd(t, []string{"quarantine", "list"}, 200, `{"messages":[]}`)
	if err != nil {
		t.Fatalf("quarantine list returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/inbox" {
		t.Errorf("expected GET /inbox; got %s %s", cap.method, cap.path)
	}
	if !strings.Contains(cap.rawQuery, "state=quarantined") {
		t.Errorf("quarantine list MUST query state=quarantined; got: %q", cap.rawQuery)
	}
}

// ── quarantine escalate (v0 stub) ──────────────────────────────

func TestQuarantineEscalateReturnsExplicitNotImplemented(t *testing.T) {
	_, _, err := runCmd(t, []string{"quarantine", "escalate", "msg_42"}, 200, `{}`)
	if err == nil {
		t.Errorf("quarantine escalate MUST return an explicit not-implemented error in v0; got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("error message MUST be the not-implemented stub; got: %v", err)
	}
}

// ── Auth header is set on every authenticated request ─────────

func TestAuthHeaderIsSetOnAuthenticatedRequests(t *testing.T) {
	cases := [][]string{
		{"inbox", "list"},
		{"msg", "show", "x"},
		{"expect", "--from", "x@y"},
		{"whitelist", "add", "x@y"},
		{"send", "--to", "x@y", "--subject", "s", "--body", "b"},
		{"quarantine", "list"},
	}
	for _, argv := range cases {
		t.Run(strings.Join(argv, "_"), func(t *testing.T) {
			_, cap, _ := runCmd(t, argv, 200, `{}`)
			if cap.authHeader != "Bearer mmb_testkey1234567890" {
				t.Errorf("argv %v: expected Bearer auth header; got %q", argv, cap.authHeader)
			}
		})
	}
}
