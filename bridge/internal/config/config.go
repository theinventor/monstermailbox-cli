// Package config owns ~/.mmb-bridge/config.json — the bridge daemon's
// per-machine state minted at `mmb-bridge init` and read by every
// other subcommand.
//
// File mode is 0600 because it carries the bridge-scoped API key.
// Loss of this file just means re-running `mmb-bridge init` (the
// enrollment token is single-use, but the user mints a fresh one in
// the dashboard); it is NOT worth backing up to a sync service.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the on-disk shape of ~/.mmb-bridge/config.json.
//
// Field names are JSON-tagged in snake_case for forward-compat with
// any future humans hand-editing the file (mmb-bridge itself only
// reads what it writes).
type Config struct {
	APIBaseURL     string `json:"api_base_url"`     // e.g. https://app.monstermailbox.com
	APIKey         string `json:"api_key"`          // bridge-scoped mmb_<...> token
	AgentEmail     string `json:"agent_email"`      // address mail forwards INTO
	GoogleAccount  string `json:"google_account"`   // gmail account gog watches (e.g. you@gmail.com)
	GCPProject     string `json:"gcp_project"`      // GCP project hosting the Pub/Sub topic+sub
	PubSubTopic    string `json:"pubsub_topic"`     // short topic name, e.g. gmail-events
	PubSubSub      string `json:"pubsub_sub"`       // short subscription name, e.g. mmb-bridge-pull
	HookBindAddr   string `json:"hook_bind_addr"`   // unused in pull-mode; reserved for v1.x push-mode
	LocalOnly      bool   `json:"local_only"`       // true = ignore /bridge/policy, use local whitelist.json
	LogLevel       string `json:"log_level"`        // info|debug
}

// Path returns the absolute config-file path, honoring the
// `MMB_BRIDGE_DIR` env var so tests + alternative homes work.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Dir returns the bridge's per-user directory (creates it on first
// access at mode 0700). Honors MMB_BRIDGE_DIR override.
func Dir() (string, error) {
	if override := os.Getenv("MMB_BRIDGE_DIR"); override != "" {
		if err := os.MkdirAll(override, 0o700); err != nil {
			return "", fmt.Errorf("create %s: %w", override, err)
		}
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".mmb-bridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return dir, nil
}

// Load reads + JSON-decodes the config file. Returns ErrNotInitialized
// (sentinel, errors.Is-compatible) when the file doesn't exist so
// callers can route to "run mmb-bridge init first" messages.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := &Config{}
	if err := json.Unmarshal(bytes, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the config atomically (tmpfile + rename) at mode 0600.
func Save(cfg *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	bytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bytes, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// ErrNotInitialized is returned by Load when ~/.mmb-bridge/config.json
// is missing — the user hasn't run `mmb-bridge init` yet.
var ErrNotInitialized = errors.New("mmb-bridge is not initialized; run `mmb-bridge init`")
