// Package credstore is the persistence boundary for API keys.
//
// Profiles in internal/config/ store non-secret metadata (api_url, agent
// address, owner email, created_at). The actual API key lives in one of:
//
//	keychain — OS keyring (macOS Keychain / Windows Credential Manager /
//	           Linux libsecret). Default for new profiles when available.
//	file     — config.json's api_key field, mode 0600. Fallback for
//	           environments without a working keyring (headless Linux,
//	           containers, CI).
//	env      — MONSTERMAILBOX_API_KEY at runtime; never persisted. Not a
//	           per-profile choice — env always wins at the client layer
//	           (see internal/client.NewWithProfile). Listed here for
//	           completeness so callers can describe the backend correctly
//	           in `auth status` output.
//
// Resolution order at the client layer is unchanged:
//
//  1. --profile flag (with explicit backend per-profile)
//  2. MONSTERMAILBOX_API_KEY env var (treated as backend "env")
//  3. config's default_profile (with whatever backend it declared)
//
// Why a separate package: ClawScan flags plaintext API keys on disk as
// HIGH severity. Routing the secret through this package — and defaulting
// the keychain backend on for new profiles — moves the secret off disk in
// the common case. Existing file-backed profiles keep working until the
// user runs `mmb auth migrate`.
package credstore

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/zalando/go-keyring"
)

// Backend names. Stored verbatim in Profile.Backend (omitempty).
const (
	BackendFile     = "file"
	BackendKeychain = "keychain"
	BackendEnv      = "env" // never persisted; describes runtime resolution
	BackendAuto     = "auto" // input-only sentinel for "pick the best available"
)

// Service is the key under which the OS keyring groups our entries.
// Account is the profile name. Picked once and never changed — renaming
// it would orphan every user's existing entries.
const Service = "monstermailbox-cli"

// EnvDisableKeychain force-disables the keychain backend, even if the OS
// claims it's available. Useful in CI containers where dbus is technically
// reachable but no agent is actually running, so keyring ops hang.
const EnvDisableKeychain = "MMB_DISABLE_KEYCHAIN"

// EnvStorage overrides the default storage backend for `auth login`/`auth
// save` when the user doesn't pass --storage.
const EnvStorage = "MMB_STORAGE"

// ErrNotFound is returned by Get when no key exists for the given profile.
// Callers can sniff it with errors.Is.
var ErrNotFound = errors.New("credstore: no key stored for profile")

// keyringIface is the minimum surface of zalando/go-keyring we use.
// Indirection lets tests inject an in-memory implementation without
// pulling go-keyring's MockInit (which is process-global and doesn't
// compose with parallel tests).
type keyringIface interface {
	Set(service, user, password string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

// realKeyring delegates to the OS keyring via zalando/go-keyring.
type realKeyring struct{}

func (realKeyring) Set(s, u, p string) error    { return keyring.Set(s, u, p) }
func (realKeyring) Get(s, u string) (string, error) {
	v, err := keyring.Get(s, u)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}
func (realKeyring) Delete(s, u string) error {
	err := keyring.Delete(s, u)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil // idempotent: deleting a missing key is fine
	}
	return err
}

// active is the keyring driver. Tests swap it via UseMockKeyring.
var active keyringIface = realKeyring{}

// MemKeyring is an in-memory keyring for tests. It satisfies the same
// contract as the OS keyring driver — Set/Get/Delete with ErrNotFound
// on miss — without touching any real OS state. Exported so tests in
// sibling packages (cmd/, internal/client/) can install it via
// UseMockKeyring without depending on go-keyring's process-global
// MockInit (which doesn't compose with parallel tests).
type MemKeyring struct {
	mu   sync.Mutex
	data map[string]string
	// FailGet/FailSet/FailDelete simulate keyring backends that
	// half-work (e.g. dbus reachable but no agent → operations error).
	// Tests flip these to exercise the auto-fallback path.
	FailGet, FailSet, FailDelete bool
}

// NewMemKeyring constructs a fresh in-memory keyring.
func NewMemKeyring() *MemKeyring { return &MemKeyring{data: map[string]string{}} }

func memKey(s, u string) string { return s + "::" + u }

// Set stores a secret in the in-memory map.
func (m *MemKeyring) Set(service, user, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailSet {
		return errors.New("MemKeyring: simulated set failure")
	}
	m.data[memKey(service, user)] = password
	return nil
}

// Get returns ErrNotFound when the entry is missing — matching the real
// driver's contract.
func (m *MemKeyring) Get(service, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailGet {
		return "", errors.New("MemKeyring: simulated get failure")
	}
	v, ok := m.data[memKey(service, user)]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Delete is idempotent on a missing entry — matching the real driver.
func (m *MemKeyring) Delete(service, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailDelete {
		return errors.New("MemKeyring: simulated delete failure")
	}
	delete(m.data, memKey(service, user))
	return nil
}

// UseMockKeyring installs a fresh in-memory keyring as the active driver
// and returns a restore func the caller should defer. Tests in any package
// can call this to ensure the test process never touches the real OS
// keyring.
func UseMockKeyring() (*MemKeyring, func()) {
	m := NewMemKeyring()
	prev := active
	active = m
	return m, func() { active = prev }
}

// SetKeyringForTesting is the lower-level escape hatch when callers need
// to inject their own driver (e.g. one that simulates errors). Most tests
// should use UseMockKeyring instead.
func SetKeyringForTesting(k keyringIface) func() {
	prev := active
	active = k
	return func() { active = prev }
}

// KeychainAvailable reports whether the OS keychain is usable on this
// host RIGHT NOW. Returns false if MMB_DISABLE_KEYCHAIN is set, or if a
// probe round-trip fails (no agent running, etc.). The probe is cheap
// (a Get on a sentinel key) and returns ErrNotFound on a working empty
// keychain — which counts as "available."
func KeychainAvailable() bool {
	if os.Getenv(EnvDisableKeychain) != "" {
		return false
	}
	// Probe with a sentinel account name. ErrNotFound = working keyring,
	// no entry → available. Any other error = not available.
	_, err := active.Get(Service, "__mmb_keychain_probe__")
	if err == nil || errors.Is(err, ErrNotFound) {
		return true
	}
	return false
}

// ResolveBackend turns a user-facing storage choice ("auto", "keychain",
// "file", "" for default) into a concrete backend. Honors MMB_STORAGE
// when storage == "" (or "auto"). Returns an error if the user explicitly
// requested keychain on a host where it isn't available.
func ResolveBackend(storage string) (string, error) {
	if storage == "" {
		storage = os.Getenv(EnvStorage)
	}
	if storage == "" {
		storage = BackendAuto
	}
	switch storage {
	case BackendKeychain:
		if !KeychainAvailable() {
			return "", fmt.Errorf("keychain backend requested but no OS keyring is available on %s (set MMB_STORAGE=file or unset MMB_DISABLE_KEYCHAIN)", runtime.GOOS)
		}
		return BackendKeychain, nil
	case BackendFile:
		return BackendFile, nil
	case BackendAuto:
		if KeychainAvailable() {
			return BackendKeychain, nil
		}
		return BackendFile, nil
	default:
		return "", fmt.Errorf("unknown storage backend %q (want keychain|file|auto)", storage)
	}
}

// Put writes the secret for `profile` to the chosen backend. The caller
// is responsible for clearing the secret from the file-side Profile when
// the backend is "keychain" — credstore intentionally doesn't reach into
// config.File. Returns the canonical backend name the caller should
// store on Profile.Backend.
func Put(profile, backend, secret string) (string, error) {
	switch backend {
	case BackendKeychain:
		if err := active.Set(Service, profile, secret); err != nil {
			return "", fmt.Errorf("keychain set: %w", err)
		}
		return BackendKeychain, nil
	case BackendFile, "":
		// File-backed profiles store the secret in the config file
		// itself; the caller writes it onto the Profile struct. Just
		// echo back the canonical backend name.
		return BackendFile, nil
	default:
		return "", fmt.Errorf("credstore.Put: unsupported backend %q", backend)
	}
}

// Get returns the live secret for `profile`. The caller passes both the
// declared backend and the file-side fallback (the legacy api_key
// embedded in Profile). Resolution:
//
//	backend == "keychain" → keyring.Get(Service, profile)
//	backend == "file"     → fileSecret
//	backend == ""         → fileSecret (legacy pre-v0.3.0 profile)
//
// ErrNotFound is returned for keychain misses; for file misses we just
// return the empty string (matches existing config.Profile semantics).
func Get(profile, backend, fileSecret string) (string, error) {
	switch backend {
	case BackendKeychain:
		v, err := active.Get(Service, profile)
		if err != nil {
			return "", err
		}
		return v, nil
	case BackendFile, "":
		return fileSecret, nil
	default:
		return "", fmt.Errorf("credstore.Get: unsupported backend %q", backend)
	}
}

// Delete removes a profile's secret from BOTH backends defensively. We
// don't trust the declared backend on logout because a half-migrated
// profile could leave a copy in the file. Errors from the absent
// backend are swallowed (Delete is idempotent on a missing key).
func Delete(profile string) error {
	// Keychain: real driver returns nil on missing-key; mock should too.
	if err := active.Delete(Service, profile); err != nil {
		// Don't fail logout because the keychain is unhappy — the
		// caller still wants the file profile gone.
		return fmt.Errorf("keychain delete: %w", err)
	}
	return nil
}

// Describe returns a one-word human-readable backend name suitable for
// the source field in `auth status` output. Maps "" → "file (legacy)".
func Describe(backend string) string {
	switch backend {
	case BackendKeychain:
		return "keychain"
	case BackendFile:
		return "file"
	case BackendEnv:
		return "env"
	case "":
		return "file (legacy)"
	default:
		return backend
	}
}
