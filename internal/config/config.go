// Package config reads + writes the persistent CLI config.
//
// File layout (XDG-conventional):
//
//	$XDG_CONFIG_HOME/mmb/config.json   (preferred when XDG_CONFIG_HOME set)
//	~/.config/mmb/config.json          (default on macOS/Linux)
//
// File mode is 0600 because it holds API keys.
//
// Schema:
//
//	{
//	  "default_profile": "claude-troy-mbp",
//	  "profiles": {
//	    "claude-troy-mbp": {
//	      "api_url":       "https://api.monstermailbox.com",
//	      "api_key":       "mmb_…",
//	      "agent_address": "claude-troy-mbp@monstermailbox.com",
//	      "owner_email":   "user@example.com",
//	      "created_at":    "2026-05-03T22:00:00Z"
//	    }
//	  }
//	}
//
// Resolution order at the client layer:
//
//  1. Explicit profile name passed in (e.g. via --profile)
//  2. MONSTERMAILBOX_API_KEY env var (with MONSTERMAILBOX_API_URL)
//  3. config's default_profile
//  4. Empty Client (caller errors if it tries an authenticated call)
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Profile is one named (api_url, api_key) pair, plus convenience metadata
// the user can read via `mmb auth status`.
type Profile struct {
	APIURL       string `json:"api_url"`
	APIKey       string `json:"api_key"`
	AgentAddress string `json:"agent_address,omitempty"`
	OwnerEmail   string `json:"owner_email,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

// File is the persisted config shape.
type File struct {
	DefaultProfile string              `json:"default_profile,omitempty"`
	Profiles       map[string]Profile  `json:"profiles,omitempty"`
}

// Path returns the resolved config file path. Honors $MMB_CONFIG (test
// override), then $XDG_CONFIG_HOME, then $HOME/.config.
func Path() string {
	if explicit := os.Getenv("MMB_CONFIG"); explicit != "" {
		return explicit
	}
	root := os.Getenv("XDG_CONFIG_HOME")
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".config")
	}
	return filepath.Join(root, "mmb", "config.json")
}

// Load reads the config file. Missing file is NOT an error — returns
// an empty File so first-run callers can populate it.
func Load() (*File, error) {
	path := Path()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{Profiles: map[string]Profile{}}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]Profile{}
	}
	return &f, nil
}

// Save writes the config file with mode 0600. Creates parent dirs.
func (f *File) Save() error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// Atomic write: tmp + rename, with mode set on tmp before rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// Get returns a profile by name. The empty string means "use default".
// Returns (nil, false) if the requested profile (or the default) doesn't
// exist.
func (f *File) Get(name string) (*Profile, bool) {
	if name == "" {
		name = f.DefaultProfile
	}
	if name == "" {
		return nil, false
	}
	p, ok := f.Profiles[name]
	if !ok {
		return nil, false
	}
	return &p, true
}

// Put adds or replaces a profile. If this is the first profile being
// added, it also becomes the default.
func (f *File) Put(name string, p Profile) {
	if f.Profiles == nil {
		f.Profiles = map[string]Profile{}
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	f.Profiles[name] = p
	if f.DefaultProfile == "" {
		f.DefaultProfile = name
	}
}

// Delete removes a profile. If it was the default, the default becomes
// either some-other-profile (alphabetically first) or empty.
func (f *File) Delete(name string) bool {
	if _, ok := f.Profiles[name]; !ok {
		return false
	}
	delete(f.Profiles, name)
	if f.DefaultProfile == name {
		f.DefaultProfile = ""
		// Pick a deterministic new default if any profiles remain.
		if len(f.Profiles) > 0 {
			names := make([]string, 0, len(f.Profiles))
			for n := range f.Profiles {
				names = append(names, n)
			}
			sort.Strings(names)
			f.DefaultProfile = names[0]
		}
	}
	return true
}

// SetDefault sets the default profile. Errors if the named profile
// doesn't exist (refuse to point default at nothing).
func (f *File) SetDefault(name string) error {
	if _, ok := f.Profiles[name]; !ok {
		return fmt.Errorf("no profile named %q", name)
	}
	f.DefaultProfile = name
	return nil
}

// Names returns profile names in stable (sorted) order.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
