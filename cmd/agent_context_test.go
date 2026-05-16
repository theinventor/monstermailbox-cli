package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/config"
)

// runAgentContext runs `mmb agent-context` against a fresh tree and
// parses the JSON result into a generic map. Returns parse error if
// the output isn't valid JSON — failing this is itself a test signal,
// because a non-JSON `agent-context` defeats the whole principle.
func runAgentContext(t *testing.T) map[string]any {
	t.Helper()
	root := NewRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"agent-context"})
	if err := root.Execute(); err != nil {
		t.Fatalf("agent-context returned error: %v", err)
	}
	var ctx map[string]any
	if err := json.Unmarshal(out.Bytes(), &ctx); err != nil {
		t.Fatalf("agent-context output MUST be valid JSON; err=%v output=%q", err, out.String())
	}
	return ctx
}

// The schema_version is what agents pin against. If a future change
// renames or removes a field, the version MUST bump and this test
// MUST be updated to match — that's how we detect contract drift.
func TestAgentContext_HasSchemaVersion(t *testing.T) {
	ctx := runAgentContext(t)
	if got := ctx["schema_version"]; got != AgentContextSchemaVersion {
		t.Errorf("schema_version = %v; want %q", got, AgentContextSchemaVersion)
	}
}

// Every top-level command (other than hidden aliases) MUST be present.
// If you add a new top-level command, add it here too.
func TestAgentContext_EnumeratesEveryTopLevelCommand(t *testing.T) {
	ctx := runAgentContext(t)
	commands, ok := ctx["commands"].(map[string]any)
	if !ok {
		t.Fatalf("commands MUST be a map; got %T", ctx["commands"])
	}
	want := []string{
		"agent-context", "skill", "auth", "whoami", "register", "update",
		"inbox", "msg", "expect", "whitelist", "send",
		// Outbound surface: reply-all primary, narrow secondary,
		// new-email tertiary. The pre-v0.7 reply-to-email alias is
		// hidden and MUST NOT show up here.
		"reply-all", "reply-not-all-with-custom-recipients", "new-email",
		"guidance", "contact", "agent-product-feedback", "quarantine",
		"feedback",
	}
	for _, name := range want {
		if _, present := commands[name]; !present {
			t.Errorf("commands MUST include %q (full set: %v)", name, keysOf(commands))
		}
	}
}

func TestAgentContext_ContactSurfaceDocumentsSupportAndProductFeedback(t *testing.T) {
	ctx := runAgentContext(t)
	commands := ctx["commands"].(map[string]any)
	contact := commands["contact"].(map[string]any)
	subs := contact["subcommands"].(map[string]any)

	support := subs["support"].(map[string]any)
	supportFlags := support["flags"].(map[string]any)
	for _, name := range []string{"--subject", "--text", "--idempotency-key", "--dry-run"} {
		if _, has := supportFlags[name]; !has {
			t.Errorf("contact support agent-context MUST expose %s; flags=%v", name, keysOf(supportFlags))
		}
	}
	if args := support["args"]; args != "[text]" {
		t.Errorf("contact support args = %v; want [text]", args)
	}

	product := subs["product-feedback"].(map[string]any)
	productFlags := product["flags"].(map[string]any)
	for _, name := range []string{"--text", "--idempotency-key", "--dry-run"} {
		if _, has := productFlags[name]; !has {
			t.Errorf("contact product-feedback agent-context MUST expose %s; flags=%v", name, keysOf(productFlags))
		}
	}
	if args := product["args"]; args != "[text]" {
		t.Errorf("contact product-feedback args = %v; want [text]", args)
	}
}

// Hidden deprecated aliases (`msg show`, `whitelist add`) MUST NOT
// appear — agents discovering the surface should only see canonical
// names. The aliases still work for back-compat but are invisible
// to introspection.
func TestAgentContext_OmitsHiddenAliases(t *testing.T) {
	ctx := runAgentContext(t)
	commands := ctx["commands"].(map[string]any)
	msg, _ := commands["msg"].(map[string]any)
	subs, _ := msg["subcommands"].(map[string]any)
	if _, has := subs["show"]; has {
		t.Errorf("agent-context MUST NOT expose hidden alias `msg show`")
	}
	if _, has := subs["get"]; !has {
		t.Errorf("agent-context MUST expose canonical `msg get`")
	}

	wl, _ := commands["whitelist"].(map[string]any)
	wlSubs, _ := wl["subcommands"].(map[string]any)
	if _, has := wlSubs["add"]; has {
		t.Errorf("agent-context MUST NOT expose hidden alias `whitelist add`")
	}
	if _, has := wlSubs["create"]; !has {
		t.Errorf("agent-context MUST expose canonical `whitelist create`")
	}

	// reply-to-email is the pre-v0.7 spelling of reply-all and stays
	// as a hidden alias; agent-context MUST NOT surface it.
	if _, has := commands["reply-to-email"]; has {
		t.Errorf("agent-context MUST NOT expose hidden alias `reply-to-email`")
	}
}

func TestAgentContext_WhitelistCreateDocumentsSenderFlags(t *testing.T) {
	ctx := runAgentContext(t)
	commands := ctx["commands"].(map[string]any)
	wl := commands["whitelist"].(map[string]any)
	wlSubs := wl["subcommands"].(map[string]any)
	create := wlSubs["create"].(map[string]any)
	flags := create["flags"].(map[string]any)

	for _, name := range []string{"--sender", "--sender-regex", "--subject-regex"} {
		if _, has := flags[name]; !has {
			t.Errorf("whitelist create agent-context MUST expose %s; flags=%v", name, keysOf(flags))
		}
	}
	if args := create["args"]; args != "[<sender>]" {
		t.Errorf("whitelist create args = %v; want [<sender>]", args)
	}
}

// Enums surface (principle 3 — errors that teach AND principle 7 —
// machine introspection) — agents should be able to discover
// inbox_state values without parsing a help string.
func TestAgentContext_EnumeratesInboxStates(t *testing.T) {
	ctx := runAgentContext(t)
	enumsAny, ok := ctx["enums"].(map[string]any)
	if !ok {
		t.Fatalf("enums MUST be present and a map; got %T", ctx["enums"])
	}
	statesAny, ok := enumsAny["inbox_state"].([]any)
	if !ok || len(statesAny) != 3 {
		t.Fatalf("enums.inbox_state MUST be a 3-element array; got %v", enumsAny["inbox_state"])
	}
	want := map[string]bool{"trusted": true, "quarantined": true, "rejected": true}
	for _, v := range statesAny {
		s, _ := v.(string)
		if !want[s] {
			t.Errorf("unexpected inbox_state value %q", s)
		}
	}
}

func TestAgentContext_EnumeratesExitCodes(t *testing.T) {
	ctx := runAgentContext(t)
	codes, ok := ctx["exit_codes"].(map[string]any)
	if !ok {
		t.Fatalf("exit_codes MUST be present; got %T", ctx["exit_codes"])
	}
	for _, want := range []string{"0", "2", "3", "4", "5"} {
		if _, ok := codes[want]; !ok {
			t.Errorf("exit_codes MUST include %q", want)
		}
	}
}

func TestAgentContext_ExposesGlobalProfileFlag(t *testing.T) {
	ctx := runAgentContext(t)
	flags, ok := ctx["global_flags"].(map[string]any)
	if !ok {
		t.Fatalf("global_flags MUST be present; got %T", ctx["global_flags"])
	}
	profile, ok := flags["--profile"].(map[string]any)
	if !ok {
		t.Fatalf("global_flags MUST include --profile; got %v", flags)
	}
	if profile["type"] != "string" || profile["usage"] == nil {
		t.Errorf("--profile entry MUST include string type and usage; got %v", profile)
	}
}

// Saved profile names MUST surface so agents can pass --profile
// without parsing config.json directly.
func TestAgentContext_ListsSavedProfiles(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)

	f := &config.File{}
	f.Put("alpha", config.Profile{APIURL: "https://a", APIKey: "k1"})
	f.Put("bravo", config.Profile{APIURL: "https://b", APIKey: "k2"})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	ctx := runAgentContext(t)
	profiles, ok := ctx["available_profiles"].([]any)
	if !ok {
		t.Fatalf("available_profiles MUST be a slice; got %T", ctx["available_profiles"])
	}
	got := map[string]bool{}
	for _, p := range profiles {
		s, _ := p.(string)
		got[s] = true
	}
	if !got["alpha"] || !got["bravo"] {
		t.Errorf("available_profiles must include alpha + bravo; got %v", profiles)
	}
}

// The feedback_upstream endpoint signals to agents whether their
// `mmb feedback` calls reach the maintainers or only the local log.
// Default (env unset) is null.
func TestAgentContext_FeedbackUpstreamReflectsEnvVar(t *testing.T) {
	ctx := runAgentContext(t)
	endpoints, _ := ctx["endpoints"].(map[string]any)
	if v, ok := endpoints["feedback_upstream"]; !ok {
		t.Errorf("endpoints.feedback_upstream MUST be present (null when unset)")
	} else if v != nil {
		t.Errorf("with env unset, feedback_upstream MUST be null; got %v", v)
	}

	t.Setenv("MONSTERMAILBOX_FEEDBACK_ENDPOINT", "https://maintainers.example.com/cli-feedback")
	ctx = runAgentContext(t)
	endpoints, _ = ctx["endpoints"].(map[string]any)
	if v, _ := endpoints["feedback_upstream"].(string); v != "https://maintainers.example.com/cli-feedback" {
		t.Errorf("feedback_upstream did not pick up env var; got %v", endpoints["feedback_upstream"])
	}
}

func TestAgentContext_ExposesContactEndpoints(t *testing.T) {
	ctx := runAgentContext(t)
	endpoints, _ := ctx["endpoints"].(map[string]any)
	if got := endpoints["product_feedback"]; got != "/agent_product_feedback" {
		t.Errorf("product_feedback endpoint = %v; want /agent_product_feedback", got)
	}
	if got := endpoints["support_intake"]; got != supportIntakePath {
		t.Errorf("support_intake endpoint = %v; want %s", got, supportIntakePath)
	}
}

// Sanity: every flag entry must carry "type" and "usage". Without those,
// the document doesn't actually help an agent construct an invocation.
func TestAgentContext_FlagsHaveTypeAndUsage(t *testing.T) {
	ctx := runAgentContext(t)
	commands := ctx["commands"].(map[string]any)
	inbox, _ := commands["inbox"].(map[string]any)
	subs, _ := inbox["subcommands"].(map[string]any)
	list, _ := subs["list"].(map[string]any)
	flags, _ := list["flags"].(map[string]any)
	state, ok := flags["--state"].(map[string]any)
	if !ok {
		t.Fatalf("inbox list flags MUST include --state; got %v", flags)
	}
	if state["type"] == nil || state["usage"] == nil {
		t.Errorf("--state entry MUST include type+usage; got %v", state)
	}
}

// keysOf is a test-only helper for prettier failure messages.
func keysOf(m map[string]any) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return strings.Join(ks, ",")
}
