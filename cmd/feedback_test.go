package cmd

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// runFeedback runs `mmb feedback <args>` against a fresh tree with
// the test's MMB_FEEDBACK_PATH pointed at a tempfile. Returns
// (stdout, stderr, error).
func runFeedback(t *testing.T, args []string, envs map[string]string) (string, string, error) {
	t.Helper()
	for k, v := range envs {
		t.Setenv(k, v)
	}
	return runMmbCmd(t, append([]string{"feedback"}, args...), nil)
}

func TestFeedback_BareFormRecordsToLocalLog(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "feedback.jsonl")
	t.Setenv("MMB_FEEDBACK_PATH", path)

	stdout, _, err := runFeedback(t, []string{"the --tier flag rejects 'enterprise'"}, nil)
	if err != nil {
		t.Fatalf("feedback record: %v", err)
	}

	// stdout MUST be valid JSON describing what happened.
	var resp map[string]any
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout MUST be JSON; got %q", stdout)
	}
	if resp["recorded"] != true {
		t.Errorf("recorded MUST be true; got %v", resp["recorded"])
	}
	if resp["upstream"] != nil {
		t.Errorf("with no MONSTERMAILBOX_FEEDBACK_ENDPOINT, upstream MUST be nil; got %v", resp["upstream"])
	}

	// File MUST exist with one JSONL line.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("feedback file not written: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	count := 0
	for sc.Scan() {
		count++
		var entry feedbackEntry
		if err := json.Unmarshal([]byte(sc.Text()), &entry); err != nil {
			t.Errorf("entry MUST be valid JSON; got: %s", sc.Text())
		}
		if !strings.Contains(entry.Text, "tier") {
			t.Errorf("entry text MUST be preserved; got: %q", entry.Text)
		}
		if entry.Timestamp == "" {
			t.Errorf("entry MUST carry a timestamp")
		}
	}
	if count != 1 {
		t.Errorf("expected 1 line in feedback log; got %d", count)
	}
}

func TestFeedback_ExplicitRecordSubcommand(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "feedback.jsonl")
	t.Setenv("MMB_FEEDBACK_PATH", path)

	if _, _, err := runFeedback(t, []string{"record", "explicit form"}, nil); err != nil {
		t.Fatalf("record: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "explicit form") {
		t.Errorf("record subcommand MUST write to the same log; got %s", raw)
	}
}

// Empty text must error out with Usage rather than recording an
// empty row — empty rows are noise.
func TestFeedback_EmptyTextIsUsageError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("MMB_FEEDBACK_PATH", filepath.Join(tmp, "feedback.jsonl"))

	_, _, err := runFeedback(t, []string{"   "}, nil)
	if err == nil {
		t.Fatalf("empty text MUST error")
	}
}

// `mmb feedback list` returns JSON with newest-first entries.
func TestFeedback_ListReturnsNewestFirstJSON(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "feedback.jsonl")
	t.Setenv("MMB_FEEDBACK_PATH", path)

	for _, txt := range []string{"first", "second", "third"} {
		if _, _, err := runFeedback(t, []string{txt}, nil); err != nil {
			t.Fatal(err)
		}
	}

	stdout, _, err := runFeedback(t, []string{"list"}, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("list MUST emit JSON; got %q", stdout)
	}
	entries, _ := resp["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("list MUST return all entries; got %d", len(entries))
	}
	first, _ := entries[0].(map[string]any)
	if first["text"] != "third" {
		t.Errorf("newest MUST come first; got %v", first["text"])
	}
}

// Upstream POST: when MONSTERMAILBOX_FEEDBACK_ENDPOINT is set, the
// entry MUST also be sent over HTTP and the response MUST report it.
func TestFeedback_UpstreamPostWhenConfigured(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("upstream MUST receive POST; got %s", r.Method)
		}
		posts.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	tmp := t.TempDir()
	t.Setenv("MMB_FEEDBACK_PATH", filepath.Join(tmp, "feedback.jsonl"))

	stdout, _, err := runFeedback(t, []string{"shipped via upstream"},
		map[string]string{"MONSTERMAILBOX_FEEDBACK_ENDPOINT": srv.URL})
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}
	if posts.Load() != 1 {
		t.Errorf("upstream MUST receive exactly 1 POST; got %d", posts.Load())
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("stdout MUST be JSON; got %q", stdout)
	}
	upstream, _ := resp["upstream"].(map[string]any)
	if upstream["posted"] != true {
		t.Errorf("response MUST report upstream.posted=true; got %v", resp["upstream"])
	}
}

// Upstream failure MUST NOT fail the command — local write succeeded,
// the user's feedback is captured, the upstream channel is best-effort.
func TestFeedback_UpstreamFailureDoesNotFailCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	tmp := t.TempDir()
	t.Setenv("MMB_FEEDBACK_PATH", filepath.Join(tmp, "feedback.jsonl"))

	stdout, _, err := runFeedback(t, []string{"upstream is sick"},
		map[string]string{"MONSTERMAILBOX_FEEDBACK_ENDPOINT": srv.URL})
	if err != nil {
		t.Errorf("upstream 502 MUST NOT fail the command (local write is the contract); got %v", err)
	}
	var resp map[string]any
	_ = json.Unmarshal([]byte(stdout), &resp)
	upstream, _ := resp["upstream"].(map[string]any)
	if upstream["posted"] != false {
		t.Errorf("response MUST report upstream.posted=false on 502; got %v", resp["upstream"])
	}
}

// `mmb feedback path` prints the JSONL location — useful for shell
// tooling that wants to grep / tail the log.
func TestFeedback_PathPrintsLogLocation(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "feedback.jsonl")
	t.Setenv("MMB_FEEDBACK_PATH", path)

	stdout, _, err := runFeedback(t, []string{"path"}, nil)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if strings.TrimSpace(stdout) != path {
		t.Errorf("path MUST equal env-overridden value; got %q", stdout)
	}
}
