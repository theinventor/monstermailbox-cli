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

// runCmdSplit is like runCmd but keeps stdout / stderr in separate
// buffers so tests can assert hints land where they should.
//
// Mirrors main.go's behavior on error: cobra has SilenceErrors=true,
// so when Execute() returns an error main.go prints "mmb: <err>" to
// stderr and exits 1. Tests want to see that final message, so this
// helper writes it to the captured stderr buffer the same way.
func runCmdSplit(t *testing.T, argv []string, respStatus int, respBody string) (string, string, *captured, error) {
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
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(argv)

	err := root.Execute()
	if err != nil {
		// Match main.go's user-facing rendering so tests can assert on it.
		_, _ = stderr.WriteString("mmb: " + err.Error() + "\n")
	}
	return stdout.String(), stderr.String(), cap, err
}

// ── client / non-2xx surfacing ─────────────────────────────────

func TestNon2xxReturnsErrorWithStatus(t *testing.T) {
	// 422 with a JSON body — the body should still print, AND the
	// command must exit non-zero so the agent can branch on it.
	stdout, _, _, err := runCmdSplit(t, []string{"register", "--address", "x", "--email", "y@z.com"},
		422, `{"error":"validation_failed"}`)
	if err == nil {
		t.Errorf("non-2xx MUST surface an error so cobra exits non-zero; got nil")
	}
	if !strings.Contains(stdout, "validation_failed") {
		t.Errorf("body must still print so the user sees the server's reason; stdout=%q", stdout)
	}
}

func TestEmpty4xxReturnsHelpfulErrorWithURL(t *testing.T) {
	// This is the v0.2.0 register-bug scenario: 405 with empty body.
	// Without this fix the user saw nothing on stdout, nothing on
	// stderr, exit 0. The fix returns an error that names the URL
	// so wrong-host configs are obvious.
	_, stderr, _, err := runCmdSplit(t, []string{"register", "--address", "x", "--email", "y@z.com"},
		405, ``)
	if err == nil {
		t.Errorf("empty 4xx MUST surface an error; got nil. stderr=%q", stderr)
	}
	// The error message rendered by cobra includes "HTTP 405" and
	// the URL hint. cobra prints to stderr by default.
	if !strings.Contains(stderr, "HTTP 405") {
		t.Errorf("error MUST name the status; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "check") || !strings.Contains(stderr, "API URL") {
		t.Errorf("error MUST hint at API URL when body is empty; stderr=%q", stderr)
	}
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

func TestInboxListDefaultsToUnreadOnly(t *testing.T) {
	_, cap, err := runCmd(t, []string{"inbox", "list"}, 200, `{"messages":[]}`)
	if err != nil {
		t.Fatalf("inbox list returned error: %v", err)
	}
	if !strings.Contains(cap.rawQuery, "unread=true") {
		t.Errorf("expected default to filter unread=true; got: %q", cap.rawQuery)
	}
}

func TestInboxListAllFlagDropsUnreadFilter(t *testing.T) {
	_, cap, err := runCmd(t, []string{"inbox", "list", "--all"}, 200, `{"messages":[]}`)
	if err != nil {
		t.Fatalf("inbox list --all returned error: %v", err)
	}
	if strings.Contains(cap.rawQuery, "unread=") {
		t.Errorf("--all MUST NOT send unread=...; got: %q", cap.rawQuery)
	}
}

func TestInboxListPeekFlagPropagates(t *testing.T) {
	_, cap, err := runCmd(t, []string{"inbox", "list", "--peek"}, 200, `{"messages":[]}`)
	if err != nil {
		t.Fatalf("inbox list --peek returned error: %v", err)
	}
	if !strings.Contains(cap.rawQuery, "peek=true") {
		t.Errorf("--peek MUST send peek=true; got: %q", cap.rawQuery)
	}
}

func TestInboxListEmitsHintToStderrAndKeepsStdoutClean(t *testing.T) {
	respBody := `{
      "messages": [{"id":"1"},{"id":"2"}],
      "meta": {
        "showing": {"state":"trusted","unread":true,"peek":false},
        "returned": 2,
        "counts": {
          "trusted":     {"unread":2,"total":50},
          "quarantined": {"unread":5,"total":5},
          "rejected":    {"unread":0,"total":2}
        }
      }
    }`
	stdout, stderr, _, _ := runCmdSplit(t, []string{"inbox", "list"}, 200, respBody)
	if !strings.Contains(stderr, "2 unread trusted shown") {
		t.Errorf("hint should describe the page; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "quarantined 5 (5 unread)") {
		t.Errorf("hint should surface other-state totals; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "--all") {
		t.Errorf("hint should mention --all switch; stderr=%q", stderr)
	}
	if strings.Contains(stdout, "trusted shown") {
		t.Errorf("hint MUST NOT pollute stdout (so | jq still works); stdout=%q", stdout)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Errorf("stdout must be the raw JSON envelope; parse err=%v stdout=%q", err, stdout)
	}
}

func TestInboxListAllFlagSuppressesAllHint(t *testing.T) {
	respBody := `{
      "messages": [{"id":"1"}],
      "meta": {
        "showing": {"state":"trusted","unread":false,"peek":false},
        "returned": 1,
        "counts": {"trusted":{"unread":0,"total":1},"quarantined":{"unread":0,"total":0},"rejected":{"unread":0,"total":0}}
      }
    }`
	_, stderr, _, _ := runCmdSplit(t, []string{"inbox", "list", "--all"}, 200, respBody)
	if strings.Contains(stderr, "use --all") {
		t.Errorf("--all should suppress the use-all hint; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "1 trusted shown") {
		t.Errorf("hint should still describe the page even with --all; stderr=%q", stderr)
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

func TestMsgShowPeekFlagPropagates(t *testing.T) {
	_, cap, err := runCmd(t, []string{"msg", "show", "abc123", "--peek"}, 200, `{"id":"abc123"}`)
	if err != nil {
		t.Fatalf("msg show --peek returned error: %v", err)
	}
	if !strings.Contains(cap.rawQuery, "peek=true") {
		t.Errorf("--peek MUST send peek=true; got: %q", cap.rawQuery)
	}
}

func TestMsgShowDefaultDoesNotPeek(t *testing.T) {
	_, cap, err := runCmd(t, []string{"msg", "show", "abc123"}, 200, `{"id":"abc123"}`)
	if err != nil {
		t.Fatalf("msg show returned error: %v", err)
	}
	if strings.Contains(cap.rawQuery, "peek=") {
		t.Errorf("default msg show MUST NOT send peek; got: %q", cap.rawQuery)
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

// ── No-silent-failure invariant ────────────────────────────────
//
// The class of bug we never want to ship again: a CLI invocation
// that should produce visible output instead produces nothing —
// empty stdout, empty stderr, exit 0. Discovered in v0.2.0 when
// `mmb register` against the marketing host returned 405 with an
// empty body and the user thought their command silently succeeded.
//
// Every API-hitting command MUST, when the server returns an empty
// 4xx, surface an error to the user (non-zero exit + non-empty
// stderr). The table below is the full set of API-hitting commands
// with minimum-viable args; if you add a new API command, add a row
// here too. Adding a row that breaks this invariant is the canary —
// don't make the test pass by deleting the row, fix the command's
// error handling.

func TestEveryAPICommandSurfacesEmpty4xxAsAVisibleError(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"inbox_list", []string{"inbox", "list"}},
		{"msg_show", []string{"msg", "show", "abc"}},
		{"expect", []string{"expect", "--from", "ceo@x.com", "--subject", "wire"}},
		{"whitelist_add", []string{"whitelist", "add", "alice@x.com"}},
		{"send", []string{"send", "--to", "x@y.com", "--subject", "s", "--body", "b"}},
		{"new_email", []string{"new-email", "--to", "x@y.com", "--subject", "s", "--body", "b"}},
		{"reply_to_email", []string{"reply-to-email", "--to-message-id", "1", "--body", "b"}},
		{"quarantine_list", []string{"quarantine", "list"}},
		{"register", []string{"register", "--address", "x", "--email", "y@z.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, _, err := runCmdSplit(t, tc.argv, 405, ``)
			if err == nil {
				t.Errorf("%s: empty 4xx MUST return a non-nil error so cobra exits non-zero", tc.name)
			}
			if !strings.Contains(stderr, "HTTP 405") {
				t.Errorf("%s: stderr MUST surface the status; got %q", tc.name, stderr)
			}
			if !strings.Contains(stderr, "API URL") {
				t.Errorf("%s: stderr MUST hint at the URL when body is empty; got %q", tc.name, stderr)
			}
		})
	}
}

// `whoami` is the diagnostic command — its whole job is to expose
// the API URL, the loaded key fingerprint, and the server's response.
// On a non-2xx, it shows `server_status` in the JSON output so the
// user can see exactly what went wrong without the command erroring
// out (which would obscure the diagnostic info in many shells). Pin
// that special-case so a future refactor doesn't accidentally make
// whoami exit non-zero on a server problem.
func TestWhoamiShowsServerStatusOnNon2xxRatherThanErroring(t *testing.T) {
	stdout, _, _, err := runCmdSplit(t, []string{"whoami"}, 405, ``)
	if err != nil {
		t.Errorf("whoami MUST stay quiet on non-2xx — it's the diagnostic surface; got err=%v", err)
	}
	if !strings.Contains(stdout, `"server_status": 405`) {
		t.Errorf("whoami MUST surface server_status in stdout JSON; got %q", stdout)
	}
}

// Companion to the table above: when the server DOES return a body
// on a non-2xx (e.g. a JSON validation error), the body must still
// reach stdout AND the error must still surface so the agent can
// branch on exit code. Body without exit-code = invisible failure
// in pipelines; exit-code without body = useless ("what was wrong?").
func TestEveryAPICommandPrintsBodyAndStillErrorsOnNon2xx(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"inbox_list", []string{"inbox", "list"}},
		{"msg_show", []string{"msg", "show", "abc"}},
		{"register", []string{"register", "--address", "x", "--email", "y@z.com"}},
	}
	body := `{"error":"validation_failed","message":"this field is required"}`
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, _, err := runCmdSplit(t, tc.argv, 422, body)
			if err == nil {
				t.Errorf("%s: 422 MUST surface an error; got nil", tc.name)
			}
			if !strings.Contains(stdout, "validation_failed") {
				t.Errorf("%s: server's error body MUST print to stdout so the user sees it; got %q",
					tc.name, stdout)
			}
		})
	}
}

// Pin the exact decision matrix in passthroughJSON so a refactor
// can't quietly swap the boundary. Writes a tiny dummy command to
// drive the helper directly; we don't want this test coupled to
// any specific API command's flag plumbing.
func TestPassthroughJSONStatusCodeClassification(t *testing.T) {
	cases := []struct {
		status      int
		body        string
		wantErr     bool
		wantStdout  string // substring expected on stdout (or "" for "any")
	}{
		// 2xx — success path. Body prints; no error.
		{200, `{"ok":true}`, false, `"ok": true`},
		{201, `{"id":"x"}`, false, `"id": "x"`},
		{202, `{"queued":true}`, false, "queued"},
		{204, ``, false, ""}, // No content; no error, no panic.

		// 4xx — error path. Body prints if present; error returned.
		{400, `{"error":"bad_request"}`, true, "bad_request"},
		{401, `{"error":"unauthorized"}`, true, "unauthorized"},
		{403, `{"error":"forbidden"}`, true, "forbidden"},
		{404, `{"error":"not_found"}`, true, "not_found"},
		{405, ``, true, ""},                        // The v0.2.0 bug shape.
		{409, `{"error":"conflict"}`, true, "conflict"},
		{422, `{"error":"validation_failed"}`, true, "validation_failed"},

		// 5xx — server problem. Treated the same as 4xx.
		{500, ``, true, ""},
		{500, `<html>oops</html>`, true, "oops"}, // non-JSON body still passes through.
		{502, `bad gateway`, true, "bad gateway"},
		{503, `{"error":"down"}`, true, "down"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)

			resp, err := http.Get(srv.URL)
			if err != nil {
				t.Fatalf("client GET: %v", err)
			}
			defer resp.Body.Close()

			out := &bytes.Buffer{}
			gotErr := passthroughJSON(out, resp)

			if tc.wantErr && gotErr == nil {
				t.Errorf("status %d MUST yield an error; got nil. stdout=%q", tc.status, out.String())
			}
			if !tc.wantErr && gotErr != nil {
				t.Errorf("status %d MUST NOT yield an error; got %v", tc.status, gotErr)
			}
			if tc.wantStdout != "" && !strings.Contains(out.String(), tc.wantStdout) {
				t.Errorf("status %d: stdout MUST contain %q; got %q", tc.status, tc.wantStdout, out.String())
			}
		})
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
