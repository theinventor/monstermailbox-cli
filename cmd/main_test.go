package cmd

import (
	"os"
	"testing"

	"github.com/theinventor/monstermailbox-cli/internal/credstore"
)

// TestMain installs an in-memory keychain for the entire cmd package
// test process. Without this, `mmb auth login --storage=auto` (the new
// default in v0.3.0) writes secrets into the developer's REAL OS
// keychain — pollution we hit once during initial development. The mock
// keyring satisfies KeychainAvailable() and PutGet roundtrips so tests
// that exercise the keychain backend run end-to-end without OS state.
//
// Tests that need to exercise the file backend explicitly should pass
// `--storage=file`; the mock is just the safety net.
func TestMain(m *testing.M) {
	_, restore := credstore.UseMockKeyring()
	defer restore()
	os.Exit(m.Run())
}
