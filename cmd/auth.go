package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/config"
	"github.com/theinventor/monstermailbox-cli/internal/credstore"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// `mmb auth …` — multi-profile credential management.
//
// Each profile records non-secret metadata (api_url, agent address,
// owner email, created_at) in $XDG_CONFIG_HOME/mmb/config.json
// (mode 0600). The actual API key lives in one of two backends:
//
//	keychain — OS keyring (macOS Keychain / Windows Credential
//	           Manager / Linux libsecret). Default for new profiles
//	           when available. Secret never lands on disk.
//	file     — config.json's api_key field, mode 0600. Fallback for
//	           environments without a working keyring (headless
//	           Linux, containers, CI) and the legacy storage for
//	           profiles created before v0.3.0.
//
// At command time, `client.NewWithProfile()` resolves credentials:
//
//  1. --profile <name>          (explicit override)
//  2. $MONSTERMAILBOX_API_KEY    (env, beats the config file)
//  3. config's default_profile  (with whatever backend it declared)
//  4. nothing (only public endpoints work)
//
// Use `mmb auth migrate` to move existing file-backed profiles into
// the keychain. Run `mmb auth status` to see which backend a profile
// is using right now.
func newAuthCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Manage saved credentials (login, status, list, use, logout, migrate)",
		Long: `Multi-profile credential management for the mmb CLI.

Resolution order at command time:
  1. --profile <name>          (explicit override)
  2. $MONSTERMAILBOX_API_KEY    (env, beats the config file)
  3. config's default_profile  (~/.config/mmb/config.json)
  4. nothing (only public endpoints work)

Default storage for new profiles is the OS keychain (macOS Keychain,
Windows Credential Manager, Linux libsecret). Pass --storage=file to
keep the secret in config.json (mode 0600) instead, e.g. for headless
servers without a keyring agent. Pass --storage=keychain to require
the keychain (errors out if unavailable).

Already have file-backed profiles? Run 'mmb auth migrate' to move
them into the keychain without re-registering.`,
	}
	c.AddCommand(newAuthLoginCmd())
	c.AddCommand(newAuthSaveCmd())
	c.AddCommand(newAuthStatusCmd())
	c.AddCommand(newAuthListCmd())
	c.AddCommand(newAuthUseCmd())
	c.AddCommand(newAuthLogoutCmd())
	c.AddCommand(newAuthMigrateCmd())
	return c
}

// storageFlagDescription is reused on every command that takes --storage
// so the help text stays consistent (principle 3 — errors that teach,
// applied to flag docs).
const storageFlagDescription = "where to persist the API key: auto (default; keychain if available, else file), keychain, or file"

// resolveStorage centralizes the --storage flag → concrete backend
// translation. Errors are wrapped with exitcode.Usage so failures get
// the right exit code (principle 2).
func resolveStorage(storage string) (string, error) {
	backend, err := credstore.ResolveBackend(storage)
	if err != nil {
		return "", exitcode.Wrap(exitcode.Usage, err)
	}
	return backend, nil
}

// persistProfile is the single write path used by login/save/migrate.
// It puts the secret in the chosen backend, clears the file-side
// APIKey when the backend is keychain (so the secret never touches
// disk), and saves the config file. Returns the canonical backend
// for the caller to surface in confirmation output.
func persistProfile(name string, p config.Profile, secret, backend string) (string, error) {
	canon, err := credstore.Put(name, backend, secret)
	if err != nil {
		return "", err
	}
	switch canon {
	case credstore.BackendKeychain:
		p.APIKey = ""
	case credstore.BackendFile:
		p.APIKey = secret
	}
	p.Backend = canon

	f, err := config.Load()
	if err != nil {
		return "", err
	}
	f.Put(name, p)
	if err := f.Save(); err != nil {
		return "", err
	}
	return canon, nil
}

// `mmb auth login --address X --email Y` — registers a new agent AND
// persists the resulting key as a profile. The most common entry point.
func newAuthLoginCmd() *cobra.Command {
	var address, email, profileName, storage string
	c := &cobra.Command{
		Use:   "login",
		Short: "Register a new agent and save the API key as a profile",
		Long: `Calls /agents/register to mint a new agent + API key, then persists the
key as a named profile. The first profile saved becomes the default.

The API key is stored in the OS keychain by default (macOS Keychain,
Windows Credential Manager, Linux libsecret). Pass --storage=file to
keep it in the mode-0600 config file instead.

By default the profile is named after the agent's local-part (e.g.
'claude-troy-mbp'); override with --profile.

If you ALREADY have a key (issued from the dashboard, copied from
a teammate, etc.), use 'mmb auth save' instead.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if address == "" || email == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("--address and --email are both required"))
			}

			backend, err := resolveStorage(storage)
			if err != nil {
				return err
			}

			// Use a fresh, unauthenticated client — register is public.
			cli := client.New()
			cli.APIKey = ""
			resp, err := cli.Do(http.MethodPost, "/agents/register", map[string]any{
				"desired_address": address,
				"owner_email":     email,
			}, nil)
			if err != nil {
				return fmt.Errorf("POST /agents/register: %w", err)
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusCreated {
				// Surface the server error verbatim.
				return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			var reg struct {
				AgentID          any    `json:"agent_id"`
				Address          string `json:"address"`
				APIKey           string `json:"api_key"`
				HumanOwnerStatus string `json:"human_owner_status"`
			}
			if err := json.Unmarshal(body, &reg); err != nil {
				return fmt.Errorf("parse register response: %w", err)
			}

			name := profileName
			if name == "" {
				// agent's local-part is a sensible default profile name.
				if i := strings.IndexByte(reg.Address, '@'); i > 0 {
					name = reg.Address[:i]
				} else {
					name = address
				}
			}

			canon, err := persistProfile(name, config.Profile{
				APIURL:       cli.BaseURL,
				AgentAddress: reg.Address,
				OwnerEmail:   email,
			}, reg.APIKey, backend)
			if err != nil {
				return err
			}

			f, _ := config.Load() // already saved; reload for default check
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ registered %s\n", reg.Address)
			fmt.Fprintf(out, "  saved as profile %q (storage: %s)\n", name, credstore.Describe(canon))
			fmt.Fprintf(out, "  config: %s\n", config.Path())
			if f != nil && f.DefaultProfile == name {
				fmt.Fprintf(out, "  set as default profile\n")
			}
			fmt.Fprintf(out, "  human_owner_status: %s\n", reg.HumanOwnerStatus)
			fmt.Fprintf(out, "  api_key fingerprint: %s\n", maskKey(reg.APIKey))
			fmt.Fprintf(out, "\nYou can now run `mmb whoami`, `mmb inbox list`, etc. — no env vars needed.\n")
			return nil
		},
	}
	c.Flags().StringVar(&address, "address", "", "desired local-part (required)")
	c.Flags().StringVar(&email, "email", "", "owner email — gets the claim invite (required)")
	c.Flags().StringVar(&profileName, "profile", "", "profile name to save under (default: agent local-part)")
	c.Flags().StringVar(&storage, "storage", "", storageFlagDescription)
	return c
}

// `mmb auth save --profile X --api-key Y --api-url Z` — persist a key
// you already have (e.g. issued from the dashboard).
func newAuthSaveCmd() *cobra.Command {
	var profileName, apiKey, apiURL, agentAddress, ownerEmail, storage string
	c := &cobra.Command{
		Use:   "save",
		Short: "Save an existing API key as a profile",
		Long: `Persists a key you already have. Use this when the key came from
somewhere other than 'mmb auth login' (the dashboard, a teammate, an
environment variable you want to make permanent).

By default the secret is stored in the OS keychain. Pass --storage=file
to keep it in the mode-0600 config file instead.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if profileName == "" || apiKey == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("--profile and --api-key are both required"))
			}
			if apiURL == "" {
				apiURL = client.DefaultAPIURL
			}

			backend, err := resolveStorage(storage)
			if err != nil {
				return err
			}

			canon, err := persistProfile(profileName, config.Profile{
				APIURL:       apiURL,
				AgentAddress: agentAddress,
				OwnerEmail:   ownerEmail,
			}, apiKey, backend)
			if err != nil {
				return err
			}

			f, _ := config.Load()
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ saved profile %q (storage: %s)\n", profileName, credstore.Describe(canon))
			fmt.Fprintf(out, "  config: %s\n", config.Path())
			if f != nil && f.DefaultProfile == profileName {
				fmt.Fprintf(out, "  set as default profile\n")
			}
			fmt.Fprintf(out, "  api_url: %s\n", apiURL)
			fmt.Fprintf(out, "  api_key fingerprint: %s\n", maskKey(apiKey))
			return nil
		},
	}
	c.Flags().StringVar(&profileName, "profile", "", "profile name (required)")
	c.Flags().StringVar(&apiKey, "api-key", "", "API key (required)")
	c.Flags().StringVar(&apiURL, "api-url", "", "API URL (default: production)")
	c.Flags().StringVar(&agentAddress, "agent-address", "", "agent's full address (optional, for status display)")
	c.Flags().StringVar(&ownerEmail, "owner-email", "", "human owner email (optional, for status display)")
	c.Flags().StringVar(&storage, "storage", "", storageFlagDescription)
	return c
}

// `mmb auth status` — show what creds the CLI will use right now.
//
// Output is JSON by default (principle 2 — agent is the primary
// audience); pass `--human` for the table layout humans prefer at
// a terminal. Honors --profile passed at the auth subcommand level.
func newAuthStatusCmd() *cobra.Command {
	var profile string
	var human bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Show the credentials the CLI will use (JSON by default)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			activeProfile := profile
			if activeProfile == "" {
				activeProfile = rootProfile
			}
			cli := client.NewWithProfile(activeProfile)
			out := cmd.OutOrStdout()

			// Always compute the same record; rendering choice differs.
			rec := map[string]any{
				"api_url":     cli.BaseURL,
				"api_key":     cli.MaskedAPIKey(),
				"source":      cli.Source,
				"backend":     credstore.Describe(cli.Backend),
				"cli_version": Version,
			}
			if rec["source"] == "" {
				rec["source"] = nil
			}
			if strings.HasPrefix(cli.Source, "profile:") {
				name := strings.TrimPrefix(cli.Source, "profile:")
				if f, err := config.Load(); err == nil {
					if p, ok := f.Get(name); ok {
						rec["profile"] = name
						if p.AgentAddress != "" {
							rec["agent_address"] = p.AgentAddress
						}
						if p.OwnerEmail != "" {
							rec["owner_email"] = p.OwnerEmail
						}
						if p.CreatedAt != "" {
							rec["saved_at"] = p.CreatedAt
						}
					}
				}
			}

			// Reachability probe — never fails the command, just records.
			resp, err := cli.Do(http.MethodGet, "/version", nil, nil)
			if err != nil {
				rec["reachable"] = false
				rec["reachable_error"] = err.Error()
			} else {
				defer resp.Body.Close()
				rec["reachable"] = resp.StatusCode < 400
				rec["server_status"] = resp.StatusCode
			}

			if !human {
				return printJSON(out, rec)
			}

			fmt.Fprintf(out, "api_url:    %s\n", cli.BaseURL)
			fmt.Fprintf(out, "api_key:    %s\n", cli.MaskedAPIKey())
			source := cli.Source
			if source == "" {
				source = "(none — auth required for most commands)"
			}
			fmt.Fprintf(out, "source:     %s\n", source)
			if cli.Backend != "" {
				fmt.Fprintf(out, "backend:    %s\n", credstore.Describe(cli.Backend))
			}
			if addr, ok := rec["agent_address"].(string); ok {
				fmt.Fprintf(out, "agent:      %s\n", addr)
			}
			if owner, ok := rec["owner_email"].(string); ok {
				fmt.Fprintf(out, "owner:      %s\n", owner)
			}
			if saved, ok := rec["saved_at"].(string); ok {
				fmt.Fprintf(out, "saved_at:   %s\n", saved)
			}
			if reachable, _ := rec["reachable"].(bool); reachable {
				fmt.Fprintf(out, "reachable:  yes (HTTP %d)\n", rec["server_status"])
			} else if msg, ok := rec["reachable_error"].(string); ok {
				fmt.Fprintf(out, "reachable:  no (%s)\n", msg)
			} else {
				fmt.Fprintf(out, "reachable:  no\n")
			}
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", "", "show status for a specific profile (default: active resolution)")
	c.Flags().BoolVar(&human, "human", false, "render as a human-friendly table instead of JSON")
	return c
}

// `mmb auth list` — enumerate saved profiles.
//
// JSON by default; `--human` renders the legacy table layout.
func newAuthListCmd() *cobra.Command {
	var human bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List saved profiles (JSON by default)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			if !human {
				profiles := make([]map[string]any, 0, len(f.Profiles))
				for _, name := range f.Names() {
					p := f.Profiles[name]
					entry := map[string]any{
						"name":          name,
						"api_url":       p.APIURL,
						"api_key":       maskKey(p.APIKey),
						"is_default":    name == f.DefaultProfile,
						"agent_address": p.AgentAddress,
						"owner_email":   p.OwnerEmail,
						"created_at":    p.CreatedAt,
					}
					profiles = append(profiles, entry)
				}
				return printJSON(out, map[string]any{
					"config_path":     config.Path(),
					"default_profile": f.DefaultProfile,
					"profiles":        profiles,
				})
			}

			if len(f.Profiles) == 0 {
				fmt.Fprintln(out, "(no profiles — try `mmb auth login`)")
				return nil
			}
			fmt.Fprintf(out, "config: %s\n\n", config.Path())
			fmt.Fprintf(out, "%-30s %-30s %s\n", "PROFILE", "AGENT", "API_URL")
			for _, name := range f.Names() {
				p := f.Profiles[name]
				marker := "  "
				if name == f.DefaultProfile {
					marker = "* "
				}
				addr := p.AgentAddress
				if addr == "" {
					addr = "(not recorded)"
				}
				fmt.Fprintf(out, "%s%-28s %-30s %s\n", marker, name, addr, p.APIURL)
			}
			fmt.Fprintln(out, "\n* = default profile (used when no --profile flag and no env var)")
			return nil
		},
	}
	c.Flags().BoolVar(&human, "human", false, "render as a human-friendly table instead of JSON")
	return c
}

// `mmb auth use <profile>` — change the default profile.
func newAuthUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <profile>",
		Short: "Set the default profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			if err := f.SetDefault(args[0]); err != nil {
				return err
			}
			if err := f.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ default profile is now %q\n", args[0])
			return nil
		},
	}
}

// `mmb auth logout [--profile X] [--force]` — remove a saved profile.
//
// --force is currently a no-op alignment flag (logout never prompts
// today). It is accepted because principle 6 standardizes on --force
// for destructive bypass; once we add an "are you sure?" prompt for
// terminals, --force will be required to skip it. Including the flag
// now means agents written today won't break when the prompt lands.
func newAuthLogoutCmd() *cobra.Command {
	var profile string
	var force bool
	c := &cobra.Command{
		Use:   "logout",
		Short: "Remove a saved profile",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = force // see comment above; reserved for future interactive guard

			f, err := config.Load()
			if err != nil {
				return err
			}
			name := profile
			if name == "" {
				name = f.DefaultProfile
			}
			if name == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("no profile to remove (config is empty)"))
			}
			if !f.Delete(name) {
				return exitcode.Wrap(exitcode.NotFound,
					fmt.Errorf("no profile named %q", name))
			}
			if err := f.Save(); err != nil {
				return err
			}
			// Best-effort: also clear any keychain entry under this
			// profile name. Errors are silently ignored — the file
			// profile is already gone, which is the user-visible state.
			_ = credstore.Delete(name)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ removed profile %q\n", name)
			if f.DefaultProfile != "" {
				fmt.Fprintf(out, "  default profile is now %q\n", f.DefaultProfile)
			} else {
				fmt.Fprintf(out, "  no profiles remain\n")
			}
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", "", "profile to remove (default: current default)")
	c.Flags().BoolVar(&force, "force", false, "skip any future confirmation prompt (reserved for principle 6 alignment)")
	return c
}

// `mmb auth migrate [--profile X] [--all]` — move file-backed profile
// secrets into the OS keychain without re-registering. The most common
// upgrade path from pre-v0.3.0 (where every key sat plaintext in
// config.json).
//
// Migration is idempotent: profiles already on the keychain are skipped
// with a no-op message. The file's api_key is cleared on success — the
// secret no longer touches disk after this runs.
func newAuthMigrateCmd() *cobra.Command {
	var profileName string
	var all bool
	c := &cobra.Command{
		Use:   "migrate",
		Short: "Move file-backed profile secrets into the OS keychain",
		Long: `Moves the api_key for one profile (--profile) or every file-backed
profile (--all) from the config file into the OS keychain.

After this, the config file holds only non-secret metadata. Existing
authenticated calls keep working — credstore reads from the keychain
on each request.

Errors out if the OS keychain isn't available (set MMB_DISABLE_KEYCHAIN=1
to confirm, or pass nothing — there's no point migrating without a
working keychain).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if profileName == "" && !all {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("pass --profile <name> or --all"))
			}
			if !credstore.KeychainAvailable() {
				return exitcode.Wrap(exitcode.Generic,
					errors.New("OS keychain unavailable; cannot migrate (set MMB_DISABLE_KEYCHAIN=0 if you previously disabled it, or run on a host with a keyring agent)"))
			}

			f, err := config.Load()
			if err != nil {
				return err
			}

			targets := []string{}
			if all {
				targets = f.Names()
			} else {
				if _, ok := f.Get(profileName); !ok {
					return exitcode.Wrap(exitcode.NotFound,
						fmt.Errorf("no profile named %q", profileName))
				}
				targets = []string{profileName}
			}

			out := cmd.OutOrStdout()
			migrated, skipped := 0, 0
			for _, name := range targets {
				p, _ := f.Get(name)
				switch p.Backend {
				case credstore.BackendKeychain:
					fmt.Fprintf(out, "  %s: already on keychain — skipped\n", name)
					skipped++
					continue
				case credstore.BackendFile, "":
					if p.APIKey == "" {
						fmt.Fprintf(out, "  %s: no api_key in file — skipped\n", name)
						skipped++
						continue
					}
					if _, err := credstore.Put(name, credstore.BackendKeychain, p.APIKey); err != nil {
						return fmt.Errorf("migrate %s: %w", name, err)
					}
					p.APIKey = ""
					p.Backend = credstore.BackendKeychain
					f.Put(name, *p)
					migrated++
					fmt.Fprintf(out, "  %s: migrated → keychain\n", name)
				default:
					fmt.Fprintf(out, "  %s: unknown backend %q — skipped\n", name, p.Backend)
					skipped++
				}
			}
			if migrated > 0 {
				if err := f.Save(); err != nil {
					return err
				}
			}
			fmt.Fprintf(out, "\n✓ migrated %d, skipped %d\n", migrated, skipped)
			return nil
		},
	}
	c.Flags().StringVar(&profileName, "profile", "", "profile to migrate (mutually exclusive with --all)")
	c.Flags().BoolVar(&all, "all", false, "migrate every file-backed profile")
	return c
}

// maskKey returns a short fingerprint of an API key for display.
func maskKey(k string) string {
	if k == "" {
		return "(none)"
	}
	if len(k) < 12 {
		return "***"
	}
	return k[:8] + "…" + k[len(k)-4:]
}
