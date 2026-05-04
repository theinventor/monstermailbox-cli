package client

import (
	"strings"
	"testing"
)

// Pinned at the package level: the production API lives on the
// `api.` subdomain. v0.2.0 had this pointing at the marketing host
// (`monstermailbox.com`), which returned 405 with an empty body for
// /agents/register and made `mmb register` look like a no-op. Catch
// any future regression at compile-test time, not from a user.
func TestDefaultAPIURLIsAPISubdomain(t *testing.T) {
	if !strings.HasPrefix(DefaultAPIURL, "https://api.") {
		t.Errorf("DefaultAPIURL must point at the api subdomain; got %q", DefaultAPIURL)
	}
	if DefaultAPIURL == "https://monstermailbox.com" {
		t.Errorf("DefaultAPIURL is the marketing host; this is the v0.2.0 bug — must be api.monstermailbox.com")
	}
}
