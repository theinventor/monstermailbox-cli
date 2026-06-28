package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPatchHermesConfig_AddsManagedKeysPreservingComments(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	orig := "# my hermes config\nmodel: opus\nplugins:\n  enabled:\n    - other-plugin  # keep me\n"
	if err := os.WriteFile(cfg, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := patchHermesConfig(cfg); err != nil {
		t.Fatalf("patch: %v", err)
	}

	// Backup preserved verbatim.
	if bak, _ := os.ReadFile(cfg + ".bak"); string(bak) != orig {
		t.Error("backup missing or altered")
	}

	b, _ := os.ReadFile(cfg)
	out := string(b)
	if !strings.Contains(out, "# my hermes config") {
		t.Error("top-of-file comment must be preserved")
	}
	if !strings.Contains(out, "keep me") {
		t.Error("inline comment on existing entry must be preserved")
	}

	var got map[string]any
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatalf("result not valid YAML: %v", err)
	}
	plugins := got["plugins"].(map[string]any)
	enabled := toStrings(plugins["enabled"])
	if !contains(enabled, "other-plugin") || !contains(enabled, "monstermailbox") {
		t.Errorf("plugins.enabled must keep other-plugin and add monstermailbox; got %v", enabled)
	}
	pts := got["platform_toolsets"].(map[string]any)
	if ts := toStrings(pts["monstermailbox"]); len(ts) != 1 || ts[0] != "hermes-cli" {
		t.Errorf("platform_toolsets.monstermailbox must be [hermes-cli]; got %v", pts["monstermailbox"])
	}
	if al := toStrings(got["command_allowlist"]); !contains(al, "mmb *") {
		t.Errorf("command_allowlist must contain \"mmb *\"; got %v", al)
	}
	if got["model"] != "opus" {
		t.Errorf("unrelated top-level key must be preserved; model=%v", got["model"])
	}
}

func TestPatchHermesConfig_Idempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfg, []byte("plugins: {}\n"), 0o644)

	for i := 0; i < 3; i++ {
		if err := patchHermesConfig(cfg); err != nil {
			t.Fatalf("patch %d: %v", i, err)
		}
	}
	b, _ := os.ReadFile(cfg)
	var got map[string]any
	yaml.Unmarshal(b, &got)
	enabled := toStrings(got["plugins"].(map[string]any)["enabled"])
	allow := toStrings(got["command_allowlist"])
	if countOf(enabled, "monstermailbox") != 1 {
		t.Errorf("monstermailbox must appear once in plugins.enabled; got %v", enabled)
	}
	if countOf(allow, "mmb *") != 1 {
		t.Errorf("\"mmb *\" must appear once in command_allowlist; got %v", allow)
	}
}

func TestHermesInstall_FailsWithoutConfigYaml(t *testing.T) {
	dir := t.TempDir() // no config.yaml
	root := NewRootCmd()
	root.SetArgs([]string{"hermes", "install", "--home", dir})
	if err := root.Execute(); err == nil {
		t.Error("install MUST fail when config.yaml is absent")
	}
}

func TestHermesInstall_WritesPluginAndPatches(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("plugins: {}\n"), 0o644)
	root := NewRootCmd()
	root.SetArgs([]string{"hermes", "install", "--home", dir})
	if err := root.Execute(); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, f := range []string{"plugin.yaml", "__init__.py", "adapter.py"} {
		if _, err := os.Stat(filepath.Join(dir, "plugins", "monstermailbox", f)); err != nil {
			t.Errorf("expected plugin file %s: %v", f, err)
		}
	}
	// The installer records the absolute mmb path so the adapter doesn't depend
	// on the gateway's PATH.
	mp := filepath.Join(dir, "plugins", "monstermailbox", "mmb_path")
	b, err := os.ReadFile(mp)
	if err != nil {
		t.Fatalf("expected mmb_path to be recorded: %v", err)
	}
	if p := strings.TrimSpace(string(b)); p == "" || !filepath.IsAbs(p) {
		t.Errorf("mmb_path should hold an absolute path; got %q", string(b))
	}
}

// Guard: `mmb msg get` has no --json flag (it emits JSON with --peek). A stray
// --json there makes the adapter fail to fetch the message. Keep both embedded
// adapters off it.
func TestEmbeddedAdaptersDoNotPassJsonToMsgGet(t *testing.T) {
	hermes, err := hermesPluginFS.ReadFile(hermesPluginEmbedRoot + "/adapter.py")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hermes), `"get", msg_id, "--peek", "--json"`) {
		t.Error("hermes adapter passes invalid --json to `mmb msg get`")
	}
	oc, err := openclawPluginFS.ReadFile(openclawPluginEmbedRoot + "/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(oc), `"get", String(id), "--peek", "--json"`) {
		t.Error("openclaw adapter passes invalid --json to `mmb msg get`")
	}
}

func toStrings(v any) []string {
	var out []string
	if s, ok := v.([]any); ok {
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
	}
	return out
}

func contains(s []string, v string) bool { return countOf(s, v) > 0 }

func countOf(s []string, v string) int {
	n := 0
	for _, e := range s {
		if e == v {
			n++
		}
	}
	return n
}

// Guard: the Hermes adapter is reply-only — it filters gateway status notices in
// send() and does NOT register as a cron/home-delivery channel (which caused
// "no home channel" errors and routed notices over email).
func TestHermesAdapterIsReplyOnly(t *testing.T) {
	b, err := hermesPluginFS.ReadFile(hermesPluginEmbedRoot + "/adapter.py")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "_is_status_notice") {
		t.Error("adapter must filter gateway status notices in send()")
	}
	if strings.Contains(s, "cron_deliver_env_var=") {
		t.Error("adapter must NOT register cron_deliver_env_var= (no home/cron channel)")
	}
}

// Guard: the adapter must filter Hermes streaming-progress lines (e.g.
// "⏳ Working — 3 min — iteration 23/90, receiving stream response") so a turn
// interrupted mid-stream never emails a progress spinner instead of a real reply.
func TestHermesAdapterFiltersProgressSpinner(t *testing.T) {
	b, err := hermesPluginFS.ReadFile(hermesPluginEmbedRoot + "/adapter.py")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "⏳") {
		t.Error("adapter must treat the ⏳ progress spinner as a status notice")
	}
	if !strings.Contains(s, "receiving stream response") {
		t.Error("adapter must filter the streaming-progress heartbeat phrase")
	}
}
