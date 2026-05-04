package config

import (
	"os"
	"path/filepath"
	"testing"
)

func tmpHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mmb-bridge-cfg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MMB_BRIDGE_DIR", dir)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestSaveLoadRoundTrip(t *testing.T) {
	tmpHome(t)
	want := &Config{
		APIBaseURL:    "https://app.example.com",
		APIKey:        "mmb_abc",
		AgentEmail:    "alpha@monstermailbox.com",
		GoogleAccount: "you@gmail.com",
		GCPProject:    "my-gcp",
		PubSubTopic:   "gmail-events",
		PubSubSub:     "mmb-bridge-pull",
		LocalOnly:     true,
		LogLevel:      "debug",
	}
	if err := Save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if *got != *want {
		t.Fatalf("mismatch\n got: %#v\nwant: %#v", *got, *want)
	}
}

func TestLoad_NotInitialized(t *testing.T) {
	tmpHome(t)
	_, err := Load()
	if err != ErrNotInitialized {
		t.Fatalf("expected ErrNotInitialized, got %v", err)
	}
}

func TestSave_FileMode(t *testing.T) {
	dir := tmpHome(t)
	if err := Save(&Config{APIKey: "mmb_x"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config.json must be 0600; got %v", info.Mode().Perm())
	}
}

func TestSave_AtomicReplace(t *testing.T) {
	dir := tmpHome(t)
	// Pre-populate so we test replace semantics.
	if err := Save(&Config{APIKey: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(&Config{APIKey: "new"}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "new" {
		t.Fatalf("expected api_key=new, got %q", got.APIKey)
	}
	// No leftover .tmp file.
	if _, err := os.Stat(filepath.Join(dir, "config.json.tmp")); err == nil {
		t.Fatalf("config.json.tmp leaked")
	}
}
