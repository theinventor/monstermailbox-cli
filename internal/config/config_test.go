package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MMB_CONFIG", filepath.Join(dir, "no-such-file.json"))

	f, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for missing file", err)
	}
	if len(f.Profiles) != 0 {
		t.Errorf("expected empty Profiles, got %v", f.Profiles)
	}
	if f.DefaultProfile != "" {
		t.Errorf("expected empty default, got %q", f.DefaultProfile)
	}
}

func TestPutAndGet_FirstProfileBecomesDefault(t *testing.T) {
	f := &File{}
	f.Put("alpha", Profile{APIKey: "k1", APIURL: "https://a"})
	if f.DefaultProfile != "alpha" {
		t.Errorf("first profile should auto-become default, got %q", f.DefaultProfile)
	}

	f.Put("beta", Profile{APIKey: "k2", APIURL: "https://b"})
	if f.DefaultProfile != "alpha" {
		t.Errorf("default should NOT change when adding a second profile, got %q", f.DefaultProfile)
	}

	got, ok := f.Get("beta")
	if !ok || got.APIKey != "k2" {
		t.Errorf("Get(beta) = %v, %v", got, ok)
	}

	got, ok = f.Get("") // "" means default
	if !ok || got.APIKey != "k1" {
		t.Errorf("Get default = %v, %v", got, ok)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mmb", "config.json")
	t.Setenv("MMB_CONFIG", path)

	f := &File{}
	f.Put("alpha", Profile{APIKey: "secret", APIURL: "https://a", AgentAddress: "alpha@x.com"})
	if err := f.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Mode must be 0600 (readable only by owner; key is sensitive).
	// Windows doesn't honor POSIX permission bits — files come back
	// as 0666 regardless of how they were written. The on-disk ACL
	// model is different there; skip the bit-level assert.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if mode := st.Mode().Perm(); mode != 0o600 {
			t.Errorf("file mode = %o, want 0600", mode)
		}
	}

	// Reload and compare.
	g, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if g.DefaultProfile != "alpha" {
		t.Errorf("default lost on round-trip: %q", g.DefaultProfile)
	}
	got, ok := g.Get("alpha")
	if !ok || got.APIKey != "secret" {
		t.Errorf("profile alpha lost: %v, %v", got, ok)
	}
}

func TestDelete_PromotesAlphabeticallyFirstNewDefault(t *testing.T) {
	f := &File{}
	f.Put("alpha", Profile{APIKey: "k1"})
	f.Put("beta", Profile{APIKey: "k2"})
	f.Put("gamma", Profile{APIKey: "k3"})
	// alpha is default. Delete alpha → beta (next alphabetical) becomes default.
	if !f.Delete("alpha") {
		t.Fatal("Delete(alpha) returned false")
	}
	if f.DefaultProfile != "beta" {
		t.Errorf("after deleting default, expected beta, got %q", f.DefaultProfile)
	}
}

func TestDelete_LastProfileLeavesEmptyDefault(t *testing.T) {
	f := &File{}
	f.Put("only", Profile{APIKey: "k"})
	f.Delete("only")
	if f.DefaultProfile != "" {
		t.Errorf("after deleting last profile, default should be empty, got %q", f.DefaultProfile)
	}
}

func TestSetDefault_ErrorsOnUnknown(t *testing.T) {
	f := &File{}
	f.Put("alpha", Profile{APIKey: "k"})
	if err := f.SetDefault("nope"); err == nil {
		t.Error("SetDefault should refuse unknown profile")
	}
}
