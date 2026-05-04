package policy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
)

func TestSync_AppliesNewerVersion(t *testing.T) {
	hits := int64(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		v := atomic.AddInt64(&hits, 1)
		_ = json.NewEncoder(w).Encode(api.PolicyResponse{
			Version:   int(v),
			Whitelist: []api.WhitelistEntry{{Sender: "a@b.com"}},
		})
	}))
	defer server.Close()

	s := NewStore()
	c := api.New(server.URL, "mmb_x")

	if err := s.Sync(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	snap, _, _ := s.Snapshot()
	if snap.Version != 1 || len(snap.Whitelist) != 1 {
		t.Fatalf("first sync mismatch: %#v", snap)
	}

	if err := s.Sync(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	snap, _, _ = s.Snapshot()
	if snap.Version != 2 {
		t.Fatalf("second sync should advance: %#v", snap)
	}
}

func TestSync_DoesNotDowngradeOnOlderVersion(t *testing.T) {
	versions := []int{3, 1} // first sync gets v3; second sync gets v1
	idx := int64(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := atomic.AddInt64(&idx, 1) - 1
		_ = json.NewEncoder(w).Encode(api.PolicyResponse{
			Version:   versions[i],
			Whitelist: []api.WhitelistEntry{{Sender: "x@y.com"}},
		})
	}))
	defer server.Close()

	s := NewStore()
	c := api.New(server.URL, "mmb_x")
	_ = s.Sync(context.Background(), c)
	_ = s.Sync(context.Background(), c)
	snap, _, _ := s.Snapshot()
	if snap.Version != 3 {
		t.Fatalf("must not downgrade from v3 to v1; got version=%d", snap.Version)
	}
}

func TestSync_RetainsPreviousOnNetworkError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.PolicyResponse{Version: 5, Whitelist: []api.WhitelistEntry{{Sender: "a@b.com"}}})
	}))
	s := NewStore()
	c := api.New(server.URL, "mmb_x")
	_ = s.Sync(context.Background(), c)
	server.Close() // simulate network failure

	if err := s.Sync(context.Background(), c); err == nil {
		t.Fatal("expected sync error after server close")
	}
	snap, _, syncErr := s.Snapshot()
	if syncErr == nil {
		t.Fatal("snapshot syncErr should be non-nil after failed sync")
	}
	if snap.Version != 5 || len(snap.Whitelist) != 1 {
		t.Fatalf("transient failure must not drop policy; got %#v", snap)
	}
}

func TestLoadLocal_ArrayShape(t *testing.T) {
	dir, _ := os.MkdirTemp("", "mmb-policy-")
	t.Setenv("MMB_BRIDGE_DIR", dir)
	defer os.RemoveAll(dir)

	if err := SaveLocal([]api.WhitelistEntry{{Sender: "a@b.com"}, {SenderRegex: `^x@.*\.io$`}}); err != nil {
		t.Fatal(err)
	}
	store, err := LoadLocal()
	if err != nil {
		t.Fatal(err)
	}
	snap, _, _ := store.Snapshot()
	if len(snap.Whitelist) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(snap.Whitelist))
	}
}
