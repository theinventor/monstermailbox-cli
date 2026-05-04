package matcher

import (
	"testing"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
)

func TestExactSenderMatchCaseInsensitive(t *testing.T) {
	m := New()
	entries := []api.WhitelistEntry{{ID: "1", Sender: "billing@stripe.com"}}
	got := m.Match(entries, "Billing@Stripe.COM", "Receipt #42")
	if got == nil || got.ID != "1" {
		t.Fatalf("expected match id=1, got %#v", got)
	}
}

func TestSenderRegex(t *testing.T) {
	m := New()
	entries := []api.WhitelistEntry{{ID: "1", SenderRegex: `^notifications@.*\.github\.com$`}}
	if got := m.Match(entries, "notifications@subdomain.github.com", ""); got == nil {
		t.Fatalf("regex should match subdomain")
	}
	if got := m.Match(entries, "evil@github.com.attacker.io", ""); got != nil {
		t.Fatalf("regex must anchor — got false positive %#v", got)
	}
}

func TestSubjectRegexNarrowsMatch(t *testing.T) {
	m := New()
	entries := []api.WhitelistEntry{{
		ID:           "1",
		Sender:       "billing@stripe.com",
		SubjectRegex: `^Receipt for`,
	}}
	if got := m.Match(entries, "billing@stripe.com", "Receipt for #42"); got == nil {
		t.Fatalf("subject regex must allow matching subject")
	}
	if got := m.Match(entries, "billing@stripe.com", "Phishing attempt"); got != nil {
		t.Fatalf("subject regex must reject non-matching subject")
	}
}

func TestBrokenRegexFailsClosed(t *testing.T) {
	m := New()
	entries := []api.WhitelistEntry{{ID: "1", SenderRegex: `[broken(`}}
	if got := m.Match(entries, "billing@stripe.com", ""); got != nil {
		t.Fatalf("broken regex must NOT match anything (fail-closed)")
	}
}

func TestEmptyFromNeverMatches(t *testing.T) {
	m := New()
	entries := []api.WhitelistEntry{{ID: "1", Sender: "billing@stripe.com"}}
	if got := m.Match(entries, "", ""); got != nil {
		t.Fatalf("empty from must never match")
	}
	if got := m.Match(entries, "  ", ""); got != nil {
		t.Fatalf("whitespace-only from must never match")
	}
}

func TestEmptyEntriesNeverMatches(t *testing.T) {
	m := New()
	if got := m.Match(nil, "x@y.com", "anything"); got != nil {
		t.Fatalf("empty whitelist must never match")
	}
}

func TestExactBeforeRegexPrecedence(t *testing.T) {
	m := New()
	entries := []api.WhitelistEntry{
		{ID: "regex", SenderRegex: `^.+@stripe\.com$`},
		{ID: "exact", Sender: "billing@stripe.com"},
	}
	got := m.Match(entries, "billing@stripe.com", "")
	if got == nil || got.ID != "exact" {
		t.Fatalf("exact match must win over regex; got %#v", got)
	}
}

func TestRegexCacheReusedAcrossMatches(t *testing.T) {
	m := New()
	entries := []api.WhitelistEntry{{ID: "1", SenderRegex: `^foo@bar\.com$`}}
	for i := 0; i < 100; i++ {
		_ = m.Match(entries, "foo@bar.com", "")
	}
	if cached := m.compiled[`^foo@bar\.com$`]; cached == nil {
		t.Fatalf("expected pattern to be cached after reuse")
	}
}
