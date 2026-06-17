package cmd

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

func TestMessagesListHitsMessagesWithParticipantFilters(t *testing.T) {
	stdout, cap, err := runCmd(t, []string{
		"messages", "list",
		"--participant", "Person@Example.COM",
		"--state", "quarantined",
		"--work-state", "blocked",
		"--limit", "25",
		"--cursor", "opaque-token",
	}, http.StatusOK, `{"messages":[],"next_cursor":null}`)
	if err != nil {
		t.Fatalf("messages list returned error: %v", err)
	}
	if cap.method != http.MethodGet || cap.path != "/messages" {
		t.Fatalf("expected GET /messages; got %s %s", cap.method, cap.path)
	}
	if cap.authHeader != "Bearer mmb_testkey1234567890" {
		t.Fatalf("expected Bearer auth; got %q", cap.authHeader)
	}

	q, err := url.ParseQuery(cap.rawQuery)
	if err != nil {
		t.Fatalf("raw query should parse: %v", err)
	}
	assertQueryValue(t, q, "participant", "person@example.com")
	assertQueryValue(t, q, "state", "quarantined")
	assertQueryValue(t, q, "work_state", "blocked")
	assertQueryValue(t, q, "limit", "25")
	assertQueryValue(t, q, "cursor", "opaque-token")

	if !strings.Contains(stdout, `"messages": []`) {
		t.Fatalf("stdout should preserve backend JSON; got %q", stdout)
	}
}

func TestMessagesListRequiresValidParticipantBeforeRequest(t *testing.T) {
	cases := [][]string{
		{"messages", "list"},
		{"messages", "list", "--participant", "not an email"},
		{"messages", "list", "--participant", "Person <person@example.com>"},
	}
	for _, argv := range cases {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			_, _, cap, err := runCmdSplit(t, argv, http.StatusOK, `{}`)
			if err == nil {
				t.Fatalf("messages list with invalid participant should fail")
			}
			if cap.hits != 0 {
				t.Fatalf("invalid participant should make no HTTP request; got %d", cap.hits)
			}
			if got := exitcode.ExitCodeFor(err); got != exitcode.Usage {
				t.Fatalf("exit code = %d; want Usage(%d)", got, exitcode.Usage)
			}
		})
	}
}

func TestMessagesListRejectsInvalidEnumsBeforeRequest(t *testing.T) {
	cases := [][]string{
		{"messages", "list", "--participant", "person@example.com", "--state", "secret"},
		{"messages", "list", "--participant", "person@example.com", "--work-state", "secret"},
	}
	for _, argv := range cases {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			_, stderr, cap, err := runCmdSplit(t, argv, http.StatusOK, `{}`)
			if err == nil {
				t.Fatalf("messages list with invalid enum should fail")
			}
			if cap.hits != 0 {
				t.Fatalf("invalid enum should make no HTTP request; got %d", cap.hits)
			}
			if !strings.Contains(stderr, "secret") {
				t.Fatalf("error should name invalid value; stderr=%q", stderr)
			}
		})
	}
}

func assertQueryValue(t *testing.T, q url.Values, key, want string) {
	t.Helper()
	if got := q.Get(key); got != want {
		t.Fatalf("query %s = %q; want %q (full query %v)", key, got, want, q)
	}
}
