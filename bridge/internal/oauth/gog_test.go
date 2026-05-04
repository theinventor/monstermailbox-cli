package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeGogBinary writes a tiny shell script that satisfies
// `gog auth tokens export <email> --out <file>` by writing
// `{"refresh_token": "...", "scopes": [...]}` to <file>.
func writeFakeGogBinary(t *testing.T, refreshToken string, scopes []string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fake-gog-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "gog")
	bytes, _ := json.Marshal(map[string]any{
		"refresh_token": refreshToken,
		"scopes":        scopes,
	})
	script := `#!/bin/sh
# fake gog: handles auth tokens export <email> --out <file>
out=""
for arg in "$@"; do
  if [ "$prev" = "--out" ]; then out="$arg"; fi
  prev="$arg"
done
cat > "$out" <<'PAYLOAD'
` + string(bytes) + `
PAYLOAD
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeGogCredentials(t *testing.T, clientID, clientSecret, tokenURL string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gog-creds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "credentials.json")
	bytes, _ := json.Marshal(map[string]any{
		"installed": map[string]any{
			"client_id":     clientID,
			"client_secret": clientSecret,
			"token_uri":     tokenURL,
		},
	})
	if err := os.WriteFile(path, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSource_RefreshesAndCachesToken(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "client-x" {
			t.Errorf("missing client_id form field")
		}
		if r.Form.Get("refresh_token") != "rt-abc" {
			t.Errorf("missing refresh_token form field")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at-1","expires_in":3600,"scope":"https://www.googleapis.com/auth/pubsub https://www.googleapis.com/auth/gmail.modify"}`))
	}))
	defer tokenServer.Close()

	credsPath := writeGogCredentials(t, "client-x", "secret-x", tokenServer.URL)
	t.Setenv("GOG_CREDENTIALS_PATH", credsPath)
	t.Setenv("MMB_BRIDGE_GOG_BIN", writeFakeGogBinary(t, "rt-abc",
		[]string{"https://www.googleapis.com/auth/pubsub", "https://www.googleapis.com/auth/gmail.modify"}))

	src, err := LoadSourceFromGog(context.Background(), "you@gmail.com")
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("first token: %v", err)
	}
	if tok != "at-1" {
		t.Fatalf("expected at-1, got %q", tok)
	}
	// Second call within expiry should NOT hit the token endpoint
	// again; we assert by closing the server and re-calling.
	tokenServer.Close()
	tok, err = src.Token(context.Background())
	if err != nil || tok != "at-1" {
		t.Fatalf("cached token should be served on second call; got %q err=%v", tok, err)
	}
}

func TestSource_MissingPubSubScope_ProducesActionableError(t *testing.T) {
	t.Setenv("GOG_CREDENTIALS_PATH", writeGogCredentials(t, "c", "s", "http://unused"))
	t.Setenv("MMB_BRIDGE_GOG_BIN", writeFakeGogBinary(t, "rt",
		[]string{"https://www.googleapis.com/auth/gmail.modify"})) // no pubsub

	_, err := LoadSourceFromGog(context.Background(), "you@gmail.com")
	if err == nil {
		t.Fatal("expected error when pubsub scope is missing")
	}
	if !strings.Contains(err.Error(), "pubsub") || !strings.Contains(err.Error(), "--extra-scopes") {
		t.Fatalf("error should reference pubsub + the re-auth command; got: %v", err)
	}
}

func TestSource_TokenEndpointErrorSurfaced(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"insufficient_scope"}`))
	}))
	defer tokenServer.Close()

	t.Setenv("GOG_CREDENTIALS_PATH", writeGogCredentials(t, "c", "s", tokenServer.URL))
	t.Setenv("MMB_BRIDGE_GOG_BIN", writeFakeGogBinary(t, "rt", []string{"https://www.googleapis.com/auth/pubsub"}))

	src, err := LoadSourceFromGog(context.Background(), "you@gmail.com")
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.Token(context.Background())
	if err == nil {
		t.Fatal("expected error from token endpoint")
	}
	if !strings.Contains(err.Error(), "pubsub") {
		t.Fatalf("error should reference pubsub: %v", err)
	}
}
