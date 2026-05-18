package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/config"
	"github.com/theinventor/monstermailbox-cli/internal/credstore"
)

// runMmbCmd runs an mmb command with stdout captured. Pass envs in.
func runMmbCmd(t *testing.T, args []string, envs map[string]string) (stdout, stderr string, exitErr error) {
	t.Helper()
	for k, v := range envs {
		t.Setenv(k, v)
	}
	root := NewRootCmd()
	var out, err bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&err)
	root.SetArgs(args)
	exitErr = root.Execute()
	return out.String(), err.String(), exitErr
}

// Resolution: --profile flag overrides everything.
func TestAuth_ProfileFlagWinsOverEnv(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)
	t.Setenv("MONSTERMAILBOX_API_KEY", "ENV_KEY_BUT_IGNORED")
	t.Setenv("MONSTERMAILBOX_API_URL", "https://env.example.com")

	// Save a profile.
	f := &config.File{}
	f.Put("alpha", config.Profile{
		APIURL: "https://alpha.example.com",
		APIKey: "PROFILE_ALPHA_KEY_LONG_ENOUGH",
	})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	c := client.NewWithProfile("alpha")
	if c.APIKey != "PROFILE_ALPHA_KEY_LONG_ENOUGH" {
		t.Errorf("explicit --profile should beat env; got APIKey=%q", c.APIKey)
	}
	if c.BaseURL != "https://alpha.example.com" {
		t.Errorf("explicit --profile should set BaseURL from profile; got %q", c.BaseURL)
	}
	if c.Source != "profile:alpha" {
		t.Errorf("Source = %q, want profile:alpha", c.Source)
	}
}

func TestAuth_MissingExplicitProfileDoesNotFallBackToEnv(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)
	t.Setenv("MONSTERMAILBOX_API_KEY", "ENV_KEY_BUT_IGNORED")
	t.Setenv("MONSTERMAILBOX_API_URL", "https://env.example.com")

	f := &config.File{}
	f.Put("alpha", config.Profile{
		APIURL: "https://alpha.example.com",
		APIKey: "PROFILE_ALPHA_KEY_LONG_ENOUGH",
	})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	c := client.NewWithProfile("missing")
	if c.APIKey != "" {
		t.Errorf("missing explicit --profile must not fall back to env/default API key; got %q", c.APIKey)
	}
	if c.Source != "" {
		t.Errorf("missing explicit --profile source = %q, want empty", c.Source)
	}
	if c.BaseURL != "https://env.example.com" {
		t.Errorf("missing explicit --profile should still honor env API URL for diagnostics; got %q", c.BaseURL)
	}
}

// Resolution: when no --profile, env wins over config default.
func TestAuth_EnvWinsOverConfigDefault(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)
	t.Setenv("MONSTERMAILBOX_API_KEY", "ENV_KEY_LONG_ENOUGH")
	t.Setenv("MONSTERMAILBOX_API_URL", "https://env.example.com")

	f := &config.File{}
	f.Put("default", config.Profile{
		APIURL: "https://config.example.com",
		APIKey: "CONFIG_DEFAULT_KEY",
	})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	c := client.New()
	if c.APIKey != "ENV_KEY_LONG_ENOUGH" {
		t.Errorf("env should beat config default; got APIKey=%q", c.APIKey)
	}
	if c.Source != "env" {
		t.Errorf("Source = %q, want env", c.Source)
	}
}

// Resolution: when no --profile, no env, config default is used.
func TestAuth_ConfigDefaultUsedWhenNoEnv(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)
	t.Setenv("MONSTERMAILBOX_API_KEY", "")
	t.Setenv("MONSTERMAILBOX_API_URL", "")

	f := &config.File{}
	f.Put("only", config.Profile{
		APIURL: "https://only.example.com",
		APIKey: "ONLY_PROFILE_KEY_LONG_ENOUGH",
	})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	c := client.New()
	if c.APIKey != "ONLY_PROFILE_KEY_LONG_ENOUGH" {
		t.Errorf("config default should be used; got APIKey=%q", c.APIKey)
	}
	if c.Source != "profile:only" {
		t.Errorf("Source = %q, want profile:only", c.Source)
	}
}

// `mmb auth save` writes a profile and sets it as default on first save.
//
// Storage backend matters here: with --storage=file the api_key lives
// in config.json; with --storage=keychain (or auto when keychain works)
// the api_key field is empty in the file and the secret is retrievable
// via credstore. Both shapes are tested.
func TestAuthSave_FirstProfileBecomesDefault_FileBackend(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)
	t.Setenv("MONSTERMAILBOX_API_KEY", "")

	stdout, _, err := runMmbCmd(t, []string{
		"auth", "save",
		"--profile", "team-bot",
		"--api-key", "mmb_team_bot_key_long",
		"--api-url", "https://api.example.com",
		"--agent-address", "team-bot@example.com",
		"--storage", "file",
	}, nil)
	if err != nil {
		t.Fatalf("auth save: %v", err)
	}
	if !strings.Contains(stdout, `saved profile "team-bot"`) {
		t.Errorf("missing save confirmation: %s", stdout)
	}
	if !strings.Contains(stdout, "storage: file") {
		t.Errorf("missing file backend marker: %s", stdout)
	}
	if !strings.Contains(stdout, "set as default profile") {
		t.Errorf("first save should auto-default: %s", stdout)
	}
	// File backend: api_key persists IN the config file.
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var parsed config.File
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.DefaultProfile != "team-bot" {
		t.Errorf("default not persisted: %q", parsed.DefaultProfile)
	}
	if got := parsed.Profiles["team-bot"].APIKey; got != "mmb_team_bot_key_long" {
		t.Errorf("api_key not persisted in file: got %q", got)
	}
	if got := parsed.Profiles["team-bot"].Backend; got != credstore.BackendFile {
		t.Errorf("backend = %q, want file", got)
	}
}

// Keychain backend: api_key MUST NOT live in the file; secret is
// retrievable via credstore.
func TestAuthSave_KeychainBackend(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)

	_, _, err := runMmbCmd(t, []string{
		"auth", "save",
		"--profile", "kc-bot",
		"--api-key", "mmb_keychain_test_key",
		"--api-url", "https://api.example.com",
		"--storage", "keychain",
	}, nil)
	if err != nil {
		t.Fatalf("auth save --storage=keychain: %v", err)
	}
	parsed, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := parsed.Profiles["kc-bot"]
	if p.APIKey != "" {
		t.Errorf("keychain backend MUST NOT leave api_key in file; got %q", p.APIKey)
	}
	if p.Backend != credstore.BackendKeychain {
		t.Errorf("backend = %q, want keychain", p.Backend)
	}
	// Round-trip via credstore.
	got, err := credstore.Get("kc-bot", credstore.BackendKeychain, "")
	if err != nil {
		t.Fatalf("credstore.Get: %v", err)
	}
	if got != "mmb_keychain_test_key" {
		t.Errorf("keychain roundtrip = %q", got)
	}
	// Client resolution should still surface the secret.
	c := client.NewWithProfile("kc-bot")
	if c.APIKey != "mmb_keychain_test_key" {
		t.Errorf("client APIKey = %q, want secret retrieved from keychain", c.APIKey)
	}
	if c.Backend != credstore.BackendKeychain {
		t.Errorf("client.Backend = %q, want keychain", c.Backend)
	}
}

// `mmb auth save --storage=auto` (no --storage flag) defaults to keychain
// when the keychain is available — which the test environment guarantees
// via the mock installed in TestMain.
func TestAuthSave_AutoDefaultsToKeychain(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)
	t.Setenv("MMB_DISABLE_KEYCHAIN", "")
	t.Setenv("MMB_STORAGE", "")

	_, _, err := runMmbCmd(t, []string{
		"auth", "save",
		"--profile", "auto-bot",
		"--api-key", "mmb_auto_test_key",
		"--api-url", "https://api.example.com",
	}, nil)
	if err != nil {
		t.Fatalf("auth save (auto): %v", err)
	}
	parsed, _ := config.Load()
	if parsed.Profiles["auto-bot"].Backend != credstore.BackendKeychain {
		t.Errorf("auto with available keychain should pick keychain; got %q",
			parsed.Profiles["auto-bot"].Backend)
	}
}

// `mmb auth migrate --profile X` moves a file-backed profile's secret
// to the keychain and clears the file's api_key.
func TestAuthMigrate_MovesFileBackedProfileToKeychain(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)

	// Seed a file-backed profile (legacy shape: Backend == "").
	f := &config.File{}
	f.Put("legacy", config.Profile{
		APIURL: "https://api.example.com",
		APIKey: "mmb_legacy_secret_long",
	})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runMmbCmd(t, []string{"auth", "migrate", "--profile", "legacy"}, nil)
	if err != nil {
		t.Fatalf("auth migrate: %v", err)
	}
	if !strings.Contains(stdout, "migrated → keychain") {
		t.Errorf("missing migration confirmation: %s", stdout)
	}

	// File no longer carries the secret.
	parsed, _ := config.Load()
	p := parsed.Profiles["legacy"]
	if p.APIKey != "" {
		t.Errorf("post-migrate api_key in file = %q; want empty", p.APIKey)
	}
	if p.Backend != credstore.BackendKeychain {
		t.Errorf("post-migrate backend = %q; want keychain", p.Backend)
	}

	// Secret is now in keychain.
	got, err := credstore.Get("legacy", credstore.BackendKeychain, "")
	if err != nil {
		t.Fatalf("credstore.Get post-migrate: %v", err)
	}
	if got != "mmb_legacy_secret_long" {
		t.Errorf("keychain holds %q, want roundtrip", got)
	}
}

// `mmb auth migrate --all` is idempotent: re-running on already-keychained
// profiles produces "skipped" without errors.
func TestAuthMigrate_IdempotentOnAlreadyKeychainedProfiles(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)

	// First save a keychain-backed profile.
	if _, _, err := runMmbCmd(t, []string{
		"auth", "save",
		"--profile", "kc",
		"--api-key", "mmb_kc_secret",
		"--api-url", "https://api.example.com",
		"--storage", "keychain",
	}, nil); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runMmbCmd(t, []string{"auth", "migrate", "--all"}, nil)
	if err != nil {
		t.Fatalf("migrate --all: %v", err)
	}
	if !strings.Contains(stdout, "already on keychain — skipped") {
		t.Errorf("expected idempotent skip, got: %s", stdout)
	}
}

// `mmb auth login` registers + saves in one shot, end-to-end against
// a stubbed /agents/register endpoint.
func TestAuthLogin_RegistersAndSavesProfile(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)
	t.Setenv("MONSTERMAILBOX_API_KEY", "")

	// Stub server returns a 201 with a fake key.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/agents/register" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Make sure auth header is NOT sent (register is public).
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization header should NOT be set on register")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agent_id":           "42",
			"address":            "ada@monstermailbox.com",
			"api_key":            "mmb_login_test_key_long_enough",
			"human_owner_status": "unclaimed",
		})
	}))
	defer server.Close()
	t.Setenv("MONSTERMAILBOX_API_URL", server.URL)

	stdout, _, err := runMmbCmd(t, []string{
		"auth", "login",
		"--address", "ada",
		"--email", "ada@lovelace.dev",
	}, nil)
	if err != nil {
		t.Fatalf("auth login: %v", err)
	}
	if !strings.Contains(stdout, "registered ada@monstermailbox.com") {
		t.Errorf("missing register confirmation: %s", stdout)
	}
	// Profile name defaults to local-part.
	parsed, _ := config.Load()
	if _, ok := parsed.Profiles["ada"]; !ok {
		t.Errorf("expected profile %q to be saved; got %v", "ada", parsed.Names())
	}
	if parsed.DefaultProfile != "ada" {
		t.Errorf("first profile should be default; got %q", parsed.DefaultProfile)
	}
}

func TestAuthLogin_BlockedOwnerEmailFailsBeforeRequest(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)
	t.Setenv("MONSTERMAILBOX_API_KEY", "")

	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		t.Errorf("auth login should reject blocked owner email before HTTP request; got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	t.Setenv("MONSTERMAILBOX_API_URL", server.URL)

	_, stderr, err := runMmbCmd(t, []string{
		"auth", "login",
		"--address", "troy",
		"--email", "troy@example.com",
	}, nil)
	if err == nil {
		t.Fatalf("auth login with placeholder owner email should fail")
	}
	if hits != 0 {
		t.Fatalf("blocked owner email should make no HTTP requests; got %d", hits)
	}
	if !strings.Contains(err.Error(), "real human owner email") || !strings.Contains(err.Error(), "actual human owner's email") {
		t.Fatalf("error should explain the owner email rule; err=%v stderr=%q", err, stderr)
	}
}

// `mmb auth list` is JSON-by-default (principle 2 — agent-first).
// The legacy table layout is still available behind --human.
func TestAuthList_JSONByDefault(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)

	f := &config.File{}
	f.Put("alpha", config.Profile{
		APIURL:       "https://alpha.example.com",
		APIKey:       "PROFILE_ALPHA_KEY",
		AgentAddress: "alpha@example.com",
	})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runMmbCmd(t, []string{"auth", "list"}, nil)
	if err != nil {
		t.Fatalf("auth list: %v", err)
	}
	// stdout MUST be valid JSON, not a human table.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("auth list MUST emit JSON by default; parse err=%v stdout=%q", err, stdout)
	}
	if parsed["default_profile"] != "alpha" {
		t.Errorf("default_profile in JSON = %v; want alpha", parsed["default_profile"])
	}
	profiles, ok := parsed["profiles"].([]any)
	if !ok || len(profiles) != 1 {
		t.Fatalf("profiles must be an array of length 1; got %v", parsed["profiles"])
	}
	first, _ := profiles[0].(map[string]any)
	if first["name"] != "alpha" {
		t.Errorf("first profile name = %v; want alpha", first["name"])
	}
	// JSON output MUST mask the api_key, never expose it raw.
	if got, _ := first["api_key"].(string); got == "PROFILE_ALPHA_KEY" {
		t.Errorf("auth list MUST mask api_key in JSON output; got raw key")
	}
}

// `mmb auth list --human` retains the legacy table.
func TestAuthList_HumanFlagRendersTable(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)

	f := &config.File{}
	f.Put("alpha", config.Profile{APIURL: "https://alpha.example.com", APIKey: "k"})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runMmbCmd(t, []string{"auth", "list", "--human"}, nil)
	if err != nil {
		t.Fatalf("auth list --human: %v", err)
	}
	if !strings.Contains(stdout, "PROFILE") || !strings.Contains(stdout, "AGENT") {
		t.Errorf("--human MUST render table headers; got: %s", stdout)
	}
	// Table output is NOT valid JSON.
	if json.Unmarshal([]byte(stdout), new(any)) == nil {
		t.Errorf("--human output must NOT be JSON-parseable; got: %s", stdout)
	}
}

// `mmb auth logout` removes the profile and promotes a new default.
func TestAuthLogout_RemovesAndPromotesNextDefault(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)

	f := &config.File{}
	f.Put("aa", config.Profile{APIKey: "k1"})
	f.Put("bb", config.Profile{APIKey: "k2"})
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	// aa is the default. Logging out aa should promote bb.
	stdout, _, err := runMmbCmd(t, []string{"auth", "logout"}, nil)
	if err != nil {
		t.Fatalf("auth logout: %v", err)
	}
	if !strings.Contains(stdout, `removed profile "aa"`) {
		t.Errorf("missing logout confirmation: %s", stdout)
	}
	if !strings.Contains(stdout, `default profile is now "bb"`) {
		t.Errorf("default should promote to bb: %s", stdout)
	}
}
