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

	// display.platforms.monstermailbox must register the minimal/non-interactive
	// tier so Hermes never emails heartbeats/progress/interim chatter.
	display := got["display"].(map[string]any)
	plats := display["platforms"].(map[string]any)
	mmb := plats["monstermailbox"].(map[string]any)
	if mmb["long_running_notifications"] != false {
		t.Errorf("long_running_notifications must be false (no ⏳ heartbeat); got %v", mmb["long_running_notifications"])
	}
	if mmb["interim_assistant_messages"] != false {
		t.Errorf("interim_assistant_messages must be false; got %v", mmb["interim_assistant_messages"])
	}
	if mmb["tool_progress"] != "off" {
		t.Errorf("tool_progress must be the string \"off\" (not bool false); got %#v", mmb["tool_progress"])
	}
	if mmb["busy_ack_detail"] != false || mmb["streaming"] != false {
		t.Errorf("busy_ack_detail and streaming must be false; got %v / %v", mmb["busy_ack_detail"], mmb["streaming"])
	}

	// compression.codex_gpt55_autoraise off — suppresses the gpt-5.5 caps-context
	// notice that has no per-platform gate.
	comp := got["compression"].(map[string]any)
	if comp["codex_gpt55_autoraise"] != false {
		t.Errorf("compression.codex_gpt55_autoraise must be false; got %v", comp["codex_gpt55_autoraise"])
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

// A leftover backup dir declaring name: monstermailbox shadows the real plugin
// because Hermes registers every manifest under plugins/ (last wins). Install
// must evict it from the scan path.
func TestHermesInstall_EvictsShadowingBackupDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("plugins: {}\n"), 0o644)

	// Simulate a deploy-script backup left inside the scan path.
	shadow := filepath.Join(dir, "plugins", "monstermailbox.backup.20260711T195455Z")
	os.MkdirAll(shadow, 0o755)
	os.WriteFile(filepath.Join(shadow, "plugin.yaml"), []byte("name: monstermailbox\nversion: 0.1.0\n"), 0o644)
	os.WriteFile(filepath.Join(shadow, "adapter.py"), []byte("# old\n"), 0o644)

	// An unrelated plugin must be left alone.
	other := filepath.Join(dir, "plugins", "some-other-plugin")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "plugin.yaml"), []byte("name: some-other-plugin\n"), 0o644)

	root := NewRootCmd()
	root.SetArgs([]string{"hermes", "install", "--home", dir, "--force"})
	if err := root.Execute(); err != nil {
		t.Fatalf("install: %v", err)
	}

	if _, err := os.Stat(shadow); !os.IsNotExist(err) {
		t.Error("shadowing backup dir must be evicted from the plugin scan path")
	}
	if _, err := os.Stat(filepath.Join(dir, "plugin-archive", "monstermailbox.backup.20260711T195455Z")); err != nil {
		t.Errorf("evicted shadow must land in plugin-archive/: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("unrelated plugin must NOT be touched")
	}
	// The real plugin must still be in place.
	if _, err := os.Stat(filepath.Join(dir, "plugins", "monstermailbox", "adapter.py")); err != nil {
		t.Error("the real monstermailbox plugin must remain installed")
	}
}

func TestHermesDoctor_EvictsShadowAndReportsClean(t *testing.T) {
	dir := t.TempDir()
	// A real install must exist for doctor to run.
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("plugins: {}\n"), 0o644)
	real := filepath.Join(dir, "plugins", "monstermailbox")
	os.MkdirAll(real, 0o755)
	os.WriteFile(filepath.Join(real, "plugin.yaml"), []byte("name: monstermailbox\nversion: 0.5.0\n"), 0o644)

	// First run with a shadow present → evicts it.
	shadow := filepath.Join(dir, "plugins", "monstermailbox.old")
	os.MkdirAll(shadow, 0o755)
	os.WriteFile(filepath.Join(shadow, "plugin.yaml"), []byte("name: MonsterMailbox\n"), 0o644) // case-insensitive

	root := NewRootCmd()
	root.SetArgs([]string{"hermes", "doctor", "--home", dir})
	if err := root.Execute(); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if _, err := os.Stat(shadow); !os.IsNotExist(err) {
		t.Error("doctor must evict the shadow dir")
	}

	// Second run with nothing to fix → must succeed (idempotent).
	root = NewRootCmd()
	root.SetArgs([]string{"hermes", "doctor", "--home", dir})
	if err := root.Execute(); err != nil {
		t.Fatalf("doctor (clean run): %v", err)
	}
}

func TestHermesDoctor_FailsWithoutInstalledPlugin(t *testing.T) {
	dir := t.TempDir() // no plugin installed
	root := NewRootCmd()
	root.SetArgs([]string{"hermes", "doctor", "--home", dir})
	if err := root.Execute(); err == nil {
		t.Error("doctor MUST fail when no monstermailbox plugin is installed")
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

// Guard: the Hermes adapter is reply-only — it does NOT register as a cron/home
// channel ("no home channel" errors), and it must NOT carry a content-based
// status-notice filter. Non-reply surfaces are suppressed by design via the
// minimal display tier (asserted in TestPatchHermesConfig), so the brittle
// emoji/phrase filter must stay gone.
func TestHermesAdapterIsReplyOnly(t *testing.T) {
	b, err := hermesPluginFS.ReadFile(hermesPluginEmbedRoot + "/adapter.py")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "cron_deliver_env_var=") {
		t.Error("adapter must NOT register cron_deliver_env_var= (no home/cron channel)")
	}
	if strings.Contains(s, "_is_status_notice") || strings.Contains(s, "_STATUS_NOTICE_PHRASES") {
		t.Error("content-based status filter must be gone — suppression is by display tier, not string matching")
	}
	// By-design suppression of the "📬 No home channel" prompt (set the env var
	// Hermes checks, rather than filtering the notice text).
	if !strings.Contains(s, "MONSTERMAILBOX_HOME_CHANNEL") {
		t.Error("adapter must set MONSTERMAILBOX_HOME_CHANNEL to suppress the no-home-channel prompt by design")
	}
}
