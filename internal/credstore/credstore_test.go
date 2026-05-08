package credstore

import (
	"errors"
	"testing"
)

func TestPutGetRoundtripKeychain(t *testing.T) {
	_, restore := UseMockKeyring()
	defer restore()

	got, err := Put("alpha", BackendKeychain, "mmb_TEST_alpha_long")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got != BackendKeychain {
		t.Errorf("Put returned backend %q, want keychain", got)
	}
	v, err := Get("alpha", BackendKeychain, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != "mmb_TEST_alpha_long" {
		t.Errorf("Get returned %q, want roundtrip value", v)
	}
}

func TestGetFileBackendUsesFileSecret(t *testing.T) {
	_, restore := UseMockKeyring()
	defer restore()

	// Even though the keychain is empty, file-backend Get returns the
	// fileSecret arg verbatim.
	v, err := Get("alpha", BackendFile, "mmb_TEST_file_secret")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != "mmb_TEST_file_secret" {
		t.Errorf("Get(file) returned %q", v)
	}
}

func TestGetLegacyEmptyBackendUsesFileSecret(t *testing.T) {
	_, restore := UseMockKeyring()
	defer restore()
	// Legacy pre-v0.3.0 profiles have Backend == "". Treat as file.
	v, err := Get("legacy", "", "mmb_TEST_legacy_in_file")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != "mmb_TEST_legacy_in_file" {
		t.Errorf("Get(\"\") returned %q", v)
	}
}

func TestGetKeychainMissingReturnsErrNotFound(t *testing.T) {
	_, restore := UseMockKeyring()
	defer restore()
	_, err := Get("ghost", BackendKeychain, "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	_, restore := UseMockKeyring()
	defer restore()

	// Delete on a missing key should not error.
	if err := Delete("nonexistent"); err != nil {
		t.Errorf("Delete on missing: %v", err)
	}

	// Roundtrip through put → delete → get-not-found.
	if _, err := Put("alpha", BackendKeychain, "mmb_TEST_alpha_long"); err != nil {
		t.Fatal(err)
	}
	if err := Delete("alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("alpha", BackendKeychain, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after Delete; got %v", err)
	}
}

func TestKeychainAvailableHonorsDisableEnv(t *testing.T) {
	_, restore := UseMockKeyring()
	defer restore()
	t.Setenv(EnvDisableKeychain, "1")
	if KeychainAvailable() {
		t.Error("MMB_DISABLE_KEYCHAIN=1 should force unavailable")
	}
}

func TestKeychainAvailableProbeFailureMeansUnavailable(t *testing.T) {
	m, restore := UseMockKeyring()
	defer restore()
	m.FailGet = true
	if KeychainAvailable() {
		t.Error("failing probe should report unavailable")
	}
}

func TestResolveBackendAutoPicksKeychainWhenAvailable(t *testing.T) {
	_, restore := UseMockKeyring()
	defer restore()
	t.Setenv(EnvDisableKeychain, "")

	got, err := ResolveBackend(BackendAuto)
	if err != nil {
		t.Fatal(err)
	}
	if got != BackendKeychain {
		t.Errorf("ResolveBackend(auto) = %q, want keychain", got)
	}
}

func TestResolveBackendAutoFallsBackToFile(t *testing.T) {
	m, restore := UseMockKeyring()
	defer restore()
	m.FailGet = true

	got, err := ResolveBackend(BackendAuto)
	if err != nil {
		t.Fatal(err)
	}
	if got != BackendFile {
		t.Errorf("ResolveBackend(auto) with broken keychain = %q, want file", got)
	}
}

func TestResolveBackendKeychainExplicitFailsWhenUnavailable(t *testing.T) {
	_, restore := UseMockKeyring()
	defer restore()
	t.Setenv(EnvDisableKeychain, "1")
	if _, err := ResolveBackend(BackendKeychain); err == nil {
		t.Error("explicit keychain on unavailable host should error")
	}
}

func TestResolveBackendFileAlwaysWorks(t *testing.T) {
	_, restore := UseMockKeyring()
	defer restore()
	got, err := ResolveBackend(BackendFile)
	if err != nil {
		t.Fatal(err)
	}
	if got != BackendFile {
		t.Errorf("ResolveBackend(file) = %q, want file", got)
	}
}

func TestResolveBackendUnknownErrors(t *testing.T) {
	if _, err := ResolveBackend("nope"); err == nil {
		t.Error("expected error for unknown backend")
	}
}

func TestResolveBackendEnvVarOverridesEmpty(t *testing.T) {
	_, restore := UseMockKeyring()
	defer restore()
	t.Setenv(EnvStorage, BackendFile)
	got, err := ResolveBackend("")
	if err != nil {
		t.Fatal(err)
	}
	if got != BackendFile {
		t.Errorf("MMB_STORAGE=file with empty arg = %q, want file", got)
	}
}

func TestDescribeMapsLegacy(t *testing.T) {
	if Describe("") != "file (legacy)" {
		t.Errorf("Describe(\"\") = %q", Describe(""))
	}
	if Describe(BackendKeychain) != "keychain" {
		t.Error("keychain")
	}
	if Describe(BackendFile) != "file" {
		t.Error("file")
	}
	if Describe(BackendEnv) != "env" {
		t.Error("env")
	}
}
