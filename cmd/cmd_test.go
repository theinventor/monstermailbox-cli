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
	"path/filepath"
	"strings"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/config"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// captured holds what the test server saw for the most recent
// request, so the test can assert on it after the command returns.
type captured struct {
	method         string
	path           string
	rawQuery       string
	authHeader     string
	contentType    string
	body           []byte
	idempotencyKey string
	hits           int
}

// runCmd wires the test server into the CLI via env vars, runs the
// argv, and returns (stdout, captured-request, command-error).
func runCmd(t *testing.T, argv []string, respStatus int, respBody string) (string, *captured, error) {
	t.Helper()

	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.hits++
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.rawQuery = r.URL.RawQuery
		cap.authHeader = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		cap.idempotencyKey = r.Header.Get("Idempotency-Key")
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
		cap.hits++
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.rawQuery = r.URL.RawQuery
		cap.authHeader = r.Header.Get("Authorization")
		cap.contentType = r.Header.Get("Content-Type")
		cap.idempotencyKey = r.Header.Get("Idempotency-Key")
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

func TestRegister_BlockedOwnerEmailsFailBeforeRequest(t *testing.T) {
	cases := []string{
		"troy@example.com",
		"troy@example.org",
		"troy@example.net",
		"troy@test.invalid",
		"troy@localhost",
		"troy@mail.localhost",
		"troy@mail.localdomain",
		"no-reply@ownerdomain.com",
		"noreply+agent@ownerdomain.com",
		"notifications@ownerdomain.com",
		"bot@ownerdomain.com",
	}
	for _, email := range cases {
		t.Run(email, func(t *testing.T) {
			_, stderr, cap, err := runCmdSplit(t, []string{"register", "--address", "x", "--email", email},
				http.StatusCreated, `{"ok":true}`)
			if err == nil {
				t.Fatalf("register with blocked owner email should fail")
			}
			if cap.hits != 0 {
				t.Fatalf("blocked owner email should make no HTTP requests; got %d", cap.hits)
			}
			if !strings.Contains(stderr, "real human owner email") || !strings.Contains(stderr, "actual human owner's email") {
				t.Fatalf("error should explain the owner email rule; stderr=%q err=%v", stderr, err)
			}
		})
	}
}

func TestRegister_BlockedOwnerEmailDryRunFailsBeforeEnvelope(t *testing.T) {
	stdout, stderr, cap, err := runCmdSplit(t, []string{"register", "--address", "x", "--email", "troy@example.com", "--dry-run"},
		http.StatusCreated, `{"ok":true}`)
	if err == nil {
		t.Fatalf("register dry-run with blocked owner email should fail")
	}
	if cap.hits != 0 {
		t.Fatalf("blocked owner email dry-run should make no HTTP requests; got %d", cap.hits)
	}
	if stdout != "" {
		t.Fatalf("blocked owner email dry-run should not emit a request envelope; stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "real human owner email") {
		t.Fatalf("error should explain the owner email rule; stderr=%q err=%v", stderr, err)
	}
}

func TestRegister_RealLookingOwnerEmailStillHitsRegisterPath(t *testing.T) {
	_, cap, err := runCmd(t, []string{"register", "--address", "x", "--email", "owner@realcompany.dev"},
		http.StatusCreated, `{"ok":true}`)
	if err != nil {
		t.Fatalf("register with real-looking owner email should proceed: %v", err)
	}
	if cap.hits != 1 || cap.method != http.MethodPost || cap.path != "/agents/register" {
		t.Fatalf("register should hit POST /agents/register once; hits=%d method=%s path=%s", cap.hits, cap.method, cap.path)
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

func TestWhoamiRootProfileSelectsSavedProfileForSingleInvocation(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)
	t.Setenv(client.EnvAPIKey, "ENV_KEY_BUT_IGNORED")
	t.Setenv(client.EnvAPIURL, "https://env.example.com")

	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.hits++
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"v0.1.0"}`)
	}))
	t.Cleanup(srv.Close)

	f := &config.File{}
	f.Put("default", config.Profile{
		APIURL:       srv.URL,
		APIKey:       "DEFAULT_PROFILE_KEY_LONG",
		AgentAddress: "default@monstermailbox.com",
	})
	f.Put("product", config.Profile{
		APIURL:       srv.URL,
		APIKey:       "PRODUCT_PROFILE_KEY_LONG",
		AgentAddress: "product@monstermailbox.com",
	})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	root := NewRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"--profile", "product", "whoami"})
	if err := root.Execute(); err != nil {
		t.Fatalf("whoami returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/version" {
		t.Errorf("expected GET /version; got %s %s", cap.method, cap.path)
	}
	if cap.authHeader != "Bearer PRODUCT_PROFILE_KEY_LONG" {
		t.Errorf("--profile product should select product credentials; got auth %q", cap.authHeader)
	}
	if !strings.Contains(out.String(), "product@monstermailbox.com") {
		t.Errorf("whoami should show selected profile address; got %s", out.String())
	}

	after, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.DefaultProfile != "default" {
		t.Errorf("--profile must not change default_profile; got %q", after.DefaultProfile)
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

// Principle 3: when the agent passes an invalid enum value, the error
// MUST name the valid set so it can self-correct in one retry.
func TestInboxListRejectsInvalidStateAndNamesValidSet(t *testing.T) {
	_, stderr, _, err := runCmdSplit(t, []string{"inbox", "list", "--state", "secret"}, 200, `{}`)
	if err == nil {
		t.Fatalf("invalid --state MUST surface an error; got nil")
	}
	for _, want := range []string{"trusted", "quarantined", "rejected", "secret"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("error MUST name %q; got %q", want, stderr)
		}
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
	// --max-reconnects=1 caps the new reconnect loop so the test
	// terminates instead of looping forever on the empty test server.
	_, cap, err := runCmd(t, []string{"inbox", "watch", "--max-reconnects", "1"},
		200, `event: hello
data: {"event":"hello"}

`)
	if err != nil {
		t.Fatalf("inbox watch returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/events" {
		t.Errorf("expected GET /events; got %s %s", cap.method, cap.path)
	}
}

// `inbox wait` blocks until the next matching event then exits. Pin
// the one-shot semantic: a single event causes a clean exit, not a
// reconnect loop.
func TestInboxWaitEmitsOneEventThenExits(t *testing.T) {
	body := `event: inbox.new
data: {"id":"m1","state":"trusted"}

`
	stdout, _, err := runCmd(t, []string{"inbox", "wait", "--timeout", "5s"}, 200, body)
	if err != nil {
		t.Fatalf("inbox wait returned error: %v", err)
	}
	// stdout MUST be ONE JSON line describing the event.
	line := strings.TrimSpace(stdout)
	if line == "" {
		t.Fatalf("inbox wait MUST emit one JSON line; got empty")
	}
	if strings.Count(line, "\n") != 0 {
		t.Errorf("inbox wait MUST emit exactly one event; got %d lines", strings.Count(line, "\n")+1)
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("inbox wait line MUST be valid JSON; got %q", line)
	}
	if ev["event"] != "inbox.new" {
		t.Errorf("event name MUST round-trip; got %v", ev["event"])
	}
	data, _ := ev["data"].(map[string]any)
	if data["id"] != "m1" {
		t.Errorf("event data MUST round-trip; got %v", data)
	}
}

// State filter MUST drop non-matching events before they reach stdout.
func TestInboxWaitStateFilterDropsNonMatching(t *testing.T) {
	body := `event: inbox.quarantined
data: {"id":"q1","state":"quarantined"}

event: inbox.new
data: {"id":"t1","state":"trusted"}

`
	stdout, _, err := runCmd(t, []string{"inbox", "wait", "--state", "trusted", "--timeout", "5s"}, 200, body)
	if err != nil {
		t.Fatalf("inbox wait: %v", err)
	}
	if !strings.Contains(stdout, `"id":"t1"`) {
		t.Errorf("trusted event MUST reach stdout; got %q", stdout)
	}
	if strings.Contains(stdout, `"id":"q1"`) {
		t.Errorf("quarantined event MUST be filtered out; got %q", stdout)
	}
}

// SSE heartbeat comments MUST NOT pollute stdout — they're proof of
// life only. Also MUST NOT satisfy `wait --stop-on-first`.
func TestInboxWaitSwallowsHeartbeatComments(t *testing.T) {
	// Heartbeat comment, then a real event. Without the suppression,
	// `wait` would exit on the comment and stdout would be empty.
	body := `: heartbeat

event: inbox.new
data: {"id":"t1","state":"trusted"}

`
	stdout, _, err := runCmd(t, []string{"inbox", "wait", "--timeout", "5s"}, 200, body)
	if err != nil {
		t.Fatalf("inbox wait: %v", err)
	}
	if strings.Contains(stdout, "heartbeat") {
		t.Errorf("heartbeat comment MUST NOT pollute stdout; got %q", stdout)
	}
	if !strings.Contains(stdout, `"id":"t1"`) {
		t.Errorf("the real event MUST still reach stdout; got %q", stdout)
	}
}

// ── msg get ────────────────────────────────────────────────────

func TestMsgGetHitsMsgIdEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"msg", "get", "abc123"}, 200, `{"id":"abc123"}`)
	if err != nil {
		t.Fatalf("msg get returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/msg/abc123" {
		t.Errorf("expected GET /msg/abc123; got %s %s", cap.method, cap.path)
	}
}

func TestMsgGetPeekFlagPropagates(t *testing.T) {
	_, cap, err := runCmd(t, []string{"msg", "get", "abc123", "--peek"}, 200, `{"id":"abc123"}`)
	if err != nil {
		t.Fatalf("msg get --peek returned error: %v", err)
	}
	if !strings.Contains(cap.rawQuery, "peek=true") {
		t.Errorf("--peek MUST send peek=true; got: %q", cap.rawQuery)
	}
}

func TestMsgGetDefaultDoesNotPeek(t *testing.T) {
	_, cap, err := runCmd(t, []string{"msg", "get", "abc123"}, 200, `{"id":"abc123"}`)
	if err != nil {
		t.Fatalf("msg get returned error: %v", err)
	}
	if strings.Contains(cap.rawQuery, "peek=") {
		t.Errorf("default msg get MUST NOT send peek; got: %q", cap.rawQuery)
	}
}

func TestMsgGetRequiresIdArg(t *testing.T) {
	_, _, err := runCmd(t, []string{"msg", "get"}, 200, `{}`)
	if err == nil {
		t.Errorf("msg get without an id MUST return an error; got nil")
	}
}

// `msg show` is the v0.2 spelling kept as a hidden deprecated alias —
// existing scripts MUST still work for one release.
func TestMsgShowAliasStillWorks(t *testing.T) {
	_, cap, err := runCmd(t, []string{"msg", "show", "abc123"}, 200, `{"id":"abc123"}`)
	if err != nil {
		t.Fatalf("msg show alias MUST still work; got error: %v", err)
	}
	if cap.path != "/msg/abc123" {
		t.Errorf("alias MUST hit the same endpoint; got %s", cap.path)
	}
}

// ── expect ─────────────────────────────────────────────────────

func TestExpectPostsCanonicalDomainPayloadFromEmail(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"expect", "--from", "noreply@mail.mapillary.com", "--subject-regex", "verify", "--purpose", "signup-verification", "--window", "7d"},
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
	if _, ok := body["from"]; ok {
		t.Errorf("body.from MUST NOT be sent to the canonical expectations API; got: %v", body["from"])
	}
	if body["domain"] != "mapillary.com" {
		t.Errorf("body.domain MUST be canonical eTLD+1; got: %v", body["domain"])
	}
	if body["subject_regex"] != "verify" {
		t.Errorf("body.subject_regex MUST carry --subject-regex; got: %v", body["subject_regex"])
	}
	if body["purpose"] != "signup-verification" {
		t.Errorf("body.purpose MUST carry --purpose; got: %v", body["purpose"])
	}
	if body["expires_in"] != "7d" {
		t.Errorf("body.expires_in MUST carry --window; got: %v", body["expires_in"])
	}
}

func TestExpectPostsCanonicalDomainPayloadFromBareDomainAndLegacyTTL(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"expect", "--from", "Login.Example.CO.UK", "--subject", "code", "--ttl", "24h"},
		201, `{"id":"e_1"}`)
	if err != nil {
		t.Fatalf("expect returned error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(cap.body, &body); err != nil {
		t.Fatalf("body MUST be JSON; got: %s", cap.body)
	}
	if body["domain"] != "example.co.uk" {
		t.Errorf("body.domain MUST be canonical eTLD+1 for bare domains; got: %v", body["domain"])
	}
	if body["subject_regex"] != "code" {
		t.Errorf("deprecated --subject MUST map to subject_regex; got: %v", body["subject_regex"])
	}
	if body["purpose"] != "verification" {
		t.Errorf("default purpose MUST be verification; got: %v", body["purpose"])
	}
	if body["expires_in"] != "24h" {
		t.Errorf("deprecated --ttl MUST map to expires_in; got: %v", body["expires_in"])
	}
}

func TestExpectAcceptsMapillaryReproInputs(t *testing.T) {
	for _, from := range []string{"mapillary.com", "noreply@mapillary.com"} {
		t.Run(from, func(t *testing.T) {
			_, cap, err := runCmd(t,
				[]string{"expect", "--from", from, "--window", "7d"},
				201, `{"id":"e_1"}`)
			if err != nil {
				t.Fatalf("expect returned error: %v", err)
			}

			var body map[string]any
			if err := json.Unmarshal(cap.body, &body); err != nil {
				t.Fatalf("body MUST be JSON; got: %s", cap.body)
			}
			if body["domain"] != "mapillary.com" {
				t.Errorf("body.domain MUST be mapillary.com; got: %v", body["domain"])
			}
			if body["expires_in"] != "7d" {
				t.Errorf("body.expires_in MUST carry the requested window; got: %v", body["expires_in"])
			}
		})
	}
}

func TestExpectRequiresFromFlag(t *testing.T) {
	_, _, err := runCmd(t, []string{"expect", "--subject", "x"}, 200, `{}`)
	if err == nil {
		t.Errorf("expect without --from MUST return an error; got nil")
	}
}

func TestExpectDryRunShowsCanonicalPayload(t *testing.T) {
	stdout, cap, err := runCmd(t,
		[]string{"expect", "--from", "noreply@mapillary.com", "--window", "7d", "--dry-run"},
		200, `{}`)
	if err != nil {
		t.Fatalf("expect dry-run returned error: %v", err)
	}
	if cap.hits != 0 {
		t.Errorf("dry-run MUST NOT make an HTTP call; server saw %d hit(s)", cap.hits)
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("dry-run output MUST be JSON; got %q", stdout)
	}
	body, ok := env["body"].(map[string]any)
	if !ok {
		t.Fatalf("dry-run body MUST be an object; got %#v", env["body"])
	}
	if body["domain"] != "mapillary.com" || body["expires_in"] != "7d" {
		t.Errorf("dry-run body MUST show canonical domain and expires_in; got %#v", body)
	}
	if _, ok := body["from"]; ok {
		t.Errorf("dry-run body MUST NOT show legacy from field; got %#v", body)
	}
	if _, ok := body["ttl"]; ok {
		t.Errorf("dry-run body MUST NOT show legacy ttl field; got %#v", body)
	}
}

func TestExpectRejectsUnsupportedDurationBeforeHTTP(t *testing.T) {
	_, cap, err := runCmd(t, []string{"expect", "--from", "mapillary.com", "--window", "forever"}, 200, `{}`)
	if err == nil {
		t.Fatalf("unsupported duration MUST return a usage error")
	}
	if got := exitcode.ExitCodeFor(err); got != exitcode.Usage {
		t.Errorf("unsupported duration MUST be a usage error; got exit %d", got)
	}
	if cap.hits != 0 {
		t.Errorf("unsupported duration MUST NOT make an HTTP call; server saw %d hit(s)", cap.hits)
	}
	if !strings.Contains(err.Error(), "unsupported expectation duration") {
		t.Errorf("unsupported duration error MUST explain supported units; got: %v", err)
	}
}

// ── whitelist create ───────────────────────────────────────────

func TestWhitelistCreatePostsToWhitelistEndpoint(t *testing.T) {
	_, cap, err := runCmd(t, []string{"whitelist", "create", "alice@example.com"}, 201, `{"id":"w_1"}`)
	if err != nil {
		t.Fatalf("whitelist create returned error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/whitelist" {
		t.Errorf("expected POST /whitelist; got %s %s", cap.method, cap.path)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["sender"] != "alice@example.com" {
		t.Errorf("body.sender MUST carry the positional arg; got: %v", body["sender"])
	}
	if _, has := body["pattern"]; has {
		t.Errorf("body MUST NOT send legacy pattern field; got: %v", body)
	}
}

func TestWhitelistCreateSenderFlagPostsSender(t *testing.T) {
	_, cap, err := runCmd(t, []string{"whitelist", "create", "--sender", "billing@example.com"}, 201, `{"id":"w_1"}`)
	if err != nil {
		t.Fatalf("whitelist create --sender returned error: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["sender"] != "billing@example.com" {
		t.Errorf("body.sender MUST carry --sender; got: %v", body["sender"])
	}
}

func TestWhitelistCreateSenderRegexWithSubjectRegex(t *testing.T) {
	_, cap, err := runCmd(t,
		[]string{"whitelist", "create", "--sender-regex", `@stripe\.com\z`, "--subject-regex", `\Ainvoice `},
		201, `{"id":"w_1"}`)
	if err != nil {
		t.Fatalf("whitelist create --sender-regex returned error: %v", err)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["sender_regex"] != `@stripe\.com\z` {
		t.Errorf("body.sender_regex MUST carry --sender-regex; got: %v", body["sender_regex"])
	}
	if body["subject_regex"] != `\Ainvoice ` {
		t.Errorf("body.subject_regex MUST carry --subject-regex; got: %v", body["subject_regex"])
	}
	if _, has := body["sender"]; has {
		t.Errorf("regex path MUST NOT also send sender; got: %v", body)
	}
}

func TestWhitelistCreateDryRunUsesSenderPayload(t *testing.T) {
	stdout, cap, err := runCmd(t,
		[]string{"whitelist", "create", "user@example.com", "--dry-run"},
		200, `{}`)
	if err != nil {
		t.Fatalf("whitelist create --dry-run returned error: %v", err)
	}
	if cap.hits != 0 {
		t.Fatalf("--dry-run MUST NOT make an HTTP call; server saw %d hit(s)", cap.hits)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("--dry-run output MUST be JSON; got %q", stdout)
	}
	body, _ := env["body"].(map[string]any)
	if body["sender"] != "user@example.com" {
		t.Errorf("dry-run body.sender MUST carry positional sender; got: %v", body["sender"])
	}
	if _, has := body["pattern"]; has {
		t.Errorf("dry-run body MUST NOT send legacy pattern field; got: %v", body)
	}
}

func TestWhitelistCreateRejectsMissingOrAmbiguousSenderBeforeHTTP(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"missing", []string{"whitelist", "create"}, "requires an exact sender"},
		{"positional_and_sender", []string{"whitelist", "create", "a@example.com", "--sender", "b@example.com"}, "only one sender source"},
		{"sender_and_regex", []string{"whitelist", "create", "--sender", "a@example.com", "--sender-regex", ".*@example.com"}, "only one sender source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, cap, err := runCmdSplit(t, tc.argv, 201, `{"id":"w_1"}`)
			if err == nil {
				t.Fatalf("expected validation error")
			}
			if cap.hits != 0 {
				t.Fatalf("validation MUST happen before HTTP; server saw %d hit(s)", cap.hits)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr should explain validation failure %q; got %q", tc.want, stderr)
			}
		})
	}
}

// `whitelist add` is the deprecated v0.2 spelling — kept hidden for one release.
func TestWhitelistAddAliasStillWorks(t *testing.T) {
	_, cap, err := runCmd(t, []string{"whitelist", "add", "legacy@example.com"}, 201, `{"id":"w_1"}`)
	if err != nil {
		t.Fatalf("whitelist add alias MUST still work; got error: %v", err)
	}
	if cap.method != http.MethodPost || cap.path != "/whitelist" {
		t.Errorf("alias MUST hit POST /whitelist; got %s %s", cap.method, cap.path)
	}
	var body map[string]any
	_ = json.Unmarshal(cap.body, &body)
	if body["sender"] != "legacy@example.com" {
		t.Errorf("alias MUST send sender payload; got %v", body)
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
	if !strings.Contains(combined, "new-email") || !strings.Contains(combined, "reply-all") {
		t.Errorf("deprecation warning MUST point at new-email + reply-all; got: %s", combined)
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

// ── quarantine escalate guidance ───────────────────────────────

func TestQuarantineEscalateGivesDashboardGuidanceWithoutHeldContent(t *testing.T) {
	_, _, err := runCmd(t, []string{"quarantine", "escalate", "msg_42"}, 200, `{}`)
	if err == nil {
		t.Errorf("quarantine escalate MUST return explicit dashboard guidance; got nil")
	}
	if err == nil {
		return
	}
	msg := err.Error()
	for _, want := range []string{"dashboard", "owner", "message msg_42", "hidden"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message MUST mention %q; got: %v", want, err)
		}
	}
	for _, forbidden := range []string{"http://", "https://", "verification link", "body:"} {
		if strings.Contains(strings.ToLower(msg), forbidden) {
			t.Errorf("error message MUST NOT expose quarantined content markers %q; got: %v", forbidden, err)
		}
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
		{"msg_get", []string{"msg", "get", "abc"}},
		{"expect", []string{"expect", "--from", "ceo@example.com", "--subject-regex", "wire"}},
		{"whitelist_create", []string{"whitelist", "create", "alice@x.com"}},
		{"send", []string{"send", "--to", "x@y.com", "--subject", "s", "--body", "b"}},
		{"new_email", []string{"new-email", "--to", "x@y.com", "--subject", "s", "--body", "b"}},
		// reply-all needs a successful GET /msg/:id BEFORE the POST /send,
		// so it doesn't fit the single-stub harness this test uses. The
		// reply-specific tests in reply_all_test.go exercise the 4xx path
		// against a route-aware stub instead.
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
		{"msg_get", []string{"msg", "get", "abc"}},
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
		status     int
		body       string
		wantErr    bool
		wantStdout string // substring expected on stdout (or "" for "any")
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
		{405, ``, true, ""}, // The v0.2.0 bug shape.
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

// ── Idempotency-Key + dry-run (principle 4) ────────────────────

// --idempotency-key MUST attach the Idempotency-Key request header so
// the server's IdempotentCreate concern can replay the cached response
// on retry. We test on every mutation command because each plumbs the
// shared mutationFlags helper independently.
func TestIdempotencyKeyHeaderIsSent(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"register", []string{"register", "--address", "x", "--email", "y@z.com", "--idempotency-key", "k-reg"}},
		{"new_email", []string{"new-email", "--to", "x@y", "--subject", "s", "--body", "b", "--idempotency-key", "k-ne"}},
		{"expect", []string{"expect", "--from", "agent@example.com", "--idempotency-key", "k-ex"}},
		{"whitelist_create", []string{"whitelist", "create", "alice@x.com", "--idempotency-key", "k-wl"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, cap, err := runCmd(t, c.argv, 201, `{"id":"x"}`)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if cap.idempotencyKey == "" {
				t.Errorf("%s: Idempotency-Key header MUST be set; got empty", c.name)
			}
		})
	}
}

// --dry-run MUST short-circuit the HTTP call and emit a structured
// envelope with `dry_run: true`, the would-be method/path, and the body.
func TestDryRunSkipsHTTPAndEmitsEnvelope(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		wantPath string
	}{
		{"register", []string{"register", "--address", "x", "--email", "y@z.com", "--dry-run"}, "/agents/register"},
		{"new_email", []string{"new-email", "--to", "a@b.c", "--subject", "s", "--body", "b", "--dry-run"}, "/send"},
		{"expect", []string{"expect", "--from", "agent@example.com", "--dry-run"}, "/expectations"},
		{"whitelist_create", []string{"whitelist", "create", "alice@x.com", "--dry-run"}, "/whitelist"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, cap, err := runCmd(t, c.argv, 200, `{}`)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if cap.hits != 0 {
				t.Errorf("%s: --dry-run MUST NOT make an HTTP call; server saw %d hit(s)", c.name, cap.hits)
			}
			var env map[string]any
			if err := json.Unmarshal([]byte(stdout), &env); err != nil {
				t.Fatalf("%s: --dry-run output MUST be JSON; got %q", c.name, stdout)
			}
			if env["dry_run"] != true {
				t.Errorf("%s: dry_run MUST be true; got %v", c.name, env["dry_run"])
			}
			if env["path"] != c.wantPath {
				t.Errorf("%s: path = %v; want %v", c.name, env["path"], c.wantPath)
			}
			if env["method"] != "POST" {
				t.Errorf("%s: method = %v; want POST", c.name, env["method"])
			}
		})
	}
}

// ── Exit-code taxonomy (principle 2) ───────────────────────────
//
// Every API command must surface a typed *exitcode.Error so main.go
// can map HTTP status classes to stable exit codes — agents branch
// on these. Without this, `if mmb msg get $id; then ...; fi` is
// almost useless because every error is exit 1.

func TestExitCodes_ByHTTPStatusClass(t *testing.T) {
	type tc struct {
		name   string
		argv   []string
		status int
		want   int
	}
	cases := []tc{
		{"401_to_Auth", []string{"msg", "get", "x"}, 401, exitcode.Auth},
		{"403_to_Auth", []string{"inbox", "list"}, 403, exitcode.Auth},
		{"404_to_NotFound", []string{"msg", "get", "x"}, 404, exitcode.NotFound},
		{"409_to_Conflict", []string{"register", "--address", "x", "--email", "y@z.com"}, 409, exitcode.Conflict},
		{"422_to_Validation", []string{"new-email", "--to", "x@y.com", "--subject", "s", "--body", "b"}, 422, exitcode.Validation},
		{"500_to_Server", []string{"inbox", "list"}, 500, exitcode.Server},
		{"503_to_Server", []string{"whitelist", "create", "x@y"}, 503, exitcode.Server},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, err := runCmdSplit(t, c.argv, c.status, `{"error":"x"}`)
			if err == nil {
				t.Fatalf("status %d MUST yield an error", c.status)
			}
			if got := exitcode.ExitCodeFor(err); got != c.want {
				t.Errorf("status %d → exit %d; want %d", c.status, got, c.want)
			}
		})
	}
}

// Usage errors (missing required flags, bad enum values) MUST exit 2
// so shells can branch on "user fixed the invocation" vs "server is sick".
func TestExitCodes_UsageErrorExitsTwo(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"new_email_missing_required", []string{"new-email"}},
		{"register_missing_required", []string{"register"}},
		{"expect_missing_from", []string{"expect"}},
		{"inbox_list_invalid_state", []string{"inbox", "list", "--state", "secret"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, err := runCmdSplit(t, c.argv, 200, `{}`)
			if err == nil {
				t.Fatalf("usage error MUST be reported as error")
			}
			if got := exitcode.ExitCodeFor(err); got != exitcode.Usage {
				t.Errorf("got exit %d; want Usage(%d)", got, exitcode.Usage)
			}
		})
	}
}

// ── Auth header is set on every authenticated request ─────────

func TestAuthHeaderIsSetOnAuthenticatedRequests(t *testing.T) {
	cases := [][]string{
		{"inbox", "list"},
		{"msg", "get", "x"},
		{"expect", "--from", "agent@example.com"},
		{"whitelist", "create", "x@y"},
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
