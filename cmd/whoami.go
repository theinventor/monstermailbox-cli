package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/config"
	"github.com/theinventor/monstermailbox-cli/internal/updater"
)

// whoami emits identity-relevant context: which API the CLI is
// targeting, the loaded API key fingerprint (NEVER the full key),
// and the server's /version response (proves the API is reachable
// + the CLI's API key was accepted-or-rejected sensibly).
func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Print the loaded API identity + API target + server version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := newAPIClient()

			resp, err := c.Do(http.MethodGet, "/version", nil, nil)
			if err != nil {
				return fmt.Errorf("GET /version: %w", err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("read /version body: %w", err)
			}

			var version map[string]any
			_ = json.Unmarshal(body, &version)

			out := map[string]any{
				"api_url":        c.BaseURL,
				"api_key":        c.MaskedAPIKey(),
				"source":         c.Source,
				"cli_version":    Version,
				"server_version": version,
				"server_status":  resp.StatusCode,
			}
			if c.Source == "" {
				out["source"] = nil
			}
			if profile, p, ok := resolvedProfile(c.Source); ok {
				out["profile"] = profile
				if p.AgentAddress != "" {
					out["agent_address"] = p.AgentAddress
				}
				if p.OwnerEmail != "" {
					out["owner_email"] = p.OwnerEmail
				}
			}

			// Surface update availability — agents read whoami output
			// to decide whether to call `mmb update`. Cached for 24h
			// so this doesn't add latency to every command. Never
			// fails the parent command on update-check error.
			info := updater.CheckForUpdate(Version)
			if info.Available {
				out["update_available"] = map[string]any{
					"current": info.Current,
					"latest":  info.Latest,
					"url":     info.URL,
					"hint":    "run `mmb update` to install",
				}
			}

			return printJSON(cmd.OutOrStdout(), out)
		},
	}
}

func resolvedProfile(source string) (string, *config.Profile, bool) {
	const prefix = "profile:"
	if !strings.HasPrefix(source, prefix) {
		return "", nil, false
	}
	name := strings.TrimPrefix(source, prefix)
	f, err := config.Load()
	if err != nil {
		return "", nil, false
	}
	p, ok := f.Get(name)
	return name, p, ok
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
