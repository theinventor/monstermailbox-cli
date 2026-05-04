package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/config"
	"github.com/spf13/cobra"
)

// `mmb auth …` — multi-profile credential management. Stores keys in
// $XDG_CONFIG_HOME/mmb/config.json (mode 0600). The shape:
//
//	{
//	  "default_profile": "claude-troy-mbp",
//	  "profiles": {
//	    "claude-troy-mbp": { "api_url": "...", "api_key": "...",
//	                        "agent_address": "...", "owner_email": "...",
//	                        "created_at": "2026-..." }
//	  }
//	}
//
// At command time, `client.NewWithProfile()` resolves: --profile flag
// → MONSTERMAILBOX_API_KEY env → config's default_profile.
func newAuthCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Manage saved credentials (login, status, list, use, logout)",
		Long: `Multi-profile credential management for the mmb CLI.

Resolution order at command time:
  1. --profile <name>          (explicit override)
  2. $MONSTERMAILBOX_API_KEY    (env, beats the config file)
  3. config's default_profile  (~/.config/mmb/config.json)
  4. nothing (only public endpoints work)

Config file is mode 0600 and contains your API keys in plaintext.
On macOS you may want --keychain (TODO) for OS-keyring storage.`,
	}
	c.AddCommand(newAuthLoginCmd())
	c.AddCommand(newAuthSaveCmd())
	c.AddCommand(newAuthStatusCmd())
	c.AddCommand(newAuthListCmd())
	c.AddCommand(newAuthUseCmd())
	c.AddCommand(newAuthLogoutCmd())
	return c
}

// `mmb auth login --address X --email Y` — registers a new agent AND
// persists the resulting key as a profile. The most common entry point.
func newAuthLoginCmd() *cobra.Command {
	var address, email, profileName string
	c := &cobra.Command{
		Use:   "login",
		Short: "Register a new agent and save the API key as a profile",
		Long: `Calls /agents/register to mint a new agent + API key, then writes the
key to your config file as a named profile. The first profile saved
becomes the default.

By default the profile is named after the agent's local-part (e.g.
'claude-troy-mbp'); override with --profile.

If you ALREADY have a key (issued from the dashboard, copied from
a teammate, etc.), use 'mmb auth save' instead.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if address == "" || email == "" {
				return fmt.Errorf("--address and --email are both required")
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

			f, err := config.Load()
			if err != nil {
				return err
			}
			f.Put(name, config.Profile{
				APIURL:       cli.BaseURL,
				APIKey:       reg.APIKey,
				AgentAddress: reg.Address,
				OwnerEmail:   email,
			})
			if err := f.Save(); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ registered %s\n", reg.Address)
			fmt.Fprintf(out, "  saved as profile %q in %s\n", name, config.Path())
			if f.DefaultProfile == name {
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
	return c
}

// `mmb auth save --profile X --api-key Y --api-url Z` — persist a key
// you already have (e.g. issued from the dashboard).
func newAuthSaveCmd() *cobra.Command {
	var profileName, apiKey, apiURL, agentAddress, ownerEmail string
	c := &cobra.Command{
		Use:   "save",
		Short: "Save an existing API key as a profile",
		Long: `Persists a key you already have to the config file. Use this when
the key came from somewhere other than 'mmb auth login' (the dashboard,
a teammate, an environment variable you want to make permanent).`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if profileName == "" || apiKey == "" {
				return fmt.Errorf("--profile and --api-key are both required")
			}
			if apiURL == "" {
				apiURL = client.DefaultAPIURL
			}

			f, err := config.Load()
			if err != nil {
				return err
			}
			f.Put(profileName, config.Profile{
				APIURL:       apiURL,
				APIKey:       apiKey,
				AgentAddress: agentAddress,
				OwnerEmail:   ownerEmail,
			})
			if err := f.Save(); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "✓ saved profile %q to %s\n", profileName, config.Path())
			if f.DefaultProfile == profileName {
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
	return c
}

// `mmb auth status` — show what creds the CLI will use right now.
// Honors --profile passed at the auth subcommand level.
func newAuthStatusCmd() *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:   "status",
		Short: "Show the credentials the CLI will use",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli := client.NewWithProfile(profile)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "api_url:    %s\n", cli.BaseURL)
			fmt.Fprintf(out, "api_key:    %s\n", cli.MaskedAPIKey())
			source := cli.Source
			if source == "" {
				source = "(none — auth required for most commands)"
			}
			fmt.Fprintf(out, "source:     %s\n", source)

			// If a profile is being used, also surface its metadata.
			if strings.HasPrefix(cli.Source, "profile:") {
				name := strings.TrimPrefix(cli.Source, "profile:")
				if f, err := config.Load(); err == nil {
					if p, ok := f.Get(name); ok {
						if p.AgentAddress != "" {
							fmt.Fprintf(out, "agent:      %s\n", p.AgentAddress)
						}
						if p.OwnerEmail != "" {
							fmt.Fprintf(out, "owner:      %s\n", p.OwnerEmail)
						}
						if p.CreatedAt != "" {
							fmt.Fprintf(out, "saved_at:   %s\n", p.CreatedAt)
						}
					}
				}
			}

			// Verify connectivity by hitting /version (no auth needed).
			resp, err := cli.Do(http.MethodGet, "/version", nil, nil)
			if err != nil {
				fmt.Fprintf(out, "reachable:  no (%v)\n", err)
				return nil
			}
			defer resp.Body.Close()
			fmt.Fprintf(out, "reachable:  yes (HTTP %d)\n", resp.StatusCode)
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", "", "show status for a specific profile (default: active resolution)")
	return c
}

// `mmb auth list` — enumerate saved profiles.
func newAuthListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved profiles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
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

// `mmb auth logout [--profile X]` — remove a profile.
func newAuthLogoutCmd() *cobra.Command {
	var profile string
	c := &cobra.Command{
		Use:   "logout",
		Short: "Remove a saved profile",
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			name := profile
			if name == "" {
				name = f.DefaultProfile
			}
			if name == "" {
				return fmt.Errorf("no profile to remove (config is empty)")
			}
			if !f.Delete(name) {
				return fmt.Errorf("no profile named %q", name)
			}
			if err := f.Save(); err != nil {
				return err
			}
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
