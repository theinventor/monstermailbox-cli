// Package state owns ~/.mmb-bridge/state.json — runtime state the
// daemon carries across restarts.
//
// Two responsibilities:
//
//  1. `LastHistoryID` — Gmail's monotonic history cursor. The push
//     payload carries the historyId at the time of the new message;
//     we feed `gog gmail history --since=<id>` to enumerate new
//     messages. Persisted so the daemon can resume after a crash
//     without re-processing or missing messages.
//
//  2. `RecentMessageIDs` — bounded LIFO ring of message IDs we've
//     already POSTed to /bridge/inbound. Pub/Sub at-least-once
//     delivery means the same notification CAN arrive twice; this
//     ring lets us skip a duplicate forward without hitting the API
//     for a full message-ID lookup. Bound is 1000 — Gmail's history
//     API rarely returns more than a few new IDs per push.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/config"
)

const ringCap = 1000

type State struct {
	LastHistoryID    string   `json:"last_history_id"`
	RecentMessageIDs []string `json:"recent_message_ids"`

	mu sync.Mutex
}

// Load reads the state file. A missing file returns a zero-value
// State (NOT an error) so the daemon can start fresh on first run.
func Load() (*State, error) {
	path, err := pathFor()
	if err != nil {
		return nil, err
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &State{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	s := &State{}
	if err := json.Unmarshal(bytes, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// Save writes the state atomically (tmpfile + rename) at mode 0600.
// Safe to call concurrently — internal mutex serializes file I/O.
func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := pathFor()
	if err != nil {
		return err
	}
	bytes, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
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

// SetHistoryID atomically advances the cursor. Caller is responsible
// for calling Save() to persist.
func (s *State) SetHistoryID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastHistoryID = id
}

// HistoryID returns the persisted cursor (empty string on first run).
func (s *State) HistoryID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastHistoryID
}

// SeenMessage reports whether `id` has already been POSTed to the
// server in a recent run. Always check + Mark before forwarding.
func (s *State) SeenMessage(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, seen := range s.RecentMessageIDs {
		if seen == id {
			return true
		}
	}
	return false
}

// MarkMessage records a message ID as forwarded. Trims the ring to
// `ringCap` keeping the most recent entries.
func (s *State) MarkMessage(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RecentMessageIDs = append(s.RecentMessageIDs, id)
	if over := len(s.RecentMessageIDs) - ringCap; over > 0 {
		s.RecentMessageIDs = s.RecentMessageIDs[over:]
	}
}

func pathFor() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}
