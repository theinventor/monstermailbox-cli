// Package policy keeps an in-memory snapshot of the agent's
// dashboard-synced whitelist, refreshed every ~30s from
// GET /bridge/policy. The matcher (internal/matcher) reads the
// snapshot via Snapshot() — no shared mutex outside this package.
//
// In `--local-only` mode the daemon initializes a Store from
// `~/.mmb-bridge/whitelist.json` and never starts a sync goroutine.
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/config"
)

const SyncInterval = 30 * time.Second

// Store is the daemon-wide policy snapshot. Safe for concurrent reads.
type Store struct {
	mu       sync.RWMutex
	policy   api.PolicyResponse
	lastSync time.Time
	syncErr  error
}

// NewStore returns an empty store. Call Sync once before serving
// traffic — an unsynced store matches no senders (closed-by-default).
func NewStore() *Store { return &Store{} }

// Snapshot returns a defensive copy of the current policy + sync
// metadata so callers don't race with a concurrent Sync.
func (s *Store) Snapshot() (api.PolicyResponse, time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.policy
	out.Whitelist = append([]api.WhitelistEntry(nil), s.policy.Whitelist...)
	return out, s.lastSync, s.syncErr
}

// Apply replaces the in-memory policy. Used by Sync and (for tests
// + local-only mode) by external callers.
func (s *Store) Apply(p api.PolicyResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = p
	s.lastSync = time.Now()
	s.syncErr = nil
}

// recordError stamps a sync failure without dropping the previous
// good policy — a transient network blip should NOT widen the
// allowlist nor empty it.
func (s *Store) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncErr = err
	s.lastSync = time.Now()
}

// Sync fetches /bridge/policy once + applies if version is newer.
func (s *Store) Sync(ctx context.Context, c *api.Client) error {
	resp, err := c.GetPolicy(ctx)
	if err != nil {
		s.recordError(err)
		return err
	}
	cur, _, _ := s.Snapshot()
	if resp.Version < cur.Version {
		// Server reports an OLDER version than we have. Treat as
		// success but don't downgrade — protects against an aliased
		// load balancer hitting a stale replica during rolling deploy.
		s.mu.Lock()
		s.lastSync = time.Now()
		s.syncErr = nil
		s.mu.Unlock()
		return nil
	}
	s.Apply(*resp)
	return nil
}

// Run launches the background sync goroutine. Returns a stop func
// the caller invokes on shutdown. The first sync runs synchronously
// so callers can fail-fast on a misconfigured key.
func (s *Store) Run(ctx context.Context, c *api.Client) (stop func(), firstErr error) {
	firstErr = s.Sync(ctx, c)

	loopCtx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(SyncInterval)
		defer t.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-t.C:
				_ = s.Sync(loopCtx, c)
			}
		}
	}()
	return cancel, firstErr
}

// LoadLocal reads ~/.mmb-bridge/whitelist.json into a Store for
// `--local-only` mode (no server sync). The file format mirrors the
// server's PolicyResponse minus the version metadata.
func LoadLocal() (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "whitelist.json")
	bytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewStore(), nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var entries []api.WhitelistEntry
	if err := json.Unmarshal(bytes, &entries); err != nil {
		// Allow an object form too: {"whitelist": [...]}
		var obj struct {
			Whitelist []api.WhitelistEntry `json:"whitelist"`
		}
		if err2 := json.Unmarshal(bytes, &obj); err2 != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		entries = obj.Whitelist
	}
	s := NewStore()
	s.Apply(api.PolicyResponse{Whitelist: entries})
	return s, nil
}

// SaveLocal persists a local-only whitelist back to disk.
func SaveLocal(entries []api.WhitelistEntry) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "whitelist.json")
	bytes, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bytes, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
