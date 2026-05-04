package forward

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/gogcli"
	mmblog "github.com/theinventor/monstermailbox-cli/bridge/internal/log"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/matcher"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/policy"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/pubsub"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/state"
)

// fakeGogBinary writes a script that dispatches the same way the
// gogcli tests do. Kept in this package so we don't import a
// _test.go from gogcli (Go doesn't allow that).
func fakeGogBinary(t *testing.T, history, metadata, rawMime string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fake-gog-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "gog")
	rawJSON := `{"raw":"` + base64.URLEncoding.EncodeToString([]byte(rawMime)) + `"}`
	script := `#!/bin/sh
sub=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "gmail" ]; then sub="$arg"; break; fi
  prev="$arg"
done
case "$sub" in
  history) cat <<'PAYLOAD'
` + history + `
PAYLOAD
  ;;
  get)
    fmt=""
    p=""
    for a in "$@"; do
      if [ "$p" = "--format" ]; then fmt="$a"; fi
      p="$a"
    done
    if [ "$fmt" = "raw" ]; then
      cat <<'PAYLOAD'
` + rawJSON + `
PAYLOAD
    else
      cat <<'PAYLOAD'
` + metadata + `
PAYLOAD
    fi
  ;;
  watch) echo '{}' ;;
  *) echo "unknown sub: $sub" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

type capturedInbound struct {
	body []byte
}

func newAPIServer(t *testing.T, captured *capturedInbound, postCount *int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bridge/inbound" {
			body, _ := io.ReadAll(r.Body)
			captured.body = body
			atomic.AddInt64(postCount, 1)
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"inbound_email_id":"42"}`))
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
	}))
}

func TestForward_HappyPath_Forwards_Match(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mmb-forward-")
	t.Setenv("MMB_BRIDGE_DIR", dir)
	defer os.RemoveAll(dir)

	rawMIME := "From: notifications@github.com\r\nTo: agent@monstermailbox.com\r\nSubject: PR #42\r\n\r\nbody"
	hist := `[{"historyId":"200","messagesAdded":[{"message":{"id":"m1"}}]}]`
	md := `{"payload":{"headers":[{"name":"From","value":"GitHub <notifications@github.com>"},{"name":"Subject","value":"PR #42"}]}}`
	t.Setenv("MMB_BRIDGE_GOG_BIN", fakeGogBinary(t, hist, md, rawMIME))

	captured := &capturedInbound{}
	postCount := int64(0)
	apiServer := newAPIServer(t, captured, &postCount)
	defer apiServer.Close()

	pol := policy.NewStore()
	pol.Apply(api.PolicyResponse{
		Whitelist: []api.WhitelistEntry{{ID: "1", Sender: "notifications@github.com"}},
	})

	st := &state.State{}
	st.SetHistoryID("100") // pre-populated so Handle() runs the full pipeline

	logger, err := mmblog.New(false, mmblog.LevelDebug)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	fwd := &Forwarder{
		API:     api.New(apiServer.URL, "mmb_test"),
		Gog:     gogcli.New("you@gmail.com"),
		Policy:  pol,
		Matcher: matcher.New(),
		State:   st,
		Logger:  logger,
	}

	if err := fwd.Handle(context.Background(), pubsub.PushPayload{
		EmailAddress: "you@gmail.com",
		HistoryID:    "200",
	}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if postCount != 1 {
		t.Fatalf("expected 1 inbound POST, got %d", postCount)
	}
	if !strings.Contains(string(captured.body), "PR #42") {
		t.Fatalf("body did not include MIME subject: %s", captured.body)
	}
	if !st.SeenMessage("m1") {
		t.Fatal("dedup ring should record forwarded message")
	}
	if st.HistoryID() != "200" {
		t.Fatalf("history cursor should advance to 200, got %q", st.HistoryID())
	}
}

func TestForward_DropsNonMatchingSenders(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mmb-forward-")
	t.Setenv("MMB_BRIDGE_DIR", dir)
	defer os.RemoveAll(dir)

	hist := `[{"historyId":"200","messagesAdded":[{"message":{"id":"m1"}}]}]`
	md := `{"payload":{"headers":[{"name":"From","value":"spam@phishing.io"},{"name":"Subject","value":"hi"}]}}`
	t.Setenv("MMB_BRIDGE_GOG_BIN", fakeGogBinary(t, hist, md, "should-never-be-fetched"))

	captured := &capturedInbound{}
	postCount := int64(0)
	apiServer := newAPIServer(t, captured, &postCount)
	defer apiServer.Close()

	pol := policy.NewStore()
	pol.Apply(api.PolicyResponse{Whitelist: []api.WhitelistEntry{{Sender: "notifications@github.com"}}})
	st := &state.State{}
	st.SetHistoryID("100")
	logger, _ := mmblog.New(false, mmblog.LevelDebug)
	defer logger.Close()

	fwd := &Forwarder{API: api.New(apiServer.URL, "k"), Gog: gogcli.New("you@gmail.com"),
		Policy: pol, Matcher: matcher.New(), State: st, Logger: logger}
	if err := fwd.Handle(context.Background(), pubsub.PushPayload{HistoryID: "200"}); err != nil {
		t.Fatal(err)
	}
	if postCount != 0 {
		t.Fatalf("non-matching message must NOT POST inbound; got %d posts", postCount)
	}
	if !st.SeenMessage("m1") {
		t.Fatal("drops should be marked seen too (dedup re-eval)")
	}
}

func TestForward_FirstRunBookmarksHistoryWithoutProcessing(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mmb-forward-")
	t.Setenv("MMB_BRIDGE_DIR", dir)
	defer os.RemoveAll(dir)

	t.Setenv("MMB_BRIDGE_GOG_BIN", fakeGogBinary(t, "[]", "{}", "{}")) // never invoked
	postCount := int64(0)
	captured := &capturedInbound{}
	apiServer := newAPIServer(t, captured, &postCount)
	defer apiServer.Close()

	pol := policy.NewStore()
	pol.Apply(api.PolicyResponse{Whitelist: []api.WhitelistEntry{{Sender: "x@y.com"}}})
	st := &state.State{} // empty history
	logger, _ := mmblog.New(false, mmblog.LevelDebug)
	defer logger.Close()

	fwd := &Forwarder{API: api.New(apiServer.URL, "k"), Gog: gogcli.New("you@gmail.com"),
		Policy: pol, Matcher: matcher.New(), State: st, Logger: logger}
	if err := fwd.Handle(context.Background(), pubsub.PushPayload{HistoryID: "9999"}); err != nil {
		t.Fatal(err)
	}
	if postCount != 0 {
		t.Fatalf("first run should not forward; got %d posts", postCount)
	}
	if st.HistoryID() != "9999" {
		t.Fatalf("first run should bookmark to push.HistoryID; got %q", st.HistoryID())
	}
}

func TestExtractAddress(t *testing.T) {
	cases := map[string]string{
		"alice@example.com":              "alice@example.com",
		"Alice <alice@example.com>":      "alice@example.com",
		"\"Alice\" <alice@EXAMPLE.com>":  "alice@example.com",
		"  bob@bob.com  ":                "bob@bob.com",
	}
	for input, want := range cases {
		if got := extractAddress(input); got != want {
			t.Errorf("extractAddress(%q) = %q, want %q", input, got, want)
		}
	}
}

// silence unused json import for builds with no json fixtures.
var _ = json.Marshal
