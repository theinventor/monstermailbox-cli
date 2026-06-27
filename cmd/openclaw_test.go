package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func TestPatchOpenClawConfig_AddsEntryAndPathPreservingOthers(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "openclaw.json")
	orig := `{"version":1,"plugins":{"entries":{"telegram":{"enabled":true}}}}`
	if err := os.WriteFile(cfg, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "extensions", "monstermailbox")

	if err := patchOpenClawConfig(cfg, dest, map[string]any{"state": "trusted"}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	// Backup written with original bytes.
	bak, err := os.ReadFile(cfg + ".bak")
	if err != nil || string(bak) != orig {
		t.Errorf("backup missing or altered: err=%v", err)
	}

	got := readJSON(t, cfg)
	plugins := got["plugins"].(map[string]any)
	entries := plugins["entries"].(map[string]any)
	if _, ok := entries["telegram"]; !ok {
		t.Error("existing telegram entry must be preserved")
	}
	mm, ok := entries["monstermailbox"].(map[string]any)
	if !ok || mm["enabled"] != true {
		t.Fatalf("monstermailbox entry not enabled: %v", entries["monstermailbox"])
	}
	if got["version"] != float64(1) {
		t.Errorf("top-level keys must be preserved; version=%v", got["version"])
	}
	paths := plugins["load"].(map[string]any)["paths"].([]any)
	if len(paths) != 1 || paths[0] != dest {
		t.Errorf("load.paths should contain dest exactly once; got %v", paths)
	}
}

func TestPatchOpenClawConfig_IdempotentPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "openclaw.json")
	os.WriteFile(cfg, []byte(`{"plugins":{}}`), 0o644)
	dest := filepath.Join(dir, "extensions", "monstermailbox")

	for i := 0; i < 3; i++ {
		if err := patchOpenClawConfig(cfg, dest, map[string]any{"state": "trusted"}); err != nil {
			t.Fatalf("patch %d: %v", i, err)
		}
	}
	got := readJSON(t, cfg)
	paths := got["plugins"].(map[string]any)["load"].(map[string]any)["paths"].([]any)
	if len(paths) != 1 {
		t.Errorf("re-running install MUST NOT duplicate load.paths; got %d entries", len(paths))
	}
}

func TestWriteEmbeddedPlugin_WritesAllAssets(t *testing.T) {
	dest := t.TempDir()
	if err := writeEmbeddedPlugin(openclawPluginFS, openclawPluginEmbedRoot, dest); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, f := range []string{"index.js", "openclaw.plugin.json", "package.json"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("expected %s to be written: %v", f, err)
		}
	}
	// The manifest must declare the monstermailbox plugin id.
	manifest := readJSON(t, filepath.Join(dest, "openclaw.plugin.json"))
	if manifest["id"] != "monstermailbox" {
		t.Errorf("manifest id = %v, want monstermailbox", manifest["id"])
	}
}

func TestOpenClawInstall_DryRunMakesNoChanges(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "openclaw.json")
	os.WriteFile(cfg, []byte(`{"plugins":{}}`), 0o644)

	root := NewRootCmd()
	root.SetArgs([]string{"openclaw", "install", "--home", dir, "--dry-run"})
	if err := root.Execute(); err != nil {
		t.Fatalf("dry-run errored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "extensions", "monstermailbox")); err == nil {
		t.Error("dry-run MUST NOT write plugin files")
	}
	if _, err := os.Stat(cfg + ".bak"); err == nil {
		t.Error("dry-run MUST NOT touch the config (no backup)")
	}
}

func TestOpenClawInstall_FailsWithoutOpenClawJSON(t *testing.T) {
	dir := t.TempDir() // no openclaw.json
	root := NewRootCmd()
	root.SetArgs([]string{"openclaw", "install", "--home", dir})
	if err := root.Execute(); err == nil {
		t.Error("install MUST fail when openclaw.json is absent")
	}
}
