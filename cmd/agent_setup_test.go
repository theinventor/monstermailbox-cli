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
)

type agentSetupCapturedRequest struct {
	Method         string
	Path           string
	RawQuery       string
	AuthHeader     string
	ContentType    string
	IdempotencyKey string
	Body           []byte
}

func runAgentSetupScenario(t *testing.T, argv []string, apiKey string, handler http.HandlerFunc) (string, []agentSetupCapturedRequest, error) {
	t.Helper()

	var requests []agentSetupCapturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		requests = append(requests, agentSetupCapturedRequest{
			Method:         r.Method,
			Path:           r.URL.Path,
			RawQuery:       r.URL.RawQuery,
			AuthHeader:     r.Header.Get("Authorization"),
			ContentType:    r.Header.Get("Content-Type"),
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
			Body:           body,
		})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	t.Setenv(client.EnvAPIURL, srv.URL)
	t.Setenv(client.EnvAPIKey, apiKey)
	t.Setenv("MMB_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	root := NewRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(argv)
	err := root.Execute()
	return out.String(), requests, err
}

func decodeAgentSetupReport(t *testing.T, stdout string) setupReport {
	t.Helper()
	var report setupReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("agent-setup stdout MUST be JSON; err=%v stdout=%s", err, stdout)
	}
	return report
}

func TestAgentSetupNoAuthEmitsActionableJSON(t *testing.T) {
	stdout, requests, err := runAgentSetupScenario(t, []string{"agent-setup"}, "", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/version" {
			t.Fatalf("no-auth setup should only hit GET /version; got %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"test"}`)
	})
	if err != nil {
		t.Fatalf("agent-setup without auth should emit needs_input JSON, not error: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected only GET /version; got %d requests: %+v", len(requests), requests)
	}
	report := decodeAgentSetupReport(t, stdout)
	if report.Status != setupStatusNeedsInput || report.OK {
		t.Fatalf("status = %s ok=%v; want needs_input false", report.Status, report.OK)
	}
	if got := report.Stages["profile_auth"].Status; got != setupStatusNeedsInput {
		t.Fatalf("profile_auth status = %s; want needs_input", got)
	}
	if got := report.Stages["human_claim"].Status; got != setupStatusNeedsInput {
		t.Fatalf("human_claim status = %s; want needs_input", got)
	}
	if !strings.Contains(stdout, "mmb auth login --address <local_part> --email <human_owner_email>") {
		t.Errorf("missing auth login next step: %s", stdout)
	}
	if !strings.Contains(stdout, "mmb auth save --profile <profile>") {
		t.Errorf("missing auth save next step: %s", stdout)
	}
}

func TestAgentSetupMapsPendingAdoptionToHumanClaim(t *testing.T) {
	stdout, _, err := runAgentSetupScenario(t, []string{"agent-setup", "--webhook-id", "42"}, "mmb_testkey1234567890", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_, _ = io.WriteString(w, `{"version":"test"}`)
		case "/webhooks":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":"agent_pending_adoption","reason":"owner_unclaimed","message":"human owner must claim and adopt this agent"}`)
		default:
			t.Fatalf("unexpected request after pending adoption: %s %s", r.Method, r.URL.String())
		}
	})
	if err != nil {
		t.Fatalf("pending adoption should emit needs_input JSON, not error: %v", err)
	}
	if strings.Contains(stdout, "mmb_testkey1234567890") {
		t.Fatalf("stdout leaked raw API key: %s", stdout)
	}
	report := decodeAgentSetupReport(t, stdout)
	if got := report.Stages["profile_auth"].Status; got != setupStatusPass {
		t.Fatalf("profile_auth status = %s; want pass", got)
	}
	human := report.Stages["human_claim"]
	if human.Status != setupStatusNeedsInput || human.Code != "owner_unclaimed" {
		t.Fatalf("human_claim = %+v; want needs_input owner_unclaimed", human)
	}
	if !strings.Contains(stdout, "claim/adoption email") {
		t.Errorf("pending adoption output should explain human claim/adoption boundary: %s", stdout)
	}
}

func TestAgentSetupExistingWebhookHappyPath(t *testing.T) {
	stdout, requests, err := runAgentSetupScenario(t,
		[]string{"agent-setup", "--webhook-id", "42", "--idempotency-key", "setup-1"},
		"mmb_testkey1234567890",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/version":
				_, _ = io.WriteString(w, `{"version":"test"}`)
			case "/webhooks":
				_, _ = io.WriteString(w, `{"webhooks":[{"id":"42"}]}`)
			case "/webhooks/42":
				_, _ = io.WriteString(w, `{"id":"42","active":true,"events":["inbox.new"],"url":"https://receiver.example.com/mmb"}`)
			case "/webhooks/42/test_deliveries":
				if r.Method != http.MethodPost {
					t.Fatalf("webhook test method = %s; want POST", r.Method)
				}
				_, _ = io.WriteString(w, `{"ok":true,"webhook_id":"42","event_id":"whtest_1"}`)
			case "/test_emails":
				if r.Method != http.MethodPost {
					t.Fatalf("test email method = %s; want POST", r.Method)
				}
				_, _ = io.WriteString(w, `{"ok":true,"test":true,"message_id":"123","message_source":"test_email","event":"inbox.new","event_id":"msg_123_test_email","data_test":true,"webhook_delivery_expected":true,"next_steps":["mmb msg get 123 --peek"]}`)
			case "/msg/123":
				if r.URL.Query().Get("peek") != "true" {
					t.Fatalf("message fetch must use peek=true; raw query=%q", r.URL.RawQuery)
				}
				_, _ = io.WriteString(w, `{"id":"123","source":"test_email","state":"trusted","work_state":"inbox"}`)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		})
	if err != nil {
		t.Fatalf("happy path returned error: %v\nstdout=%s", err, stdout)
	}
	report := decodeAgentSetupReport(t, stdout)
	if report.Status != setupStatusPass || !report.OK {
		t.Fatalf("status = %s ok=%v; want pass true\nstdout=%s", report.Status, report.OK, stdout)
	}
	for _, stageName := range []string{"profile_auth", "human_claim", "webhook_config", "webhook_synthetic_test", "test_email_send", "message_fetch", "final_result"} {
		if got := report.Stages[stageName].Status; got != setupStatusPass {
			t.Fatalf("%s status = %s; want pass", stageName, got)
		}
	}
	if requests[3].Path != "/webhooks/42/test_deliveries" || requests[3].IdempotencyKey != "setup-1-webhook-test" {
		t.Fatalf("webhook test request/idempotency = %+v", requests[3])
	}
	if requests[4].Path != "/test_emails" || requests[4].IdempotencyKey != "setup-1-test-email" {
		t.Fatalf("test email request/idempotency = %+v", requests[4])
	}
	if strings.TrimSpace(string(requests[4].Body)) != "" {
		t.Fatalf("POST /test_emails should not send caller-controlled body; got %q", string(requests[4].Body))
	}
}

func TestAgentSetupWebhookCreatePostsRecommendedPayload(t *testing.T) {
	var createBody map[string]any
	stdout, _, err := runAgentSetupScenario(t,
		[]string{
			"agent-setup",
			"--webhook-url", "https://receiver.example.com/mmb",
			"--webhook-name", "setup receiver",
			"--event-preset", "quarantine-aware-inbox",
			"--header", "x-openclaw-token: fake-api-token",
			"--auth-bearer", "fake-bearer",
		},
		"mmb_testkey1234567890",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/version":
				_, _ = io.WriteString(w, `{"version":"test"}`)
			case "/webhooks":
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, `{"webhooks":[]}`)
					return
				}
				createBody = decodeBody(t, mustReadBody(t, r))
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"id":"77","active":true,"events":["inbox.new","inbox.quarantined","inbox.released"],"secret":"whsec_fake_once"}`)
			case "/webhooks/77/test_deliveries":
				_, _ = io.WriteString(w, `{"ok":true,"webhook_id":"77","event_id":"whtest_77"}`)
			case "/test_emails":
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"ok":true,"test":true,"message_id":"123","message_source":"test_email","event_id":"msg_123_test_email","webhook_delivery_expected":true}`)
			case "/msg/123":
				_, _ = io.WriteString(w, `{"id":"123","source":"test_email","state":"trusted","work_state":"inbox"}`)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		})
	if err != nil {
		t.Fatalf("webhook create setup returned error: %v\nstdout=%s", err, stdout)
	}
	if createBody["name"] != "setup receiver" || createBody["url"] != "https://receiver.example.com/mmb" {
		t.Fatalf("bad webhook create body: %v", createBody)
	}
	assertBodyEvents(t, mustMarshalBody(t, createBody), []string{"inbox.new", "inbox.quarantined", "inbox.released"})
	headers := createBody["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer fake-bearer" || headers["x-openclaw-token"] != "fake-api-token" {
		t.Fatalf("delivery headers missing from create body: %v", headers)
	}
	report := decodeAgentSetupReport(t, stdout)
	if report.Status != setupStatusPass {
		t.Fatalf("status = %s; want pass\nstdout=%s", report.Status, stdout)
	}
	if _, ok := report.Artifacts["webhook_secret"]; !ok {
		t.Fatalf("webhook create should surface one-time secret artifact with sensitivity metadata")
	}
}

func TestAgentSetupMissingTestEmailCapabilityFailsActionably(t *testing.T) {
	stdout, _, err := runAgentSetupScenario(t,
		[]string{"agent-setup", "--webhook-id", "42"},
		"mmb_testkey1234567890",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/version":
				_, _ = io.WriteString(w, `{"version":"test"}`)
			case "/webhooks":
				_, _ = io.WriteString(w, `{"webhooks":[{"id":"42"}]}`)
			case "/webhooks/42":
				_, _ = io.WriteString(w, `{"id":"42","active":true,"events":["inbox.new"]}`)
			case "/webhooks/42/test_deliveries":
				_, _ = io.WriteString(w, `{"ok":true,"webhook_id":"42","event_id":"whtest_1"}`)
			case "/test_emails":
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"error":"not_found"}`)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		})
	if err != nil {
		t.Fatalf("non-strict missing backend capability should emit fail JSON, not error: %v", err)
	}
	report := decodeAgentSetupReport(t, stdout)
	if report.Status != setupStatusFail {
		t.Fatalf("status = %s; want fail", report.Status)
	}
	stage := report.Stages["test_email_send"]
	if stage.Status != setupStatusFail || stage.Code != "missing_backend_capability" {
		t.Fatalf("test_email_send = %+v; want missing_backend_capability fail", stage)
	}
	if !strings.Contains(stdout, "mmb update") {
		t.Fatalf("missing backend capability should include update/deploy next step: %s", stdout)
	}
}

func TestAgentSetupDryRunPrintsPlannedMutationsWithoutHTTP(t *testing.T) {
	stdout, requests, err := runAgentSetupScenario(t,
		[]string{"agent-setup", "--dry-run", "--webhook-id", "42", "--idempotency-key", "setup-1", "--mark-test-done"},
		"mmb_testkey1234567890",
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("dry-run must not make HTTP requests; got %s %s", r.Method, r.URL.String())
		})
	if err != nil {
		t.Fatalf("dry-run should not error: %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("dry-run made HTTP requests: %+v", requests)
	}
	report := decodeAgentSetupReport(t, stdout)
	if report.Status != setupStatusDryRun {
		t.Fatalf("status = %s; want dry_run", report.Status)
	}
	planned, ok := report.Artifacts["planned_requests"].([]any)
	if !ok || len(planned) < 4 {
		t.Fatalf("planned_requests missing or too short: %v", report.Artifacts["planned_requests"])
	}
	for _, want := range []string{
		"/webhooks/42/test_deliveries",
		"/test_emails",
		"/msg/{message_id_from_test_email}/work_state",
		"setup-1-test-email",
		"setup-1-test-message-done",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry-run output missing %q: %s", want, stdout)
		}
	}
}

func mustReadBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustMarshalBody(t *testing.T, body map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
