package gogcli

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGog writes a shell script that dispatches on subcommand and
// echoes canned JSON. Tests inject it via $MMB_BRIDGE_GOG_BIN.
func fakeGog(t *testing.T, history, metadata, raw string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "fake-gog-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "gog")
	script := `#!/bin/sh
# Walk args; the subcommand we care about is whatever follows
# 'gmail'. Shifted arguments include '-a <account> -j gmail …'.
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
    # The 4th positional arg here is the messageId followed by
    # --format <full|metadata|raw>.
    fmt=""
    p=""
    for a in "$@"; do
      if [ "$p" = "--format" ]; then fmt="$a"; fi
      p="$a"
    done
    if [ "$fmt" = "raw" ]; then
      cat <<'PAYLOAD'
` + raw + `
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

func TestHistory_ParsesAddedMessages(t *testing.T) {
	hist := `[{"id":"hist-1","historyId":"123","messagesAdded":[{"message":{"id":"m1","threadId":"t1"}},{"message":{"id":"m2","threadId":"t1"}}]}]`
	t.Setenv("MMB_BRIDGE_GOG_BIN", fakeGog(t, hist, "{}", "{}"))

	c := New("you@gmail.com")
	ids, hi, err := c.History(context.Background(), "100")
	if err != nil {
		t.Fatal(err)
	}
	if hi != "123" {
		t.Fatalf("expected next historyId=123, got %q", hi)
	}
	if len(ids) != 2 || ids[0] != "m1" || ids[1] != "m2" {
		t.Fatalf("unexpected ids %#v", ids)
	}
}

func TestHistory_ObjectShape(t *testing.T) {
	hist := `{"history":[{"id":"hist-1","messagesAdded":[{"message":{"id":"m1"}}]}],"historyId":"999"}`
	t.Setenv("MMB_BRIDGE_GOG_BIN", fakeGog(t, hist, "{}", "{}"))
	c := New("you@gmail.com")
	ids, hi, err := c.History(context.Background(), "0")
	if err != nil {
		t.Fatal(err)
	}
	if hi != "999" || len(ids) != 1 || ids[0] != "m1" {
		t.Fatalf("unexpected: ids=%#v hi=%q", ids, hi)
	}
}

func TestGetMetadata_ParsesFromAndSubject(t *testing.T) {
	md := `{"payload":{"headers":[{"name":"From","value":"Alice <alice@example.com>"},{"name":"Subject","value":"hi"}]}}`
	t.Setenv("MMB_BRIDGE_GOG_BIN", fakeGog(t, "[]", md, "{}"))
	c := New("you@gmail.com")
	out, err := c.GetMetadata(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.From, "alice@example.com") || out.Subject != "hi" {
		t.Fatalf("unexpected metadata: %#v", out)
	}
}

func TestGetRaw_DecodesBase64URL(t *testing.T) {
	mime := "From: a@b.com\r\nSubject: hi\r\n\r\nbody"
	encoded := base64.URLEncoding.EncodeToString([]byte(mime))
	rawJSON := `{"raw":"` + encoded + `"}`
	t.Setenv("MMB_BRIDGE_GOG_BIN", fakeGog(t, "[]", "{}", rawJSON))
	c := New("you@gmail.com")
	got, err := c.GetRaw(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != mime {
		t.Fatalf("decoded mismatch:\n got: %q\nwant: %q", got, mime)
	}
}
