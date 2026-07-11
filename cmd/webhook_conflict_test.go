package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/client"
)

func TestDisableConflictingInboxWebhooks(t *testing.T) {
	var patched []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/webhooks":
			_ = json.NewEncoder(w).Encode(map[string]any{"webhooks": []map[string]any{
				{"id": "10", "name": "old bridge", "active": true, "events": []any{"inbox.new"}},
				{"id": "11", "name": "bounces only", "active": true, "events": []any{"outbound.bounced"}},
				{"id": "12", "name": "already off", "active": false, "events": []any{"inbox.new"}},
				{"id": "13", "name": "all events", "active": true, "events": []any{"*"}},
			}})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/webhooks/"):
			patched = append(patched, strings.TrimPrefix(r.URL.Path, "/webhooks/"))
			_, _ = w.Write([]byte(`{"webhook":{"active":false}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	t.Setenv(client.EnvAPIURL, srv.URL)
	t.Setenv(client.EnvAPIKey, "test-key")
	t.Setenv("MMB_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	var out bytes.Buffer
	disableConflictingInboxWebhooks(&out, client.New())

	// Only the ACTIVE inbox.new (10) and active all-events (13) webhooks get
	// paused; the outbound-only (11) and already-inactive (12) are left alone.
	has := func(id string) bool {
		for _, p := range patched {
			if p == id {
				return true
			}
		}
		return false
	}
	if len(patched) != 2 || !has("10") || !has("13") {
		t.Fatalf("expected PATCH to exactly 10 and 13, got %v", patched)
	}
	if has("11") {
		t.Error("must NOT touch the outbound.bounced webhook (11)")
	}
	if has("12") {
		t.Error("must NOT touch the already-inactive webhook (12)")
	}
	if !strings.Contains(out.String(), "id 10") || !strings.Contains(out.String(), "--active=true") {
		t.Errorf("notice should name the disabled webhook and the re-enable command; got:\n%s", out.String())
	}
}
