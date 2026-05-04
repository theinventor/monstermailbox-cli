package state

import (
	"os"
	"path/filepath"
	"testing"
)

func tmpHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mmb-bridge-state-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MMB_BRIDGE_DIR", dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestLoadEmptyAndSaveRoundTrip(t *testing.T) {
	tmpHome(t)
	s, err := Load()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if s.HistoryID() != "" {
		t.Fatalf("fresh state should have empty history id; got %q", s.HistoryID())
	}
	s.SetHistoryID("12345")
	s.MarkMessage("msg-a")
	s.MarkMessage("msg-b")
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.HistoryID() != "12345" {
		t.Fatalf("history id round-trip; got %q", got.HistoryID())
	}
	if !got.SeenMessage("msg-a") || !got.SeenMessage("msg-b") {
		t.Fatalf("dedup ring round-trip lost messages")
	}
	if got.SeenMessage("never-saw-this") {
		t.Fatalf("dedup must not match unknown id")
	}
}

func TestRingBound(t *testing.T) {
	tmpHome(t)
	s, _ := Load()
	for i := 0; i < ringCap+50; i++ {
		s.MarkMessage(charID(i))
	}
	if l := len(s.RecentMessageIDs); l != ringCap {
		t.Fatalf("ring should be capped at %d, got %d", ringCap, l)
	}
	if s.SeenMessage(charID(0)) {
		t.Fatalf("oldest entry should have been evicted")
	}
	if !s.SeenMessage(charID(ringCap + 49)) {
		t.Fatalf("newest entry must still be present")
	}
}

func TestSave_FileMode(t *testing.T) {
	dir := tmpHome(t)
	s, _ := Load()
	s.SetHistoryID("9")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state.json must be 0600; got %v", info.Mode().Perm())
	}
}

func charID(i int) string {
	return "msg-" + itoaPad(i)
}

// itoaPad zero-pads an int — tiny stdlib-only stand-in to avoid
// pulling fmt into a tight test.
func itoaPad(i int) string {
	if i == 0 {
		return "0000"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	for len(digits) < 4 {
		digits = append([]byte{'0'}, digits...)
	}
	return string(digits)
}
